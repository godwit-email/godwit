package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/rpc"
)

// target is a purpose-built RPC target covering every supported signature.
type target struct {
	blockStarted   chan struct{}
	blockCancelled chan struct{}
	backlogExit    chan struct{}
	floodDone      chan struct{}
	gatedStopped   chan struct{}
}

func newTarget() *target {
	return &target{
		blockStarted:   make(chan struct{}, 64),
		blockCancelled: make(chan struct{}, 64),
		backlogExit:    make(chan struct{}),
		floodDone:      make(chan struct{}, 64),
		gatedStopped:   make(chan struct{}, 64),
	}
}

type point struct{ X, Y int }

func (t *target) Void()                          {}
func (t *target) VoidCtx(ctx context.Context)    {}
func (t *target) Echo(s string, n int) string    { return fmt.Sprintf("%s%d", s, n) }
func (t *target) Pair(x int) (int, error)        { return x * 2, nil }
func (t *target) Struct(p point) (*point, error) { return &p, nil }
func (t *target) Leak() error {
	return &rpc.WireError{Code: -32001, Message: "password=x", Data: &rpc.WireErrorData{Code: "x", Detail: json.RawMessage(`"secret"`)}}
}

// Gated returns, only once its ctx is cancelled, a channel that closes only
// when the returned stop func is called.
func (t *target) Gated(ctx context.Context) (<-chan int, func()) {
	t.blockStarted <- struct{}{}
	<-ctx.Done()
	ch := make(chan int)
	return ch, func() { close(ch); t.gatedStopped <- struct{}{} }
}

func (t *target) Hidden() int { return 1 }

func (t *target) Fail(fail bool) error {
	if fail {
		return errors.New("secret detail")
	}
	return nil
}

func (t *target) Multi(ctx context.Context) (string, int, bool, error) { return "a", 1, true, nil }
func (t *target) MultiFail() (string, int, error)                      { return "", 0, errors.New("boom") }

// Ints's events are queued before the call returns, so the pump starts at once.
func (t *target) Ints() (<-chan int, error) {
	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	ch <- 3
	close(ch)
	return ch, nil
}

func (t *target) Sub() (<-chan string, func()) {
	ch := make(chan string, 1)
	ch <- "x"
	return ch, func() { close(ch) }
}

func (t *target) Block(ctx context.Context) error {
	t.blockStarted <- struct{}{}
	<-ctx.Done()
	t.blockCancelled <- struct{}{}
	return ctx.Err()
}

// Backlog emits one event, waits for cancellation, then still has a backlog;
// its pump exits only once someone reads everything.
func (t *target) Backlog(ctx context.Context) (<-chan int, error) {
	ch := make(chan int)
	go func() {
		defer close(t.backlogExit)
		defer close(ch)
		ch <- 0
		<-ctx.Done()
		for i := 1; i <= 100; i++ {
			ch <- i
		}
	}()
	return ch, nil
}

// Flood ignores its context; only draining lets it finish.
func (t *target) Flood(n int) (<-chan int, error) {
	ch := make(chan int)
	go func() {
		defer func() { t.floodDone <- struct{}{} }()
		defer close(ch)
		for i := range n {
			ch <- i
		}
	}()
	return ch, nil
}

func expose(m string) bool { return m != "Hidden" }

func newDispatcher(t *testing.T, tg any) *rpc.Dispatcher {
	t.Helper()
	d, err := rpc.NewDispatcher(tg, expose)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// serve runs ServeConn on one end of a pipe and returns the other end.
func serve(t *testing.T, d *rpc.Dispatcher, opts rpc.ConnOptions) (net.Conn, <-chan error) {
	t.Helper()
	return serveWrapped(t, d, opts, func(c net.Conn) net.Conn { return c })
}

// serveWrapped is serve with the server's end of the pipe wrapped.
func serveWrapped(t *testing.T, d *rpc.Dispatcher, opts rpc.ConnOptions, wrap func(net.Conn) net.Conn) (net.Conn, <-chan error) {
	t.Helper()
	pipe, cli := net.Pipe()
	srv := wrap(pipe)
	done := make(chan error, 1)
	go func() {
		done <- rpc.ServeConn(context.Background(), srv, d, opts)
		close(done) // later waits return at once
	}()
	t.Cleanup(func() {
		cli.Close()
		waitDone(t, done)
	})
	return cli, done
}

func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ServeConn did not return")
	}
}

func wait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func client(t *testing.T, d *rpc.Dispatcher) (*rpc.Client, <-chan error) {
	conn, done := serve(t, d, rpc.ConnOptions{})
	c := rpc.NewClient(conn)
	t.Cleanup(func() { c.Close() })
	return c, done
}

func TestDispatchSignatures(t *testing.T) {
	c, _ := client(t, newDispatcher(t, newTarget()))
	ctx := context.Background()
	call := func(method string, args ...any) string {
		t.Helper()
		var raw json.RawMessage
		if err := c.Call(ctx, method, &raw, args...); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return string(raw)
	}
	for _, tc := range []struct {
		method string
		args   []any
		want   string
	}{
		{"Void", nil, "null"},
		{"VoidCtx", nil, "null"},
		{"Echo", []any{"a", 1}, `"a1"`},
		{"Fail", []any{false}, "null"},
		{"Pair", []any{21}, "42"},
		{"Struct", []any{point{1, 2}}, `{"X":1,"Y":2}`},
		{"Multi", nil, `["a",1,true]`},
	} {
		if got := call(tc.method, tc.args...); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.method, got, tc.want)
		}
	}

	var multi []json.RawMessage
	if err := c.Call(ctx, "Multi", &multi); err != nil || len(multi) != 3 {
		t.Fatalf("Multi into slice = %v, %v", multi, err)
	}

	for _, m := range []string{"Fail", "MultiFail"} {
		args := []any{}
		if m == "Fail" {
			args = append(args, true)
		}
		err := c.Call(ctx, m, nil, args...)
		var we *rpc.WireError
		if !errors.As(err, &we) || we.Code != -32603 || we.Message != "internal error" {
			t.Errorf("%s error = %#v, want the default internal error", m, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("default encoding leaked the error text: %v", err)
		}
	}

	events, cancel, err := c.Stream(ctx, "Ints")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for ev := range events {
		got = append(got, string(ev))
	}
	cancel()
	if strings.Join(got, ",") != "1,2,3" {
		t.Errorf("Ints events = %v", got)
	}

	events, cancel, err = c.Stream(ctx, "Sub")
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-events; string(ev) != `"x"` {
		t.Errorf("Sub event = %s", ev)
	}
	cancel()
	if _, ok := <-events; ok {
		t.Error("events still open after cancel")
	}
}

type bad struct{}

func (bad) TwoNoErr() (int, string)        { return 0, "" }
func (bad) ChanNoErr() <-chan int          { return nil }
func (bad) ChanArg(chan int)               {}
func (bad) FuncArg(func())                 {}
func (bad) FuncResult() (func(), error)    { return nil, nil }
func (bad) Variadic(...int)                {}
func (bad) ErrFirst() (error, int)         { return nil, 0 }
func (bad) StreamWrong() (<-chan int, int) { return nil, 0 }
func (bad) SendChan() (chan<- int, error)  { return nil, nil }
func (bad) CtxLate(int, context.Context)   {}

func TestNewDispatcherRejectsUnsupported(t *testing.T) {
	for _, name := range []string{"TwoNoErr", "ChanNoErr", "ChanArg", "FuncArg", "FuncResult", "Variadic", "ErrFirst", "StreamWrong", "SendChan", "CtxLate"} {
		_, err := rpc.NewDispatcher(bad{}, func(m string) bool { return m == name })
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want an error naming the method", name, err)
		}
	}
	if _, err := rpc.NewDispatcher(bad{}, func(string) bool { return false }); err != nil {
		t.Errorf("nothing exposed: %v", err)
	}
}

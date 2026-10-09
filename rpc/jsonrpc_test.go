package rpc_test

import (
	"bufio"
	"bytes"
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

type wire struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
	Result  json.RawMessage   `json:"result"`
	Error   *rpc.WireError    `json:"error"`
}

// raw speaks the wire protocol by hand.
type raw struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func newRaw(t *testing.T, conn net.Conn) *raw { return &raw{t, conn, bufio.NewReader(conn)} }

func (r *raw) send(format string, args ...any) {
	r.t.Helper()
	r.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(r.conn, format+"\n", args...); err != nil {
		r.t.Fatalf("send: %v", err)
	}
}

func (r *raw) recv() wire {
	r.t.Helper()
	r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.r.ReadBytes('\n')
	if err != nil {
		r.t.Fatalf("recv: %v", err)
	}
	var m wire
	if err := json.Unmarshal(line, &m); err != nil || m.JSONRPC != "2.0" {
		r.t.Fatalf("bad line %q: %v", line, err)
	}
	return m
}

// until reads messages until the response with the given id.
func (r *raw) until(id string) wire {
	r.t.Helper()
	for {
		if m := r.recv(); string(m.ID) == id {
			return m
		}
	}
}

func (r *raw) closed() {
	r.t.Helper()
	r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, err := r.r.ReadBytes('\n'); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				r.t.Fatal("connection still open")
			}
			return
		}
	}
}

func TestDispatchRejects(t *testing.T) {
	tg := newTarget()
	conn, done := serve(t, newDispatcher(t, tg), rpc.ConnOptions{})
	r := newRaw(t, conn)
	for _, tc := range []struct {
		line string
		id   string
		code int
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"Nope","params":[]}`, "1", -32601},
		{`{"jsonrpc":"2.0","id":2,"method":"Hidden","params":[]}`, "2", -32601},
		{`{"jsonrpc":"2.0","id":3,"method":"Echo","params":["a"]}`, "3", -32602},
		{`{"jsonrpc":"2.0","id":4,"method":"Echo","params":[1,"a"]}`, "4", -32602},
		{`{"jsonrpc":"2.0","id":5,"method":"Echo","params":{"s":"a"}}`, "5", -32602},
		{`{"jsonrpc":"2.0","id":"6","method":"Void","params":[]}`, "null", -32600},
		{`[{"jsonrpc":"2.0","id":7,"method":"Void","params":[]}]`, "null", -32600},
		{`{"jsonrpc":"2.0","id":0,"method":"Void","params":[]}`, "null", -32600},
		{`{"jsonrpc":"2.0","id":-1,"method":"Void","params":[]}`, "null", -32600},
		{`{"jsonrpc":"2.0","id":1.5,"method":"Void","params":[]}`, "null", -32600},
		{`{"jsonrpc":"2.0","method":"Void","params":[]}`, "null", -32600},
		{`{"id":10,"method":"Void","params":[]}`, "10", -32600},
		{`not json`, "null", -32700},
	} {
		r.send("%s", tc.line)
		m := r.recv()
		if string(m.ID) != tc.id || m.Error == nil || m.Error.Code != tc.code {
			t.Errorf("%s → id %s error %+v, want id %s code %d", tc.line, m.ID, m.Error, tc.id, tc.code)
		}
	}
	// Unknown control notifications are ignored; the connection still works.
	r.send(`{"jsonrpc":"2.0","method":"$/whatever","params":[]}`)
	r.send(`{"jsonrpc":"2.0","id":11,"method":"Echo","params":["a",1]}`)
	if m := r.recv(); string(m.ID) != "11" || string(m.Result) != `"a1"` {
		t.Fatalf("Echo after rejects = %+v", m)
	}

	// A duplicate in-flight ID is a protocol violation: disconnect.
	r.send(`{"jsonrpc":"2.0","id":12,"method":"Block","params":[]}`)
	wait(t, tg.blockStarted, "Block to start")
	r.send(`{"jsonrpc":"2.0","id":12,"method":"Block","params":[]}`)
	r.closed()
	waitDone(t, done)
	wait(t, tg.blockCancelled, "Block to be cancelled by the disconnect")
}

func TestStreamOrdering(t *testing.T) {
	conn, _ := serve(t, newDispatcher(t, newTarget()), rpc.ConnOptions{})
	r := newRaw(t, conn)
	for i := 1; i <= 50; i++ {
		r.send(`{"jsonrpc":"2.0","id":%d,"method":"Ints","params":[]}`, i)
		m := r.recv()
		if string(m.ID) != fmt.Sprint(i) || string(m.Result) != fmt.Sprintf(`{"stream":%d}`, i) {
			t.Fatalf("first message = %+v, want the response", m)
		}
		for _, want := range []string{"1", "2", "3"} {
			m := r.recv()
			if m.Method != "$/stream" || len(m.Params) != 2 || string(m.Params[0]) != fmt.Sprint(i) || string(m.Params[1]) != want {
				t.Fatalf("event = %+v, want %s", m, want)
			}
		}
		if m := r.recv(); m.Method != "$/stream.end" || len(m.Params) != 1 || string(m.Params[0]) != fmt.Sprint(i) {
			t.Fatalf("end = %+v", m)
		}
	}
}

func TestStreamCancelDrains(t *testing.T) {
	tg := newTarget()
	c, _ := client(t, newDispatcher(t, tg))
	events, cancel, err := c.Stream(context.Background(), "Backlog")
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-events; string(ev) != "0" {
		t.Fatalf("first event = %s", ev)
	}
	cancel()
	wait(t, tg.backlogExit, "the target's pump to exit")
	var s string
	if err := c.Call(context.Background(), "Echo", &s, "a", 1); err != nil || s != "a1" {
		t.Fatalf("Echo after cancel = %q, %v", s, err)
	}
}

func TestStreamCancelOnDisconnect(t *testing.T) {
	tg := newTarget()
	c, done := client(t, newDispatcher(t, tg))
	events, _, err := c.Stream(context.Background(), "Backlog")
	if err != nil {
		t.Fatal(err)
	}
	<-events
	c.Close()
	wait(t, tg.backlogExit, "the target's pump to exit")
	waitDone(t, done)
	if _, ok := <-events; ok {
		t.Error("events still open after Close")
	}
	if !errors.Is(c.StreamErr(events), rpc.ErrClosed) {
		t.Errorf("StreamErr = %v, want ErrClosed", c.StreamErr(events))
	}
}

func TestRequestCancel(t *testing.T) {
	tg := newTarget()
	c, _ := client(t, newDispatcher(t, tg))
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, "Block", nil) }()
	wait(t, tg.blockStarted, "Block to start")
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call = %v, want context.Canceled", err)
	}
	wait(t, tg.blockCancelled, "the target's ctx to be cancelled")
}

func TestServerLimits(t *testing.T) {
	t.Run("message size", func(t *testing.T) {
		conn, done := serve(t, newDispatcher(t, newTarget()), rpc.ConnOptions{})
		go func() {
			line := append([]byte(`{"jsonrpc":"2.0","id":1,"method":"Echo","params":["`), bytes.Repeat([]byte("a"), 16<<20)...)
			conn.Write(append(line, "\",1]}\n"...))
		}()
		waitDone(t, done)
	})
	t.Run("active budget", func(t *testing.T) {
		tg := newTarget()
		conn, _ := serve(t, newDispatcher(t, tg), rpc.ConnOptions{})
		r := newRaw(t, conn)
		for i := 1; i <= 31; i++ {
			r.send(`{"jsonrpc":"2.0","id":%d,"method":"Block","params":[]}`, i)
		}
		r.send(`{"jsonrpc":"2.0","id":32,"method":"Sub","params":[]}`)
		r.send(`{"jsonrpc":"2.0","id":33,"method":"Echo","params":["a",1]}`)
		if m := r.until("33"); m.Error == nil || m.Error.Message != "too many requests" {
			t.Fatalf("33rd = %+v, want too many requests", m)
		}
		// Finishing one frees its slot.
		r.send(`{"jsonrpc":"2.0","method":"$/cancel","params":[1]}`)
		r.until("1")
		r.send(`{"jsonrpc":"2.0","id":34,"method":"Echo","params":["a",1]}`)
		if m := r.until("34"); string(m.Result) != `"a1"` {
			t.Fatalf("after a slot frees = %+v", m)
		}
	})
}

func TestSlowConsumerDisconnected(t *testing.T) {
	tg := newTarget()
	d := newDispatcher(t, tg)
	t.Run("queue full", func(t *testing.T) {
		slow, done := serve(t, d, rpc.ConnOptions{QueueMessages: 8})
		fmt.Fprintf(slow, `{"jsonrpc":"2.0","id":1,"method":"Flood","params":[1000]}`+"\n")
		// Nobody reads slow: its queue fills and the server drops it.
		waitDone(t, done)
		wait(t, tg.floodDone, "the flood pump to be drained")

		c, _ := client(t, d)
		var s string
		if err := c.Call(context.Background(), "Echo", &s, "a", 1); err != nil || s != "a1" {
			t.Fatalf("other connection: %q, %v", s, err)
		}
	})
	t.Run("write timeout", func(t *testing.T) {
		slow, done := serve(t, d, rpc.ConnOptions{WriteTimeout: 50 * time.Millisecond})
		fmt.Fprintf(slow, `{"jsonrpc":"2.0","id":1,"method":"Echo","params":["a",1]}`+"\n")
		waitDone(t, done)
	})
}

func TestClientStreamBackpressure(t *testing.T) {
	tg := newTarget()
	c, _ := client(t, newDispatcher(t, tg))
	ctx := context.Background()
	flood, cancel, err := c.Stream(ctx, "Flood", 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	wait(t, tg.floodDone, "the flood pump to finish")

	// Other calls and streams still work while flood is unread.
	ints, cancelInts, err := c.Stream(ctx, "Ints")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for ev := range ints {
		got = append(got, string(ev))
	}
	cancelInts()
	if strings.Join(got, ",") != "1,2,3" {
		t.Fatalf("Ints = %v", got)
	}
	var s string
	if err := c.Call(ctx, "Echo", &s, "a", 1); err != nil || s != "a1" {
		t.Fatalf("Echo = %q, %v", s, err)
	}

	n := 0
	for range flood {
		n++
	}
	if n == 0 || n >= 1000 {
		t.Errorf("flood delivered %d events, want a bounded prefix", n)
	}
	if err := c.StreamErr(flood); !errors.Is(err, rpc.ErrStreamOverflow) {
		t.Errorf("StreamErr = %v, want ErrStreamOverflow", err)
	}
}

func TestTargetErrorsAlwaysEncoded(t *testing.T) {
	c, _ := client(t, newDispatcher(t, newTarget()))
	err := c.Call(context.Background(), "Leak", nil)
	var we *rpc.WireError
	if !errors.As(err, &we) || we.Message != "internal error" || we.Code != -32603 {
		t.Fatalf("Leak = %#v, want the encoder's internal error", err)
	}
	if b, _ := json.Marshal(we); bytes.Contains(b, []byte("secret")) || bytes.Contains(b, []byte("password")) {
		t.Fatalf("target's WireError leaked: %s", b)
	}
}

func TestCancelBeforeStreamReturns(t *testing.T) {
	tg := newTarget()
	conn, _ := serve(t, newDispatcher(t, tg), rpc.ConnOptions{MaxActive: 1})
	r := newRaw(t, conn)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"Gated","params":[]}`)
	wait(t, tg.blockStarted, "Gated to start")
	r.send(`{"jsonrpc":"2.0","method":"$/cancel","params":[1]}`)
	if m := r.until("1"); m.Error == nil {
		t.Fatalf("cancelled stream call = %+v, want an error", m)
	}
	wait(t, tg.gatedStopped, "stop to be called")
	r.send(`{"jsonrpc":"2.0","id":2,"method":"Echo","params":["a",1]}`)
	if m := r.until("2"); string(m.Result) != `"a1"` {
		t.Fatalf("slot not released: %+v", m)
	}
}

func TestClientWriteHonoursCtx(t *testing.T) {
	t.Run("peer not reading", func(t *testing.T) {
		srv, cli := net.Pipe()
		defer srv.Close()
		c := rpc.NewClient(cli)
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		errc := make(chan error, 200)
		for range 200 {
			go func() { errc <- c.Call(ctx, "Echo", nil, "a", 1) }()
		}
		go func() {
			_, _, err := c.Stream(ctx, "Ints")
			errc <- err
		}()
		deadline := time.After(3 * time.Second)
		for range 201 {
			select {
			case err := <-errc:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err = %v, want DeadlineExceeded", err)
				}
			case <-deadline:
				t.Fatal("a call blocked on a peer that does not read")
			}
		}
	})
	t.Run("pre-cancelled ctx sends nothing", func(t *testing.T) {
		srv, cli := net.Pipe()
		defer srv.Close()
		c := rpc.NewClient(cli)
		defer c.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := make(chan []byte, 1)
		go func() {
			srv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			b := make([]byte, 1)
			n, _ := srv.Read(b)
			got <- b[:n]
		}()
		if err := c.Call(ctx, "Echo", nil, "a", 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v", err)
		}
		if _, _, err := c.Stream(ctx, "Ints"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream = %v", err)
		}
		if b := <-got; len(b) != 0 {
			t.Fatalf("wrote %q for a cancelled call", b)
		}
	})
}

type badDeadline struct{ net.Conn }

func (badDeadline) SetWriteDeadline(time.Time) error { return errors.New("no deadlines") }

func TestWriteDeadlineErrorCloses(t *testing.T) {
	conn, done := serveWrapped(t, newDispatcher(t, newTarget()), rpc.ConnOptions{}, func(c net.Conn) net.Conn { return badDeadline{c} })
	fmt.Fprintf(conn, `{"jsonrpc":"2.0","id":1,"method":"Echo","params":["a",1]}`+"\n")
	waitDone(t, done)
}

func TestHelloGate(t *testing.T) {
	d := newDispatcher(t, newTarget())
	opts := rpc.ConnOptions{AuthTimeout: time.Minute, Hello: func(params []json.RawMessage) (func(string, []json.RawMessage) error, any, error) {
		if len(params) != 1 || string(params[0]) != `"tok"` {
			return nil, nil, &rpc.WireError{Code: 1, Message: "bad token tok=secret"}
		}
		return func(m string, _ []json.RawMessage) error {
			if m == "Pair" {
				return rpc.ErrForbidden
			}
			return nil
		}, "hi", nil
	}}
	t.Run("control before hello closes", func(t *testing.T) {
		conn, done := serve(t, d, opts)
		newRaw(t, conn).send(`{"jsonrpc":"2.0","method":"$/cancel","params":[1]}`)
		waitDone(t, done)
	})
	t.Run("call before hello closes", func(t *testing.T) {
		conn, done := serve(t, d, opts)
		r := newRaw(t, conn)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"Echo","params":["a",1]}`)
		if m := r.recv(); m.Error == nil || m.Error.Code != -32002 {
			t.Fatalf("= %+v, want unauthenticated", m)
		}
		r.closed()
		waitDone(t, done)
	})
	t.Run("bad token is encoded and closes", func(t *testing.T) {
		conn, done := serve(t, d, opts)
		r := newRaw(t, conn)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["nope"]}`)
		if m := r.recv(); m.Error == nil || m.Error.Message != "internal error" {
			t.Fatalf("= %+v, want the encoded error", m)
		}
		r.closed()
		waitDone(t, done)
	})
	t.Run("authenticated", func(t *testing.T) {
		conn, _ := serve(t, d, opts)
		r := newRaw(t, conn)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["tok"]}`)
		if m := r.recv(); string(m.Result) != `"hi"` {
			t.Fatalf("hello = %+v", m)
		}
		r.send(`{"jsonrpc":"2.0","id":2,"method":"Echo","params":["a",1]}`)
		if m := r.recv(); string(m.Result) != `"a1"` {
			t.Fatalf("Echo = %+v", m)
		}
		r.send(`{"jsonrpc":"2.0","id":3,"method":"Pair","params":[1]}`)
		if m := r.recv(); m.Error == nil || m.Error.Message != "forbidden" {
			t.Fatalf("Pair = %+v, want forbidden", m)
		}
		r.send(`{"jsonrpc":"2.0","id":4,"method":"hello","params":["tok"]}`)
		if m := r.recv(); m.Error == nil {
			t.Fatalf("second hello = %+v, want an error", m)
		}
	})
}

func TestCancelRacesRegistration(t *testing.T) {
	check := func(t *testing.T, tg *target, r *raw) {
		t.Helper()
		r.until("1")
		wait(t, tg.gatedStopped, "stop to be called")
		r.send(`{"jsonrpc":"2.0","id":2,"method":"Echo","params":["a",1]}`)
		if m := r.until("2"); string(m.Result) != `"a1"` {
			t.Fatalf("slot not released: %+v", m)
		}
	}
	t.Run("cancel after the method returned", func(t *testing.T) {
		tg := newTarget()
		reached := make(chan struct{})
		opts := rpc.WithHooks(rpc.ConnOptions{MaxActive: 1}, func(ctx context.Context) {
			close(reached)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}, nil, nil)
		conn, _ := serve(t, newDispatcher(t, tg), opts)
		r := newRaw(t, conn)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"Stopper","params":[]}`)
		wait(t, reached, "the method to return")
		r.send(`{"jsonrpc":"2.0","method":"$/cancel","params":[1]}`)
		check(t, tg, r)
	})
	t.Run("cancel lands as the stream registers", func(t *testing.T) {
		tg := newTarget()
		reached, proceed, inCancel := make(chan struct{}), make(chan struct{}), make(chan struct{})
		registered := make(chan struct{}, 1)
		opts := rpc.WithHooks(rpc.ConnOptions{MaxActive: 1},
			func(context.Context) { close(reached); <-proceed },
			func() { registered <- struct{}{} },
			func() {
				close(inCancel)
				// Let registration run now if it can; under a correct lock it can't.
				select {
				case <-registered:
				case <-time.After(300 * time.Millisecond):
				}
			})
		conn, _ := serve(t, newDispatcher(t, tg), opts)
		r := newRaw(t, conn)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"Stopper","params":[]}`)
		wait(t, reached, "the method to return")
		r.send(`{"jsonrpc":"2.0","method":"$/cancel","params":[1]}`)
		wait(t, inCancel, "$/cancel to find the request")
		close(proceed)
		check(t, tg, r)
	})
}

func TestClientNeverDropsStreamCancel(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	c := rpc.NewClient(cli)
	defer c.Close()
	r := bufio.NewReader(srv)

	type res struct {
		cancel func()
		err    error
	}
	got := make(chan res, 1)
	go func() {
		_, cancel, err := c.Stream(context.Background(), "Sub")
		got <- res{cancel, err}
	}()
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(srv, `{"jsonrpc":"2.0","id":1,"result":{"stream":7}}`+"\n")
	st := <-got
	if st.err != nil {
		t.Fatal(st.err)
	}

	// The peer stops reading; fill the client's write queue.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	errc := make(chan error, 100)
	for range 100 {
		go func() { errc <- c.Call(ctx, "Echo", nil) }()
	}
	for range 100 {
		<-errc
	}
	st.cancel()

	// The peer reads again: the stream cancel must arrive.
	srv.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("no $/stream.cancel received: %v", err)
		}
		if bytes.Contains(line, []byte(`"$/stream.cancel","params":[7]`)) {
			return
		}
	}
}

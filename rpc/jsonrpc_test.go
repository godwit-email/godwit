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

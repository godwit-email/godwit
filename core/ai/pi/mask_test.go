package pi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testllm"
)

const card = "4111 1111 1111 1111"

// cardMask stands in for masking: the card number goes as [card]. calls
// counts the requests it masked.
func cardMask(calls *atomic.Int32) func(string) (string, error) {
	return func(body string) (string, error) {
		calls.Add(1)
		return strings.ReplaceAll(body, card, "[card]"), nil
	}
}

func TestARequestLeavesMasked(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	var calls atomic.Int32
	_, err := Generate(t.Context(), http.DefaultClient, Request{API: "openai-completions", BaseURL: srv.URL, Model: "m", APIKey: "k",
		System: "Mail: " + card, Prompt: "Is " + card + " mine?"}, Hooks{Mask: cardMask(&calls)})
	if err != nil {
		t.Fatal(err)
	}
	if b := string(srv.Requests()[0].Body); strings.Contains(b, card) || strings.Count(b, "[card]") != 2 {
		t.Errorf("body %s", b)
	}
}

// droppingConn is a connection the server dropped while it was idle: once
// drop is set, a write fails before anything is written.
type droppingConn struct {
	net.Conn
	drop atomic.Bool
}

func (c *droppingConn) Write(b []byte) (int, error) {
	if c.drop.Load() {
		return 0, errors.New("connection reset by peer")
	}
	return c.Conn.Write(b)
}

// net/http sends a request again on a new connection when the reused one it
// was given fails before anything was written, as it does after an HTTP/2
// GOAWAY, with the body GetBody gives it: that body is the masked one too.
func TestARetryLeavesMasked(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"ok"}}, testllm.Turn{Text: []string{"ok"}})
	var mu sync.Mutex
	var conns []*droppingConn
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		conns = append(conns, &droppingConn{Conn: c})
		return conns[len(conns)-1], nil
	}}}
	var calls atomic.Int32
	req := Request{API: "openai-completions", BaseURL: srv.URL, Model: "m", APIKey: "k", Prompt: "Is " + card + " mine?"}
	if _, err := Generate(t.Context(), client, req, Hooks{Mask: cardMask(&calls)}); err != nil {
		t.Fatal(err)
	}
	conns[0].drop.Store(true)
	res, err := Generate(t.Context(), client, req, Hooks{Mask: cardMask(&calls)})
	if err != nil || res.StopReason != "stop" {
		t.Fatalf("result %+v, %v", res, err)
	}
	if len(conns) != 2 || calls.Load() != 2 {
		t.Fatalf("%d connections, %d masked: net/http did not send it again itself", len(conns), calls.Load())
	}
	reqs := srv.Requests()
	if b := string(reqs[len(reqs)-1].Body); len(reqs) != 2 || strings.Contains(b, card) || !strings.Contains(b, "[card]") {
		t.Errorf("%d requests, the last %s", len(reqs), b)
	}
}

// A request that cannot be masked is not sent, nor tried again: the run fails.
func TestARequestThatCannotBeMaskedIsNotSent(t *testing.T) {
	broken := Hooks{Mask: func(string) (string, error) { return "", errors.New("a rule failed") }}
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	start := time.Now()
	_, err := Generate(t.Context(), http.DefaultClient, Request{API: "openai-completions", BaseURL: srv.URL, Model: "m", APIKey: "k", Prompt: "hi"}, broken)
	if err == nil || !strings.Contains(err.Error(), "a rule failed") {
		t.Errorf("err %v", err)
	}
	_, err = Agent(t.Context(), http.DefaultClient, AgentRequest{Request: Request{API: "openai-completions", BaseURL: srv.URL, Model: "m", APIKey: "k"},
		Messages: []json.RawMessage{question("q")}, MaxTurns: 1}, broken)
	if err == nil {
		t.Error("the agent ran")
	}
	_, err = Judge(t.Context(), http.DefaultClient, JudgeRequest{URL: srv.URL + "/decisions", Body: json.RawMessage(`{}`)}, 1<<20, broken)
	if err == nil {
		t.Error("the judge ran")
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v: tried again", d)
	}
}

package jmap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// shortTimeouts shortens the HTTP timeouts for one test.
func shortTimeouts(t *testing.T) {
	h, r, s, w := conn.HeaderTimeout, conn.RequestTimeout, conn.StallTimeout, watchStall
	conn.HeaderTimeout, conn.RequestTimeout, conn.StallTimeout, watchStall = 200*time.Millisecond, time.Second, 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { conn.HeaderTimeout, conn.RequestTimeout, conn.StallTimeout, watchStall = h, r, s, w })
}

// stallServer answers the session resource and calls stall for every other
// request, which should block until the client gives up.
func stallServer(t *testing.T, stall http.HandlerFunc) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session" {
			io.WriteString(w, `{"capabilities":{"urn:ietf:params:jmap:core":{}},"accounts":{"a":{}},`+
				`"primaryAccounts":{"urn:ietf:params:jmap:mail":"a"},"apiUrl":"`+srv.URL+`/api",`+
				`"downloadUrl":"`+srv.URL+`/d/{accountId}/{blobId}","eventSourceUrl":"`+srv.URL+`/es?ping={ping}","state":"s"}`)
			return
		}
		stall(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestReceiver(srv *httptest.Server) *Receiver {
	return New(Config{SessionURL: srv.URL + "/session", Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "u", Password: "p"}, nil
	}})
}

// Watch asks for pings and connects again when the stream goes silent.
func TestWatchReconnectsAfterSilence(t *testing.T) {
	shortTimeouts(t)
	var conns atomic.Int32
	r := newTestReceiver(stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "ping="+watchPing) {
			t.Errorf("event source query %q asks for no ping", r.URL.RawQuery)
		}
		conns.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer r.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		for conns.Load() < 2 && ctx.Err() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if err := r.Watch(ctx, func([]backend.FolderRef) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch = %v, want context.Canceled", err)
	}
	if conns.Load() < 2 {
		t.Fatalf("%d connections, want a second after the silence", conns.Load())
	}
}

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
	h, r, s, w := headerTimeout, requestTimeout, stallTimeout, watchStall
	headerTimeout, requestTimeout, stallTimeout, watchStall = 200*time.Millisecond, time.Second, 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { headerTimeout, requestTimeout, stallTimeout, watchStall = h, r, s, w })
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

// hang reads the request, so the server notices the client hang up, and never answers.
func hang(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
}

func newTestReceiver(srv *httptest.Server) *Receiver {
	return New(Config{SessionURL: srv.URL + "/session", Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "u", Password: "p"}, nil
	}})
}

// within fails the test unless f returns an error within d.
func within(t *testing.T, d time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error from a server that never answers")
		}
		return err
	case <-time.After(d):
		t.Fatalf("still waiting after %v", d)
		return nil
	}
}

func TestSilentServerFailsTheRound(t *testing.T) {
	shortTimeouts(t)
	r := newTestReceiver(stallServer(t, hang))
	defer r.Close()
	within(t, 3*time.Second, func() error { _, err := r.Check(t.Context()); return err })
}

func TestStalledDownloadFails(t *testing.T) {
	shortTimeouts(t)
	r := newTestReceiver(stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "From: a\r\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer r.Close()
	err := within(t, 3*time.Second, func() error {
		rc, err := r.c.download(t.Context(), "b")
		if err != nil {
			return errors.New("download failed before its body: " + err.Error())
		}
		defer rc.Close()
		_, err = io.ReadAll(rc)
		return err
	})
	if !errors.Is(err, conn.ErrStalled) {
		t.Fatalf("err = %v, want conn.ErrStalled", err)
	}
}

// A download is not cut off by requestTimeout while bytes keep arriving.
func TestSlowDownloadOutlastsRequestTimeout(t *testing.T) {
	shortTimeouts(t)
	requestTimeout = 300 * time.Millisecond
	r := newTestReceiver(stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		for range 6 {
			io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer r.Close()
	rc, err := r.c.download(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, err := io.ReadAll(rc); err != nil || string(b) != "xxxxxx" {
		t.Fatalf("read %q, %v", b, err)
	}
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

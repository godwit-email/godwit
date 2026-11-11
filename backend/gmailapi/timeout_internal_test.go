package gmailapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// stalledClient is a receiving client whose server calls stall for its first
// request, which should block until the client gives up, and answers the
// profile after that.
func stalledClient(t *testing.T, stall http.HandlerFunc) *client {
	h, r, s := headerTimeout, requestTimeout, stallTimeout
	headerTimeout, requestTimeout, stallTimeout = 200*time.Millisecond, time.Second, 200*time.Millisecond
	t.Cleanup(func() { headerTimeout, requestTimeout, stallTimeout = h, r, s })
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			stall(w, r)
			return
		}
		io.WriteString(w, `{"emailAddress":"a@gmail.com","historyId":"1"}`)
	}))
	t.Cleanup(srv.Close)
	c := newClient(Config{BaseURL: srv.URL, Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "stall-" + t.Name(), Token: "t"}, nil
	}}, requestTimeout)
	t.Cleanup(c.http.CloseIdleConnections)
	return c
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

// A server that never answers fails the call, and the account's request lock
// is free for the next one.
func TestSilentServerFailsAndReleasesTheLock(t *testing.T) {
	c := stalledClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	within(t, 3*time.Second, func() error { _, err := c.getProfile(t.Context()); return err })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := c.getProfile(ctx); err != nil {
		t.Fatalf("the next call: %v", err)
	}
}

func TestStalledMessageFails(t *testing.T) {
	c := stalledClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"m","raw":"`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	err := within(t, 3*time.Second, func() error { _, err := c.getMessage(t.Context(), "m", "raw"); return err })
	if !errors.Is(err, conn.ErrStalled) {
		t.Fatalf("err = %v, want conn.ErrStalled", err)
	}
}

// A whole message is not cut off by requestTimeout while bytes keep arriving.
func TestSlowMessageOutlastsRequestTimeout(t *testing.T) {
	c := stalledClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"m","raw":"`)
		for range 15 {
			io.WriteString(w, "QUFB")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		io.WriteString(w, `"}`)
	})
	m, err := c.getMessage(t.Context(), "m", "raw")
	if err != nil || m.ID != "m" {
		t.Fatalf("getMessage = %+v, %v", m, err)
	}
}

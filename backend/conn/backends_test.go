package conn_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/backend/gmailapi"
	"github.com/godwit-email/godwit/backend/graph"
	"github.com/godwit-email/godwit/backend/jmap"
)

// Every HTTP backend fails a call to a server that never answers, instead of
// holding the account's sync forever.
func TestBackendsGiveUpOnSilentServer(t *testing.T) {
	h, r := conn.HeaderTimeout, conn.RequestTimeout
	conn.HeaderTimeout, conn.RequestTimeout = 200*time.Millisecond, time.Second
	t.Cleanup(func() { conn.HeaderTimeout, conn.RequestTimeout = h, r })
	creds := func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "u-" + t.Name(), Password: "p", Token: "t"}, nil
	}
	for name, open := range map[string]func(url string) backend.Receiver{
		"jmap": func(url string) backend.Receiver { return jmap.New(jmap.Config{SessionURL: url, Credentials: creds}) },
		"graph": func(url string) backend.Receiver {
			return graph.NewReceiver(graph.Config{URL: url, Credentials: creds})
		},
		"ews": func(url string) backend.Receiver { return ews.New(ews.Config{URL: url, Credentials: creds}) },
		"gmailapi": func(url string) backend.Receiver {
			return gmailapi.New(gmailapi.Config{BaseURL: url, Credentials: creds})
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body) // so the server notices the client hang up
				<-r.Context().Done()
			}))
			t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
			r := open(srv.URL)
			defer r.Close()
			done := make(chan error, 1)
			go func() { _, err := r.Describe(context.Background()); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("no error from a server that never answers")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still waiting on a server that never answers")
			}
		})
	}
}

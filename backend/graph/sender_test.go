package graph_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/graph"
)

// server stands in for Graph's sendMail, taking the token "good".
type server struct {
	mu       sync.Mutex
	accepted []backendtest.Delivery
	refused  map[string]bool
	lose     bool
	rejected bool
	url      string
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejected || r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	b, _ := io.ReadAll(r.Body)
	raw, err := base64.StdEncoding.DecodeString(string(b))
	m, merr := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil || merr != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.lose {
		s.lose = false
		s.accepted = append(s.accepted, backendtest.Delivery{Raw: raw})
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
		return
	}
	var rcpts []string
	for _, h := range []string{"To", "Cc", "Bcc"} {
		list, _ := m.Header.AddressList(h)
		for _, a := range list {
			if s.refused[strings.ToLower(a.Address)] {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"code":"ErrorInvalidRecipients","message":"refused"}}`)
				return
			}
			rcpts = append(rcpts, a.Address)
		}
	}
	s.accepted = append(s.accepted, backendtest.Delivery{Rcpts: rcpts, Raw: raw})
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) Sender(*testing.T) backend.Sender {
	return graph.New(s.url, func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Token: "good"}, nil
	})
}

func (s *server) Accepted(*testing.T) []backendtest.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []backendtest.Delivery
	for _, d := range s.accepted {
		if d.Rcpts != nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *server) Refuse(_ *testing.T, rcpt string) {
	s.mu.Lock()
	s.refused[rcpt] = true
	s.mu.Unlock()
}
func (s *server) LoseVerdict(*testing.T) { s.mu.Lock(); s.lose = true; s.mu.Unlock() }
func (s *server) RejectAuth(*testing.T)  { s.mu.Lock(); s.rejected = true; s.mu.Unlock() }

func TestGraphBackendContract(t *testing.T) {
	backendtest.RunSender(t, func(t *testing.T) backendtest.SendServer {
		s := &server{refused: map[string]bool{}}
		srv := httptest.NewServer(s)
		t.Cleanup(srv.Close)
		s.url = srv.URL + "/v1.0/me/sendMail"
		return s
	})
}

// Graph saves the sent copy itself.
func TestGraphSavesSent(t *testing.T) {
	if !graph.New("", nil).SavesSent() {
		t.Error("SavesSent = false, want true")
	}
}

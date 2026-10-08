// Package testllm is a fake model service for tests. Task 3 adds chat endpoints
// to the mux.
package testllm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Server struct {
	URL string // base URL, as a user would enter it

	mu      sync.Mutex
	models  []string
	key     string
	headers []http.Header
	Mux     *http.ServeMux
}

// Start serves the model-list endpoint of kind ("openai" or "anthropic").
func Start(t *testing.T, kind string) *Server {
	t.Helper()
	s := &Server{Mux: http.NewServeMux()}
	path := "/models"
	if kind == "anthropic" {
		path = "/v1/models"
	}
	s.Mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		models, key := s.models, s.key
		s.mu.Unlock()
		got := r.Header.Get("x-api-key")
		if kind == "openai" {
			got = r.Header.Get("Authorization")
			if got != "" {
				got = strings.TrimPrefix(got, "Bearer ")
			}
		}
		if key != "" && got != key {
			// echoes what it got, so tests can prove errors don't repeat it
			http.Error(w, "bad key "+got, http.StatusUnauthorized)
			return
		}
		data := []map[string]string{}
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		s.Mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	s.URL = ts.URL
	return s
}

func (s *Server) SetModels(m ...string) { s.mu.Lock(); s.models = m; s.mu.Unlock() }
func (s *Server) RequireKey(k string)   { s.mu.Lock(); s.key = k; s.mu.Unlock() }

// Headers returns the headers of every request so far.
func (s *Server) Headers() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.headers...)
}

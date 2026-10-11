package core

import (
	"sync"

	"github.com/godwit-email/godwit/core/ai"
)

// secretSet holds the secret values of extensions (plugin secret config, MCP
// env and header values) for value-based redaction (spec 5.5). It only grows:
// a value replaced or removed may still be held by a running instance or work
// in flight, so it stays redacted for the rest of the Service's lifetime. Its
// zero value is ready; it is safe for concurrent use.
type secretSet struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

func (s *secretSet) Add(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]struct{}{}
	}
	for _, v := range values {
		s.m[v] = struct{}{}
	}
}

func (s *secretSet) Values() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.m))
	for v := range s.m {
		out = append(out, v)
	}
	return out
}

// Redact replaces every value in the set (8 characters or longer) in str.
func (s *secretSet) Redact(str string) string { return ai.Redact(str, s.Values()) }

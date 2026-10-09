package core

import (
	"sync"

	"github.com/godwit-email/godwit/core/ai"
)

// secretSet holds the secret values of extensions (plugin secret config, MCP
// env and header values) by owner, for value-based redaction (spec 5.5). Its
// zero value is ready; it is safe for concurrent use.
type secretSet struct {
	mu sync.RWMutex
	m  map[string][]string
}

func (s *secretSet) Add(owner string, values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]string{}
	}
	s.m[owner] = append(s.m[owner], values...)
}

func (s *secretSet) RemoveOwner(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, owner)
}

func (s *secretSet) Values() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, vs := range s.m {
		out = append(out, vs...)
	}
	return out
}

// Redact replaces every value in the set (8 characters or longer) in str.
func (s *secretSet) Redact(str string) string { return ai.Redact(str, s.Values()) }

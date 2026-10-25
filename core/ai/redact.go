package ai

import (
	"strings"
)

// Redact replaces each secret in s with "[redacted]". Secrets under 8 characters are skipped:
// replacing them by value would mangle ordinary text, and real keys are longer.
func Redact(s string, secrets []string) string {
	for _, k := range secrets {
		if len(k) >= 8 {
			s = strings.ReplaceAll(s, k, "[redacted]")
		}
	}
	return s
}

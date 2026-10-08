package mail

import (
	"regexp"
	"slices"
	"strings"
)

var subjectPrefix = regexp.MustCompile(`(?i)^\s*((re|fwd?|回复|答复|转发)\s*(\[\d+\])?\s*[:：]\s*)+`)

// ThreadKeys returns the conversation keys of p: ids is its own Message-ID plus
// In-Reply-To and References (deduplicated), subjectKey the lowercased subject
// without Re/Fwd/回复/转发 prefixes and with whitespace collapsed.
func ThreadKeys(p *Parsed) (ids []string, subjectKey string) {
	for _, id := range append([]string{p.MessageID, p.InReplyTo}, p.References...) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	s := subjectPrefix.ReplaceAllString(p.Subject, "")
	return ids, strings.ToLower(strings.Join(strings.Fields(s), " "))
}

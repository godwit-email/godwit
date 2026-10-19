package mail

import (
	"regexp"
	"slices"
	"strings"
)

// İlt is spelled beside ilt: Go's case-insensitive match does not fold U+0130
// (İ) to ASCII i, so a Turkish İlt: prefix would otherwise stay in the subject
// key and split the thread from the original.
var subjectPrefix = regexp.MustCompile(`(?i)^\s*((re|fwd?|aw|wg|tr|rv|res|enc|antw|dr|ynt|ilt|İlt|回复|答复|转发|回覆|轉寄|轉發|답장|전달)\s*(\[\d+\])?\s*[:：]\s*)+`)

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

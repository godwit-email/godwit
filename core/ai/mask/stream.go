package mask

import (
	"regexp"
	"slices"
	"strings"
)

// A stream restores what it has, held back from Cut on, and at its end what
// is left without a cut placeholder (DropCut): a chunk may end inside one.

// maxHeld caps a tail held back as the start of a placeholder.
const maxHeld = 64

var (
	// braceTail is a tail from { or {{ on that the next chunk may end as a
	// placeholder: a body, its start or none, and one } at most.
	braceTail = regexp.MustCompile(`\{\{?[ \t]*[^{}\s]*[ \t]*\}?$`)
	// wordTail is a last word, which may be one without braces.
	wordTail = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]*$`)
	// cutTail is what DropCut may drop: {{ and a body with no }.
	cutTail = regexp.MustCompile(`\{\{[ \t]*[^{}\s]*$`)
	// wholeBody ends a whole placeholder's body: _ and the hash.
	wholeBody = regexp.MustCompile(`_[A-Za-z2-7]{12}$`)
)

// Cut is where text, a stream so far, may end in a placeholder the next
// chunk finishes; len(text) when it does not.
func (m *Masker) Cut(text string) int {
	if loc := braceTail.FindStringIndex(text); loc != nil && len(text)-loc[0] <= maxHeld && !strings.HasSuffix(text, "}}") {
		return loc[0]
	}
	if loc := wordTail.FindStringIndex(text); loc != nil && kindPrefix(text[loc[0]:], m.own()) {
		return loc[0]
	}
	return len(text)
}

// DropCut is text without the placeholder its end cut short: one in braces
// the model was still writing when the stream ended, which names no value.
// A last word without braces stays: API_KEY or PHONE_NUMBER is as likely
// the reply's own.
func (m *Masker) DropCut(text string) string {
	loc := cutTail.FindStringIndex(text)
	if loc == nil || wholeBody.MatchString(strings.TrimRight(text[loc[0]:], " \t")) { // a whole one is restored as it is
		return text
	}
	return text[:loc[0]]
}

// kindPrefix says w, in any case, starts a placeholder without braces: a
// start of KIND_, or KIND_ and up to its hash, of a built-in kind or own.
func kindPrefix(w string, own []rule) bool {
	u := strings.ToUpper(w)
	for _, r := range slices.Concat([]rule{{kind: nameKind}, {kind: TermKind}}, own, rules) {
		k := r.kind + "_"
		if strings.HasPrefix(k, u) {
			return true
		}
		if rest, ok := strings.CutPrefix(u, k); ok && len(rest) <= hashLen && strings.Trim(rest, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") == "" {
			return true
		}
	}
	return false
}

// own are the user's rules, whose kinds name placeholders too.
func (m *Masker) own() []rule {
	if m == nil {
		return nil
	}
	return m.cfg.Load().own
}

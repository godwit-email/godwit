package mask

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Recognizer is 识别模型 (docs/adr/0028-local-model-for-masking.md): the
// values it finds in a text, each under AstrLink Guard's label for it.
type Recognizer interface {
	Recognize(text string) ([]Recognized, error)
}

// Recognized is a value a Recognizer found: text[Start:End], its Label
// and its Score, from 0 to 1.
type Recognized struct {
	Start, End int
	Label      string
	Score      float64
}

// minScore is the score under which what the model found goes as it is:
// the publisher's default, not the user's to set (ADR 0028).
const minScore = 0.8

// modelKinds are the kinds of the model's labels that masking takes, as
// magpie's: a date is nearly always a mail's own, a link the rules'
// (LINK). An account is a user name or a bank account's number.
var modelKinds = map[string]string{"private_person": nameKind, "phone": "PHONE", "email": "EMAIL", "common_secret": "SECRET", "payment_card": "BANK_CARD",
	"private_address": "ADDRESS", "account": "ACCOUNT"}

// modelSpans are what c's model finds in s, of the labels masking takes,
// scored minScore or more, without the spaces and separators at their
// ends; of the secrets, those modelSecret takes. None overlaps what a
// rule found in s, the user's or a built-in one: a rule's kind and bounds
// are surer (an address's domain is kept), so the rule's go.
func (c *compiled) modelSpans(s string) ([]span, error) {
	if c.model == nil {
		return nil, nil
	}
	found, err := c.model.Recognize(s)
	if err != nil {
		return nil, fmt.Errorf("mask: the recognition model: %w", err)
	}
	ruled := c.ruled(s)
	var out []span
	for _, r := range found {
		kind := modelKinds[r.Label]
		a, b := trim(s, r.Start, r.End)
		if kind == "" || r.Score < minScore || a == b || kind == "SECRET" && !modelSecret(s[a:b]) ||
			slices.ContainsFunc(ruled, func(sp span) bool { return sp.start < b && a < sp.end }) {
			continue
		}
		out = append(out, span{start: a, end: b, kind: kind, model: true})
	}
	return out, nil
}

// modelSecret is a secret the model found that is one, as the rules'
// are: 8 characters or more, not one character over and over, nor 3, 6
// or 8 hex digits, which are a colour or a short hash. On real mail, its
// secrets were mostly lone symbols, short strings and colours.
func modelSecret(v string) bool {
	hex := strings.Trim(v, digits+"abcdefABCDEF") == ""
	return utf8.RuneCountInString(v) >= 8 && strings.Trim(v, string(firstRune(v))) != "" && !(hex && slices.Contains([]int{3, 6, 8}, len(v)))
}

// trim is s[a:b] without the spaces and punctuation at its ends, which
// the model's tokens take in: "Sarah," "(415".
func trim(s string, a, b int) (int, int) {
	v := strings.TrimLeftFunc(s[a:b], separator)
	a = b - len(v)
	return a, a + len(strings.TrimRightFunc(v, separator))
}

func separator(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }

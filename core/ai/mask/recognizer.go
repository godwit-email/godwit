package mask

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Recognizer is 识别模型 (docs/adr/0028-recognition-model-for-masking.md):
// the values it finds in a text, each under AstrLink Guard's label for it.
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

// MinScore is the score under which what a Recognizer found goes as it
// is: AstrLink Guard's publisher's default, fixed (ADR 0028).
const MinScore = 0.8

// recognizedKinds are the kinds of AstrLink Guard's labels that masking
// takes, as magpie's: a date is nearly always a mail's own, a link the
// rules' (LINK). An account is a user name, a bank account's number or
// an IP address.
var recognizedKinds = map[string]string{"private_person": nameKind, "phone": "PHONE", "email": "EMAIL", "common_secret": "SECRET", "payment_card": "BANK_CARD",
	"private_address": "ADDRESS", "account": "ACCOUNT", "ip_address": "ACCOUNT"}

// recognizedSpans are what c's Recognizer finds in s, of the labels
// masking takes, scored MinScore or more, without the spaces and
// separators at their ends; of the secrets, those recognizedSecret takes;
// then where s has what it found before, known(s). None overlaps what a
// rule found in s, the user's or a built-in one: a rule's kind and bounds
// are surer (an address's domain is kept), so the rule's go.
func (c *compiled) recognizedSpans(s string, known func(string) []span) ([]span, error) {
	if c.recognizer == nil {
		return nil, nil
	}
	found, err := c.recognizer.Recognize(s)
	if err != nil {
		return nil, fmt.Errorf("mask: the recognition model: %w", err)
	}
	var out []span
	for _, r := range found {
		kind := recognizedKinds[r.Label]
		a, b := trim(s, r.Start, r.End, kind)
		if kind == "" || r.Score < MinScore || a == b || kind == "SECRET" && !recognizedSecret(s[a:b]) {
			continue
		}
		out = append(out, span{start: a, end: b, kind: kind, recognized: true})
	}
	ruled := c.ruled(s)
	return slices.DeleteFunc(append(out, known(s)...), func(sp span) bool {
		return slices.ContainsFunc(ruled, func(r span) bool { return r.start < sp.end && sp.start < r.end })
	}), nil
}

// recognizedSecret is a secret 识别模型 found that is one, as the rules'
// are: 8 characters or more, not one character over and over, nor 3, 6
// or 8 hex digits, which are a colour or a short hash. On real mail, its
// secrets were mostly lone symbols, short strings and colours.
func recognizedSecret(v string) bool {
	hex := strings.Trim(v, digits+"abcdefABCDEF") == ""
	return utf8.RuneCountInString(v) >= 8 && strings.Trim(v, string(firstRune(v))) != "" && !(hex && slices.Contains([]int{3, 6, 8}, len(v)))
}

// trim is s[a:b] of kind without the spaces and separators at its ends,
// which its tokens take in: "Sarah," "(415". A secret loses only the
// spaces: a symbol at its end is likely the secret's own (Abc12345!).
func trim(s string, a, b int, kind string) (int, int) {
	cut := func(r rune) bool { return unicode.IsSpace(r) || kind != "SECRET" && strings.ContainsRune(separators, r) }
	v := strings.TrimLeftFunc(s[a:b], cut)
	a = b - len(v)
	return a, a + len(strings.TrimRightFunc(v, cut))
}

// separators are the punctuation around a value in text, not of it.
const separators = `"'“”‘’«»「」『』()（）[]【】<>《》,，、;；:：.。!！?？`

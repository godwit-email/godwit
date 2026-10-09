package richtext

import (
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func isLatin(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) || unicode.In(r, unicode.Latin, unicode.Greek, unicode.Cyrillic)
}

func gap(a, b rune) bool { return isCJK(a) && isLatin(b) || isLatin(a) && isCJK(b) }

// Autospace splits s into spans so that the character before each place
// where CJK text meets a Latin letter or digit is a span of its own with a
// quarter em (size/4) of LetterSpacing (CSS text-autospace: normal). No
// characters are added: the text of the spans is s.
func Autospace(s string, size float32) []ui.Span { return autospace(s, -1, size) }

// autospace is Autospace for text followed by the rune next (-1 for none),
// so a gap also opens where a styled run ends.
func autospace(s string, next rune, size float32) []ui.Span {
	var out []ui.Span
	start := 0
	for i, r := range s {
		j := i + utf8.RuneLen(r)
		after := next
		if j < len(s) {
			after, _ = utf8.DecodeRuneInString(s[j:])
		}
		if after >= 0 && gap(r, after) {
			if i > start {
				out = append(out, ui.Span{Text: s[start:i]})
			}
			out = append(out, ui.Span{Text: s[i:j], LetterSpacing: size / 4})
			start = j
		}
	}
	if start < len(s) {
		out = append(out, ui.Span{Text: s[start:]})
	}
	return out
}

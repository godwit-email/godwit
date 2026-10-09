package richtext

import (
	"unicode"

	"github.com/egoist/mygo/ui"
)

// autospace is the gap Autospace puts between CJK and Latin text: U+2005,
// a quarter em.
const autospace = " "

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func isLatin(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) || unicode.In(r, unicode.Latin, unicode.Greek, unicode.Cyrillic)
}

// Autospace splits s into spans with a quarter-em gap span wherever CJK
// text meets a Latin letter or digit (CSS text-autospace: normal). The gap
// is drawn, not part of the model's text.
func Autospace(s string) []ui.Span {
	var out []ui.Span
	start, prev := 0, rune(-1)
	for i, r := range s {
		if prev >= 0 && (isCJK(prev) && isLatin(r) || isLatin(prev) && isCJK(r)) {
			out = append(out, ui.Span{Text: s[start:i]}, ui.Span{Text: autospace})
			start = i
		}
		prev = r
	}
	if start < len(s) {
		out = append(out, ui.Span{Text: s[start:]})
	}
	return out
}

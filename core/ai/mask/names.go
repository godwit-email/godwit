package mask

import (
	"cmp"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// nameKind is 人名's: no rule finds a name in text. The display names of
// the From, To and Cc of mail in what is sent are known values instead,
// masked wherever they are written as they are: in the header, the mail's
// text, the user's question. Off by default: a display name is often a
// brand's (GitHub), not a person's.
const nameKind = "NAME"

// SetNames turns the masking of names on or off. Off, names are neither
// learned nor masked; their placeholders still restore.
func (m *Masker) SetNames(on bool) { m.namesOn.Store(on) }

// headerRe is a From, To or Cc line, as mailHeader writes one.
var headerRe = regexp.MustCompile(`(?im)^(?:from|to|cc)[ \t]*:[ \t]*(.+)$`)

// learnNames makes the display names in s's header lines known values.
func (m *Masker) learnNames(s string) error {
	if !m.namesOn.Load() {
		return nil
	}
	for _, sm := range headerRe.FindAllStringSubmatch(s, -1) {
		as, _ := mail.ParseAddressList(strings.TrimSpace(sm[1]))
		for _, a := range as {
			if n := strings.TrimSpace(a.Name); !shortName(n) && !strings.Contains(n, "@") {
				if _, err := m.placeholder(nameKind, n); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// learnBodyNames is learnNames over every string of the JSON body s, the
// JSON in its strings too.
func (m *Masker) learnBodyNames(s string) (err error) {
	if m.namesOn.Load() {
		deep(s, scope{top: true}, func(s string, _ scope) string {
			err = cmp.Or(err, m.learnNames(s))
			return s
		})
	}
	return err
}

// shortName is a name too short to be told from an ordinary word: under
// two characters in Chinese, Japanese or Korean, under four in others
// (Al, Ann, Bob are words of their own, or inside them).
func shortName(n string) bool {
	c := utf8.RuneCountInString(n)
	if strings.IndexFunc(n, cjk) >= 0 {
		return c < 2
	}
	return c < 4
}

func cjk(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// addName keeps n in m.names, longest first; called with mu held.
func (m *Masker) addName(n string) {
	i, found := slices.BinarySearchFunc(m.names, n, func(a, b string) int {
		return cmp.Or(len(b)-len(a), strings.Compare(a, b))
	})
	if !found {
		m.names = slices.Insert(m.names, i, n)
	}
}

// nameSpans are where s has a known name, when names are on: as written,
// and, where it starts or ends with a letter or digit that isn't Chinese,
// Japanese or Korean, not touching another (Mark Lee, not Mark Leeds).
func (m *Masker) nameSpans(s string) []span {
	if !m.namesOn.Load() {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []span
	for _, n := range m.names {
		for at := 0; ; {
			i := strings.Index(s[at:], n)
			if i < 0 {
				break
			}
			a, b := at+i, at+i+len(n)
			at = a + 1
			if word(n, true) && a > 0 && alnumRune(lastRune(s[:a])) || word(n, false) && b < len(s) && alnumRune(firstRune(s[b:])) {
				continue
			}
			out = append(out, span{a, b, nameKind})
		}
	}
	return out
}

// word says n's first (or last) character is one that a word goes on from.
func word(n string, first bool) bool {
	if first {
		return alnumRune(firstRune(n))
	}
	return alnumRune(lastRune(n))
}

func alnumRune(r rune) bool { return (unicode.IsLetter(r) || unicode.IsDigit(r)) && !cjk(r) }

func firstRune(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }

func lastRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }

package mask

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The categories of values, as the user turns each on or off (设置 › AI ›
// 隐私脱敏). Every kind a built-in rule finds is in one (categories).
const (
	Secrets = "secrets" // 密钥
	IDs     = "ids"     // 证件与金融号码
	Codes   = "codes"   // 验证码与一次性链接
	Phones  = "phones"  // 电话
	Emails  = "emails"  // 邮箱
	Names   = "names"   // 人名
)

// categories are the built-in kinds' categories.
var categories = map[string]string{
	"PRIVATE_KEY": Secrets, "API_KEY": Secrets, "TOKEN": Secrets, "PASSWORD": Secrets, "SECRET": Secrets,
	"ID_CARD": IDs, "BANK_CARD": IDs, "PASSPORT": IDs, "IBAN": IDs,
}

// Category is the category of a built-in kind; "" for the user's own.
func Category(kind string) string { return categories[kind] }

// TermKind is the kind of the user's words (自定义词).
const TermKind = "TERM"

// How many rules and allowed values the user may have.
const (
	MaxRules = 32
	MaxAllow = 200
)

// Rule is one of the user's (自定义正则): Regex, in RE2 syntax, finds values
// of Kind, its first group that matched if it has one, else all it matches.
type Rule struct{ Kind, Regex string }

// Config is what the user set: the categories turned Off; their Words,
// matched in any case unless WordsCase, inside other words unless
// WordsWhole; their Rules; and the values never masked (Allow, in any case).
type Config struct {
	Off                   []string
	Words                 []string
	WordsCase, WordsWhole bool
	Rules                 []Rule
	Allow                 []string
}

// RuleError says why the user's rule at Index can't be used.
type RuleError struct {
	Index int
	Kind  string
	Err   error
}

func (e *RuleError) Error() string {
	return fmt.Sprintf("masking rule %d (%s): %v", e.Index+1, e.Kind, e.Err)
}

func (e *RuleError) Unwrap() error { return e.Err }

// What is wrong with a rule, besides a pattern RE2 can't compile.
var (
	ErrKind       = errors.New("a kind is a letter, then letters, digits or _, at most 24")
	ErrEmptyMatch = errors.New("the pattern matches empty text")
)

// kindRe is a kind as a placeholder names it.
var kindRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,23}$`)

// compiled is a Config as find runs it.
type compiled struct {
	off   map[string]bool
	own   []rule // the user's rules and words, found before the built-in ones
	allow map[string]bool
}

// Check says what is wrong with c, if anything: a rule's pattern that does
// not compile or matches empty text, a kind a placeholder can't name, too
// many rules or allowed values.
func Check(c Config) error {
	_, err := compile(c)
	return err
}

// Configure masks as c says from the next text on; a c Check refuses
// leaves the masker as it was.
func (m *Masker) Configure(c Config) error {
	cc, err := compile(c)
	if err == nil {
		m.cfg.Store(cc)
	}
	return err
}

func compile(c Config) (*compiled, error) {
	if len(c.Rules) > MaxRules {
		return nil, fmt.Errorf("at most %d masking rules", MaxRules)
	}
	if len(c.Allow) > MaxAllow {
		return nil, fmt.Errorf("at most %d values may go unmasked", MaxAllow)
	}
	cc := &compiled{off: map[string]bool{}, allow: map[string]bool{}}
	for _, o := range c.Off {
		cc.off[o] = true
	}
	for _, a := range c.Allow {
		cc.allow[strings.ToLower(strings.TrimSpace(a))] = true
	}
	for i, r := range c.Rules {
		kind := strings.ToUpper(strings.TrimSpace(r.Kind))
		re, err := regexp.Compile(r.Regex)
		switch {
		case !kindRe.MatchString(kind):
			err = ErrKind
		case err == nil && re.MatchString(""):
			err = ErrEmptyMatch
		}
		if err != nil {
			return nil, &RuleError{Index: i, Kind: r.Kind, Err: err}
		}
		cc.own = append(cc.own, rule{kind: kind, re: re})
	}
	var words []string
	for _, w := range c.Words {
		if w = strings.TrimSpace(w); w != "" {
			words = append(words, regexp.QuoteMeta(w))
		}
	}
	if len(words) > 0 {
		// the longest first: of "Falcon" and "Falcon Nine", the whole name
		slices.SortFunc(words, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
		pat := strings.Join(words, "|")
		if !c.WordsCase {
			pat = "(?i)" + pat
		}
		cc.own = append(cc.own, rule{kind: TermKind, re: regexp.MustCompile(pat), word: c.WordsWhole})
	}
	return cc, nil
}

// wordRune is a letter, a digit or _ of a script that puts spaces between
// words: Chinese and Japanese don't, so a word of theirs is whole inside
// their text, as magpie has it.
func wordRune(r rune) bool {
	if r == '_' || unicode.IsDigit(r) {
		return true
	}
	return unicode.IsLetter(r) && !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

// wholeWord says s[a:b] is no part of a longer word.
func wholeWord(s string, a, b int) bool {
	before, _ := utf8.DecodeLastRuneInString(s[:a])
	first, _ := utf8.DecodeRuneInString(s[a:])
	last, _ := utf8.DecodeLastRuneInString(s[:b])
	after, _ := utf8.DecodeRuneInString(s[b:])
	return !(a > 0 && wordRune(first) && wordRune(before)) && !(b < len(s) && wordRune(last) && wordRune(after))
}

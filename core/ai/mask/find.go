package mask

import (
	"regexp"
	"slices"
	"strings"
)

// rule finds values of one kind: the match, or its first group that matched;
// bound are the characters that may not touch it on either side. fit, if
// set, checks it and says how much of it is the value (0: none).
type rule struct {
	kind  string
	re    *regexp.Regexp
	bound string
	fit   func(v string) int
}

const (
	digits  = "0123456789"
	alnum   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" + digits
	tokenCh = alnum + "_-"
	// secretCh are the characters of a password's or a secret's value.
	secretCh = `[A-Za-z0-9_\-./+=~!@#%^&*]`
)

// The rules, after magpie's (internal/redact). A value two rules find goes
// as the earlier rule's kind.
var rules = []rule{
	{kind: "PRIVATE_KEY", re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]+?-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{kind: "API_KEY", bound: tokenCh, re: regexp.MustCompile(`sk-(?:ant-|proj-|or-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}` +
		`|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|glpat-[A-Za-z0-9_-]{20,}` +
		`|AIza[0-9A-Za-z_-]{35}|xox[abposr]-[0-9A-Za-z-]{10,}|(?:sk|rk)_live_[0-9A-Za-z]{20,}` +
		`|hf_[A-Za-z0-9]{30,}|gsk_[A-Za-z0-9]{40,}|xai-[A-Za-z0-9]{40,}|npm_[A-Za-z0-9]{36}|pypi-[A-Za-z0-9_-]{50,}|SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`)},
	{kind: "API_KEY", bound: alnum, re: regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)},
	{kind: "TOKEN", bound: tokenCh, re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	// the password of scheme://user:password@host
	{kind: "PASSWORD", re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s:@/?#"'<>]*:([^\s@/?#"'<>]{3,})@`), fit: whole(notAVariable)},
	// password = …, API_KEY: "…", as configs and .env files have them
	{kind: "SECRET", re: regexp.MustCompile(`(?i)[A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)[A-Za-z0-9_.-]*` +
		`\\?["']?[ \t]*[:=][ \t]*\\?["']?(` + secretCh + `{8,})`), fit: whole(secretValue)},
	// a password after its name in Chinese or Japanese, as people write one in a message: 密码：786kih, パスワード: …
	{kind: "PASSWORD", re: regexp.MustCompile(`(?:密码|密碼|口令|パスワード|暗証番号)[ \t]*(?:[:：=][ \t]*|[为為是][ \t]*[:：]?[ \t]*|は[ \t]*)?(?:\\?["'“「])?(` +
		secretCh + `{3,63}[A-Za-z0-9_\-/+=~!@#%^&*])`), fit: whole(notAVariable)},

	{kind: "ID_CARD", bound: alnum, re: regexp.MustCompile(`[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`), fit: whole(chineseID)},
	{kind: "BANK_CARD", bound: digits, re: regexp.MustCompile(`[3-6]\d{3}(?:[ -]?\d{4}){2,3}(?:[ -]?\d{1,3})?`), fit: whole(luhn)},
	// a passport's number only after the word: a number alone is no passport's
	{kind: "PASSPORT", bound: alnum, fit: whole(passportOK),
		re: regexp.MustCompile(`(?i:\bpassport(?:[ \t]*(?:no\.?|number|num|#))?)["']?[ \t]*(?:[:#=：][ \t]*)?(?:(?i:is)[ \t]+)?["']?([A-Z0-9]{6,9})` +
			`|(?:护照(?:号码|号)?|護照(?:號碼|号码)?|パスポート(?:番号)?)[ \t]*(?:[:：=][ \t]*)?(?:是[ \t]*|は[ \t]*)?["']?([A-Z0-9]{6,9})`)},
	// an IBAN, as one word or printed in groups of four, by its checksum
	{kind: "IBAN", bound: alnum, re: regexp.MustCompile(`[A-Z]{2}\d{2}(?:[A-Z0-9]{11,30}|(?: [A-Z0-9]{4}){2,7}(?: [A-Z0-9]{1,3})?)`), fit: ibanFit},
}

// span is a value found at s[start:end].
type span struct {
	start, end int
	kind       string
}

// find is the values in s the rules find and those known, in order, none
// overlapping: where two start together, a rule's.
func find(s string, known []span) []span {
	var found []span
	for _, r := range rules {
		for _, loc := range r.re.FindAllStringSubmatchIndex(s, -1) {
			a, b := loc[0], loc[1]
			for g := 2; g < len(loc); g += 2 {
				if loc[g] >= 0 {
					a, b = loc[g], loc[g+1]
					break
				}
			}
			if r.fit != nil {
				if b = a + r.fit(s[a:b]); b == a {
					continue
				}
			}
			if a > 0 && strings.IndexByte(r.bound, s[a-1]) >= 0 || b < len(s) && strings.IndexByte(r.bound, s[b]) >= 0 {
				continue
			}
			found = append(found, span{a, b, r.kind})
		}
	}
	found = append(found, known...)
	slices.SortStableFunc(found, func(x, y span) int { return x.start - y.start })
	out := found[:0]
	for _, sp := range found {
		if len(out) == 0 || sp.start >= out[len(out)-1].end {
			out = append(out, sp)
		}
	}
	return out
}

// whole is a fit that takes all of a value ok accepts.
func whole(ok func(string) bool) func(string) int {
	return func(v string) int {
		if ok(v) {
			return len(v)
		}
		return 0
	}
}

// notAVariable turns away what stands in for a value: ${PASS}, <password>, ****.
func notAVariable(v string) bool {
	return !strings.ContainsAny(v[:1], "$%{<[*") && strings.Trim(v, "*xX.") != ""
}

// secretValue is a value that looks like a secret, not a word or code:
// letters and digits both.
func secretValue(v string) bool {
	return notAVariable(v) && strings.ContainsAny(v, digits) && strings.IndexFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }) >= 0
}

// chineseID is whether v's last character is the check digit of its first 17.
func chineseID(v string) bool {
	w := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i := range 17 {
		sum += int(v[i]-'0') * w[i]
	}
	return strings.ToUpper(v[17:]) == string("10X98765432"[sum%11])
}

// luhn is whether v's 15 to 19 digits pass the Luhn check, as a card number's do.
func luhn(v string) bool {
	var ds []int
	for _, r := range v {
		if r >= '0' && r <= '9' {
			ds = append(ds, int(r-'0'))
		}
	}
	if len(ds) < 15 || len(ds) > 19 {
		return false
	}
	sum := 0
	for i := range ds {
		d := ds[len(ds)-1-i]
		if i%2 == 1 {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// passportOK is a number with five digits or more, not one character repeated.
func passportOK(v string) bool {
	return strings.Count(strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return 'd'
		}
		return -1
	}, v), "d") >= 5 && strings.Trim(v, v[:1]) != ""
}

// ibanLen is how long each country's IBAN is.
var ibanLen = map[string]int{
	"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28, "BA": 20, "BE": 16, "BG": 22, "BH": 22, "BI": 27, "BR": 29, "BY": 28,
	"CH": 21, "CR": 22, "CY": 28, "CZ": 24, "DE": 22, "DJ": 27, "DK": 18, "DO": 28, "EE": 20, "EG": 29, "ES": 24, "FI": 18,
	"FK": 18, "FO": 18, "FR": 27, "GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27, "GT": 28, "HN": 28, "HR": 21, "HU": 28,
	"IE": 22, "IL": 23, "IQ": 23, "IS": 26, "IT": 27, "JO": 30, "KW": 30, "KZ": 20, "LB": 28, "LC": 32, "LI": 21, "LT": 20,
	"LU": 20, "LV": 21, "LY": 25, "MC": 27, "MD": 24, "ME": 22, "MK": 19, "MN": 20, "MR": 27, "MT": 31, "MU": 30, "NI": 28,
	"NL": 18, "NO": 15, "OM": 23, "PK": 24, "PL": 28, "PS": 29, "PT": 25, "QA": 29, "RO": 24, "RS": 22, "RU": 33, "SA": 24,
	"SC": 31, "SD": 18, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "SO": 23, "ST": 25, "SV": 28, "TL": 23, "TN": 24, "TR": 26,
	"UA": 29, "VA": 22, "VG": 24, "XK": 20, "YE": 30,
}

// ibanFit is how much of v is an IBAN, its country's length and mod 97 of it
// 1: groups of four after it may be the words that follow (… 7034 THEN).
func ibanFit(v string) int {
	for {
		if ibanOK(v) {
			return len(v)
		}
		i := strings.LastIndexByte(v, ' ')
		if i < 0 {
			return 0
		}
		v = v[:i]
	}
}

func ibanOK(v string) bool {
	v = strings.ReplaceAll(v, " ", "")
	if n, ok := ibanLen[v[:2]]; !ok || n != len(v) {
		return false
	}
	m := 0
	for _, c := range []byte(v[4:] + v[:4]) {
		switch {
		case c >= '0' && c <= '9':
			m = (m*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			m = (m*100 + int(c-'A') + 10) % 97
		default:
			return false
		}
	}
	return m == 1
}

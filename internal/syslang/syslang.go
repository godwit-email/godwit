// Package syslang detects the system UI language as a BCP 47 tag.
package syslang

import (
	"os"
	"regexp"
	"strings"

	"golang.org/x/text/language"
)

// osLang is the platform-specific lookup (empty when unavailable); a var so tests can stub it.
var osLang = osLanguage

// Detect returns the OS language where available, else the first non-empty
// of LC_ALL, LC_MESSAGES, LANG; "en" when nothing valid is found.
func Detect() string {
	if t := Normalize(osLang()); t != "" {
		return t
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			if t := Normalize(v); t != "" {
				return t
			}
			break
		}
	}
	return "en"
}

// Normalize turns a POSIX locale ("zh_CN.UTF-8") into a canonical BCP 47 tag
// ("zh-CN"), or "" for empty, "C", "POSIX" and anything that is not valid.
func Normalize(s string) string {
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(strings.TrimSpace(s), "_", "-")
	if s == "" || s == "C" || s == "POSIX" {
		return ""
	}
	t, err := language.Parse(s)
	if err != nil {
		return ""
	}
	return t.String()
}

var firstItem = regexp.MustCompile(`\(\s*"?([^",\s)]+)`)

// parseAppleLanguages extracts the first array item (quoted or not) of
// `defaults read -g AppleLanguages`.
func parseAppleLanguages(out string) string {
	if m := firstItem.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

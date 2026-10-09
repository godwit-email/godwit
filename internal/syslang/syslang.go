// Package syslang detects the system UI language as a BCP 47 tag.
package syslang

import (
	"os"
	"regexp"
	"strings"
)

// osLang is the platform-specific lookup (empty when unavailable); a var so tests can stub it.
var osLang = osLanguage

// Detect returns the OS language where available, else LC_ALL, LC_MESSAGES,
// LANG; "en" when nothing usable is found.
func Detect() string {
	if s := osLang(); s != "" {
		return Normalize(s)
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return Normalize(v)
		}
	}
	return "en"
}

// Normalize turns a POSIX locale ("zh_CN.UTF-8") into BCP 47 ("zh-CN").
// Empty, "C" and "POSIX" give "en".
func Normalize(s string) string {
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(strings.TrimSpace(s), "_", "-")
	if s == "" || s == "C" || s == "POSIX" {
		return "en"
	}
	return s
}

var firstQuoted = regexp.MustCompile(`"([^"]+)"`)

// parseAppleLanguages extracts the first quoted entry of `defaults read -g AppleLanguages`.
func parseAppleLanguages(out string) string {
	if m := firstQuoted.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

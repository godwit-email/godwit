// Package i18n is the message catalog (zh-CN, en). Keys are dot-separated;
// later tasks append their keys to zh.go and en.go (TestKeysMatch guards parity).
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

var lang atomic.Value // "zh-CN" or "en"

func init() { lang.Store("zh-CN") }

// SetLanguage maps any tag starting with zh to zh-CN, everything else to en.
func SetLanguage(tag string) {
	if strings.HasPrefix(strings.ToLower(tag), "zh") {
		lang.Store("zh-CN")
	} else {
		lang.Store("en")
	}
}

func Language() string { return lang.Load().(string) }

// T returns the message for key, or the key itself when missing.
func T(key string) string {
	m := en
	if Language() == "zh-CN" {
		m = zh
	}
	if s, ok := m[key]; ok {
		return s
	}
	return key
}

func Tf(key string, args ...any) string { return fmt.Sprintf(T(key), args...) }

// Plural formats key+".one" (n == 1) or key+".other" with n.
func Plural(key string, n int) string {
	if n == 1 {
		return Tf(key+".one", n)
	}
	return Tf(key+".other", n)
}

// Keys are every key of the current language's catalog.
func Keys() []string {
	m := en
	if Language() == "zh-CN" {
		m = zh
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

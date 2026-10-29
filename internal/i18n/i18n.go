// Package i18n shares MyGo's message catalogs with the native UI and pages.
package i18n

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	mygoi18n "github.com/egoist/mygo/i18n"
)

var lang, locale atomic.Value
var messageKey = strings.NewReplacer(".", "_", "-", "_")

func init() { lang.Store("zh-Hans"); locale.Store("zh-Hans") }

// SetLanguage selects the catalog language best matching tag (or "en") and
// accepts MyGo's pseudo locales for layout checks.
func SetLanguage(tag string) {
	tag = mygoi18n.Normalize(tag)
	locale.Store(tag)
	switch {
	case mygoi18n.IsPseudo(tag):
	case mygoi18n.Match([]string{tag}, Catalog.Languages()) != "":
		tag = mygoi18n.Match([]string{tag}, Catalog.Languages())
	case strings.HasPrefix(tag, "zh"):
		tag = "zh-Hans"
	default:
		tag = "en"
	}
	lang.Store(tag)
}

func Language() string { return lang.Load().(string) }

// T retains the dotted keys used by dynamic menus and settings indexes.
func T(key string) string { return Tf(key) }

// Tf formats positional callers through the catalog's named ICU arguments.
func Tf(key string, args ...any) string {
	k := messageKey.Replace(key)
	if _, _, ok := Catalog.Message(Language(), k); !ok {
		return key
	}
	var values map[string]any
	if len(args) > 0 {
		values = make(map[string]any, len(args))
		for i, v := range args {
			values["arg"+strconv.Itoa(i+1)] = v
		}
	}
	return Catalog.Format(Language(), k, values)
}

// Plural uses the language's ICU plural rules.
func Plural(key string, n int) string { return Tf(key, n) }

// FormatDate is FormatDateIn in the locale SetLanguage was given, which keeps
// the region the catalog match drops (en-GB is "en" for T, day-first here).
func FormatDate(t, now time.Time) string {
	return FormatDateIn(t, now, locale.Load().(string))
}

// FormatDateIn is t's numeric date in lang (CLDR short date, Md / yMd), in
// now's zone, without the year in now's year: "01.10." / "01.10.2025" (de).
func FormatDateIn(t, now time.Time, lang string) string {
	t = t.In(now.Location())
	l := dateLayout(lang)
	if t.Year() == now.Year() {
		return t.Format(l[0])
	}
	return t.Format(l[1])
}

// FullDate is FullDateIn in the locale SetLanguage was given.
func FullDate(t time.Time) string { return FullDateIn(t, locale.Load().(string)) }

// FullDateIn is t's numeric date with the year in lang, in t's own zone.
func FullDateIn(t time.Time, lang string) string { return t.Format(dateLayout(lang)[1]) }

// dateLayouts are the time layouts of a date without and with the year, by base language.
var dateLayouts = map[string][2]string{
	"de": {"02.01.", "02.01.2006"},
	"fr": {"02/01", "02/01/2006"},
	"es": {"02/01", "02/01/2006"},
	"it": {"02/01", "02/01/2006"},
	"pt": {"02/01", "02/01/2006"},
	"vi": {"02/01", "02/01/2006"},
	"nl": {"02-01", "02-01-2006"},
	"ru": {"02.01", "02.01.2006"},
	"tr": {"02.01", "02.01.2006"},
	"ko": {"1. 2.", "2006. 1. 2."},
	"zh": {"1/02", "2006/01/02"},
	"ja": {"1/02", "2006/01/02"},
	"en": {"1/02", "1/02/2006"},
}

func dateLayout(lang string) [2]string {
	base, region, _ := strings.Cut(strings.ToLower(strings.TrimSpace(lang)), "-")
	if base == "en" && (region == "gb" || region == "ie" || region == "au" || region == "nz") {
		return dateLayouts["fr"]
	}
	if l, ok := dateLayouts[base]; ok {
		return l
	}
	return dateLayouts["en"]
}

// Keys returns the source catalog's message keys.
func Keys() []string {
	data, err := files.ReadFile("en.json")
	if err != nil {
		panic(err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		panic(err)
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		if !strings.HasPrefix(k, "@") {
			keys = append(keys, k)
		}
	}
	return keys
}

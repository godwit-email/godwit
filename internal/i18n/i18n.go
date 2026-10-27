// Package i18n shares MyGo's message catalogs with the native UI and pages.
package i18n

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	mygoi18n "github.com/egoist/mygo/i18n"
)

var lang atomic.Value
var messageKey = strings.NewReplacer(".", "_", "-", "_")

func init() { lang.Store("zh-Hans") }

// SetLanguage selects the catalog language best matching tag (or "en") and
// accepts MyGo's pseudo locales for layout checks.
func SetLanguage(tag string) {
	tag = mygoi18n.Normalize(tag)
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

// FormatDate formats t for list views and cards according to Language():
// in the current year: e.g. "01.10." (de), "10/01" (en/zh), "01/10" (fr/es);
// in a different year: e.g. "01.10.2025" (de), "10/01/2025" (en), "2025/10/01" (zh).
func FormatDate(t, now time.Time) string {
	return FormatDateIn(t, now, Language())
}

// FormatDateIn formats t for list views and cards according to lang/locale,
// adhering to Unicode CLDR standards:
//   - de: DD.MM. / DD.MM.YYYY
//   - fr, es, it, pt, vi: DD/MM / DD/MM/YYYY
//   - en-GB, en-IE, en-AU, en-NZ: DD/MM / DD/MM/YYYY
//   - en-US (and en default): MM/DD / MM/DD/YYYY
//   - zh, ja: MM/DD / YYYY/MM/DD
//   - ko: MM. DD. / YYYY. MM. DD.
//   - nl: DD-MM / DD-MM-YYYY
//   - ru, tr: DD.MM / DD.MM.YYYY
func FormatDateIn(t, now time.Time, lang string) string {
	t = t.In(now.Location())
	tag := strings.ToLower(strings.TrimSpace(lang))
	base, region, _ := strings.Cut(tag, "-")
	sameYear := t.Year() == now.Year()
	switch base {
	case "de":
		if sameYear {
			return fmt.Sprintf("%02d.%02d.", t.Day(), t.Month())
		}
		return fmt.Sprintf("%02d.%02d.%d", t.Day(), t.Month(), t.Year())
	case "fr", "es", "it", "pt", "vi":
		if sameYear {
			return fmt.Sprintf("%02d/%02d", t.Day(), t.Month())
		}
		return fmt.Sprintf("%02d/%02d/%d", t.Day(), t.Month(), t.Year())
	case "nl":
		if sameYear {
			return fmt.Sprintf("%02d-%02d", t.Day(), t.Month())
		}
		return fmt.Sprintf("%02d-%02d-%d", t.Day(), t.Month(), t.Year())
	case "ru", "tr":
		if sameYear {
			return fmt.Sprintf("%02d.%02d", t.Day(), t.Month())
		}
		return fmt.Sprintf("%02d.%02d.%d", t.Day(), t.Month(), t.Year())
	case "ko":
		if sameYear {
			return fmt.Sprintf("%d. %d.", t.Month(), t.Day())
		}
		return fmt.Sprintf("%d. %d. %d.", t.Year(), t.Month(), t.Day())
	case "zh", "ja":
		if sameYear {
			return fmt.Sprintf("%d/%02d", t.Month(), t.Day())
		}
		return fmt.Sprintf("%d/%02d/%02d", t.Year(), t.Month(), t.Day())
	case "en":
		if region == "gb" || region == "ie" || region == "au" || region == "nz" {
			if sameYear {
				return fmt.Sprintf("%02d/%02d", t.Day(), t.Month())
			}
			return fmt.Sprintf("%02d/%02d/%d", t.Day(), t.Month(), t.Year())
		}
		if sameYear {
			return fmt.Sprintf("%d/%02d", t.Month(), t.Day())
		}
		return fmt.Sprintf("%d/%02d/%d", t.Month(), t.Day(), t.Year())
	default:
		if sameYear {
			return fmt.Sprintf("%d/%02d", t.Month(), t.Day())
		}
		return fmt.Sprintf("%d/%02d/%d", t.Month(), t.Day(), t.Year())
	}
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

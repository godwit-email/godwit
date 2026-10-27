package i18n

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPseudoLocale(t *testing.T) {
	t.Cleanup(func() { SetLanguage("zh-CN") })
	SetLanguage("en-XA")
	if Language() != "en-XA" || !strings.HasPrefix(T("common.save"), "[") {
		t.Fatalf("pseudo locale: %q %q", Language(), T("common.save"))
	}
}

func TestReorderedArgumentsAndPercent(t *testing.T) {
	t.Cleanup(func() { SetLanguage("zh-CN") })
	SetLanguage("en")
	if got := Tf("wit.compose.history", "Ada", 3); got != "3 emails with Ada" {
		t.Fatalf("reordered arguments: %q", got)
	}
	if got := Tf("wit.compose.rewrite", 62); got != "Rewritten, about 62% changed" {
		t.Fatalf("percent: %q", got)
	}
}

func TestKeysMatch(t *testing.T) {
	catalogs := map[string]map[string]json.RawMessage{}
	for _, locale := range Catalog.Languages() {
		data, err := files.ReadFile(locale + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		catalogs[locale] = entries
	}
	for _, key := range Keys() {
		for locale, entries := range catalogs {
			var text string
			if err := json.Unmarshal(entries[key], &text); err != nil || text == "" {
				t.Errorf("%s is missing or empty for %q", locale, key)
			}
		}
	}
}

func TestFallbackAndFormat(t *testing.T) {
	defer SetLanguage("zh-CN")
	SetLanguage("zh_CN.UTF-8")
	if Language() != "zh-Hans" || T("common.save") != "保存" {
		t.Fatalf("zh: %q %q", Language(), T("common.save"))
	}
	SetLanguage("sv-SE")
	if Language() != "en" || T("common.save") != "Save" {
		t.Fatalf("en: %q %q", Language(), T("common.save"))
	}
	if T("no.such.key") != "no.such.key" {
		t.Error("missing key must return itself")
	}
	if got := Plural("shell.sync.minutes_ago", 1); got != "1 minute ago" {
		t.Errorf("plural 1: %q", got)
	}
	if got := Plural("shell.sync.minutes_ago", 5); got != "5 minutes ago" {
		t.Errorf("plural 5: %q", got)
	}
	SetLanguage("zh-CN")
	if got := Plural("shell.sync.minutes_ago", 5); got != "5 分钟前" {
		t.Errorf("zh plural: %q", got)
	}
	if got := Tf("common.save"); got != "保存" {
		t.Errorf("Tf: %q", got)
	}
	if got := T("compose.format"); got != "格式" {
		t.Errorf("compose.format: %q", got)
	}
}

func TestChineseLocaleAliases(t *testing.T) {
	t.Cleanup(func() { SetLanguage("zh-Hans") })
	for _, tag := range []string{"zh", "zh-CN", "zh-SG", "zh-Hans", "zh-Hans-SG"} {
		SetLanguage(tag)
		if Language() != "zh-Hans" || T("common.save") != "保存" {
			t.Errorf("%s: %q %q", tag, Language(), T("common.save"))
		}
	}
	for _, tag := range []string{"zh-TW", "zh-HK", "zh-MO", "zh-Hant", "zh-Hant-HK"} {
		SetLanguage(tag)
		if Language() != "zh-Hant" || T("common.save") != "儲存" {
			t.Errorf("%s: %q %q", tag, Language(), T("common.save"))
		}
	}
}

func TestGermanLocaleAliases(t *testing.T) {
	t.Cleanup(func() { SetLanguage("zh-Hans") })
	for _, tag := range []string{"de", "de-DE", "de-AT", "de-CH"} {
		SetLanguage(tag)
		if Language() != "de" {
			t.Errorf("%s: %q", tag, Language())
		}
	}
}

func TestAllSupportedLocales(t *testing.T) {
	t.Cleanup(func() { SetLanguage("zh-Hans") })
	for tag, want := range map[string]string{
		"en-US": "en",
		"zh-CN": "zh-Hans",
		"zh-TW": "zh-Hant",
		"ja-JP": "ja",
		"ko-KR": "ko",
		"fr-FR": "fr",
		"de-DE": "de",
		"es-ES": "es",
		"pt-BR": "pt",
		"it-IT": "it",
		"ru-RU": "ru",
		"nl-NL": "nl",
		"tr-TR": "tr",
		"vi-VN": "vi",
	} {
		SetLanguage(tag)
		if Language() != want {
			t.Errorf("SetLanguage(%q) = %q, want %q", tag, Language(), want)
		}
		if got := Plural("shell.sync.minutes_ago", 1); got == "" || got == "shell.sync.minutes_ago" {
			t.Errorf("%s plural(1): %q", want, got)
		}
		if got := Plural("shell.sync.minutes_ago", 5); got == "" || got == "shell.sync.minutes_ago" {
			t.Errorf("%s plural(5): %q", want, got)
		}
	}
}

func TestThreadExpandPlural(t *testing.T) {
	t.Cleanup(func() { SetLanguage("zh-Hans") })
	SetLanguage("en")
	if got := Plural("reader.thread.expand", 1); got != "Expand email history (1 email)" {
		t.Errorf("one: %q", got)
	}
	if got := Plural("reader.thread.expand", 3); got != "Expand email history (3 emails)" {
		t.Errorf("other: %q", got)
	}
	SetLanguage("ru")
	if got := Plural("reader.thread.expand", 3); got != "Развернуть историю переписки (3 письма)" {
		t.Errorf("ru few: %q", got)
	}
}

func TestFormatDate(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	sameYear := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	diffYear := time.Date(2025, 10, 1, 10, 0, 0, 0, time.UTC)

	// German: DD.MM. / DD.MM.YYYY
	if got := FormatDateIn(sameYear, now, "de"); got != "01.10." {
		t.Errorf("de sameYear: got %q, want %q", got, "01.10.")
	}
	if got := FormatDateIn(diffYear, now, "de"); got != "01.10.2025" {
		t.Errorf("de diffYear: got %q, want %q", got, "01.10.2025")
	}

	// English: M/DD / M/DD/YYYY
	if got := FormatDateIn(sameYear, now, "en"); got != "10/01" {
		t.Errorf("en sameYear: got %q, want %q", got, "10/01")
	}
	if got := FormatDateIn(diffYear, now, "en"); got != "10/01/2025" {
		t.Errorf("en diffYear: got %q, want %q", got, "10/01/2025")
	}

	// French: DD/MM / DD/MM/YYYY
	if got := FormatDateIn(sameYear, now, "fr"); got != "01/10" {
		t.Errorf("fr sameYear: got %q, want %q", got, "01/10")
	}
	if got := FormatDateIn(diffYear, now, "fr"); got != "01/10/2025" {
		t.Errorf("fr diffYear: got %q, want %q", got, "01/10/2025")
	}

	// Chinese: M/DD / YYYY/MM/DD
	if got := FormatDateIn(sameYear, now, "zh-Hans"); got != "10/01" {
		t.Errorf("zh sameYear: got %q, want %q", got, "10/01")
	}
	if got := FormatDateIn(diffYear, now, "zh-Hans"); got != "2025/10/01" {
		t.Errorf("zh diffYear: got %q, want %q", got, "2025/10/01")
	}

	// Active Language()
	t.Cleanup(func() { SetLanguage("zh-Hans") })
	SetLanguage("de")
	if got := FormatDate(sameYear, now); got != "01.10." {
		t.Errorf("Language() de: got %q, want %q", got, "01.10.")
	}
}


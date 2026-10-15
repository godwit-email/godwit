package i18n

import (
	"encoding/json"
	"strings"
	"testing"
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
	SetLanguage("fr-FR")
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
	for _, tag := range []string{"zh", "zh-CN", "zh-SG", "zh-Hans", "zh-Hans-SG", "zh-TW", "zh-Hant"} {
		SetLanguage(tag)
		if Language() != "zh-Hans" || T("common.save") != "保存" {
			t.Errorf("%s: %q %q", tag, Language(), T("common.save"))
		}
	}
}

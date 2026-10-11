package i18n

import "testing"

func TestKeysMatch(t *testing.T) {
	for k, v := range zh {
		if v == "" {
			t.Errorf("zh-CN %q is empty", k)
		}
		if e, ok := en[k]; !ok || e == "" {
			t.Errorf("en is missing or empty for %q", k)
		}
	}
	for k := range en {
		if _, ok := zh[k]; !ok {
			t.Errorf("zh-CN is missing %q", k)
		}
	}
}

func TestFallbackAndFormat(t *testing.T) {
	defer SetLanguage("zh-CN")
	SetLanguage("zh_CN.UTF-8")
	if Language() != "zh-CN" || T("common.save") != "保存" {
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
}

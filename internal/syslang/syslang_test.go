package syslang

import "testing"

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{
		"zh_CN.UTF-8": "zh-CN",
		"en_US":       "en-US",
		"zh-Hans-CN":  "zh-Hans-CN",
		"en_US@euro":  "en-US",
		"C":           "",
		"POSIX":       "",
		"":            "",
		"???":         "",
		"zh_cn":       "zh-CN",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAppleLanguages(t *testing.T) {
	out := "(\n    \"zh-Hans-CN\",\n    \"en-CN\"\n)\n"
	if got := parseAppleLanguages(out); got != "zh-Hans-CN" {
		t.Errorf("got %q", got)
	}
	if got := parseAppleLanguages("(\n    en,\n    \"zh-Hans\"\n)\n"); got != "en" {
		t.Errorf("unquoted first: %q", got)
	}
	if got := parseAppleLanguages("(\n    ja-JP\n)"); got != "ja-JP" {
		t.Errorf("unquoted only: %q", got)
	}
	if parseAppleLanguages("garbage") != "" {
		t.Error("garbage should give empty")
	}
}

func TestDetectEnvOrder(t *testing.T) {
	orig := osLang
	t.Cleanup(func() { osLang = orig })
	osLang = func() string { return "" }
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "zh_CN.UTF-8")
	if got := Detect(); got != "zh-CN" {
		t.Errorf("LANG: %q", got)
	}
	t.Setenv("LC_MESSAGES", "en_US.UTF-8")
	if got := Detect(); got != "en-US" {
		t.Errorf("LC_MESSAGES: %q", got)
	}
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	if got := Detect(); got != "fr-FR" {
		t.Errorf("LC_ALL first: %q", got)
	}
	t.Setenv("LC_ALL", "???")
	if got := Detect(); got != "en" {
		t.Errorf("invalid env: %q", got)
	}
	osLang = func() string { return "ja-JP" }
	if got := Detect(); got != "ja-JP" {
		t.Errorf("os first: %q", got)
	}
}

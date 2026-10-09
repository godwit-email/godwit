package syslang

import "testing"

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{
		"zh_CN.UTF-8": "zh-CN",
		"en_US":       "en-US",
		"zh-Hans-CN":  "zh-Hans-CN",
		"en_US@euro":  "en-US",
		"C":           "en",
		"POSIX":       "en",
		"":            "en",
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
	if parseAppleLanguages("garbage") != "" {
		t.Error("garbage should give empty")
	}
}

func TestDetectEnvOrder(t *testing.T) {
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
	osLang = func() string { return "ja-JP" }
	if got := Detect(); got != "ja-JP" {
		t.Errorf("os first: %q", got)
	}
}

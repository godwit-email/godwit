package core

import "testing"

func TestReadmeBase(t *testing.T) {
	for home, want := range map[string]string{
		"https://github.com/o/r":                        "https://raw.githubusercontent.com/o/r/HEAD",
		"https://github.com/o/r/":                       "https://raw.githubusercontent.com/o/r/HEAD",
		"https://github.com/o/r/tree/main/packages/zen": "https://raw.githubusercontent.com/o/r/main/packages/zen",
		"https://github.com/o/r/blob/main/README.md":    "",
		"https://github.com/o":                          "",
		"http://github.com/o/r":                         "",
		"https://example.com/o/r":                       "",
		"https://github.com.evil.example/o/r":           "",
		"https://github.com/o/r/tree/main/../../x/y":    "",
		"": "",
	} {
		if got := readmeBase(home); got != want {
			t.Errorf("readmeBase(%q) = %q, want %q", home, got, want)
		}
	}
}

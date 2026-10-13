package avatar

import (
	"strings"
	"testing"
)

func TestGravatarURL(t *testing.T) {
	const want = "https://gravatar.com/avatar/973dfe463ec85785f5f95af5ba3906eedb2d931c24e69824a89ea65dba4e813b?s=64&d=404&r=g"
	for _, email := range []string{"test@example.com", " TEST@Example.COM \t"} {
		if got := GravatarURL(email); got != want {
			t.Errorf("GravatarURL(%q) = %q, want %q", email, got, want)
		}
	}
	base := GravatarURL("a.b@example.com")
	if base == GravatarURL("ab@example.com") || base == GravatarURL("a.b+tag@example.com") {
		t.Error("dots or tag collapsed")
	}
	if strings.Contains(base, "example.com") || strings.Contains(base, "profile") || strings.Contains(base, "a.b") {
		t.Error("URL exposes original email or requests profile")
	}
}

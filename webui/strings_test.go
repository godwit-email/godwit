package webui

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposeStringsExist(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh") })
	for _, lang := range []string{"zh", "en"} {
		i18n.SetLanguage(lang)
		for _, k := range append(composeKeys, signatureKeys...) {
			assert.NotEqual(t, k, i18n.T(k), "%s has no %s text", k, lang)
		}
	}
	assert.NotContains(t, composeKeys, "compose.sent")
}

// webKeys are the i18n keys quoted in the non-test sources under web/src/<dir>.
func webKeys(t *testing.T, dirs ...string) map[string]bool {
	t.Helper()
	lit := regexp.MustCompile(`"([a-z_]+(?:\.[a-z0-9_]+)+)"`)
	out := map[string]bool{}
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join("..", "web", "src", d), func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || strings.Contains(p, ".test.") ||
				(!strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx")) {
				return err
			}
			b, err := os.ReadFile(p)
			for _, m := range lit.FindAllStringSubmatch(string(b), -1) {
				if i18n.T(m[1]) != m[1] {
					out[m[1]] = true
				}
			}
			return err
		})
		require.NoError(t, err)
	}
	return out
}

// TestWebKeysSent: every key the web pages quote reaches them, else they show the raw key.
// The shared wit/ code has copy for one layout only: ask.* (the Wit page's citations) and the
// panel's compose copy, which the other page never shows.
func TestWebKeysSent(t *testing.T) {
	panelOnly := func(k string) bool {
		return strings.HasPrefix(k, "compose.") || strings.HasPrefix(k, "wit.compose.") ||
			k == "wit.tools.called" || k == "wit.thinking"
	}
	for k := range webKeys(t, "compose", "wit") {
		assert.True(t, strings.HasPrefix(k, "ask.") || slices.Contains(composeKeys, k), "composeKeys lacks %s", k)
	}
	for k := range webKeys(t, "wit-page", "wit") {
		assert.True(t, panelOnly(k) || slices.Contains(witKeys, k), "witKeys lacks %s", k)
	}
}

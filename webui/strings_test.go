package webui

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
				if strings.HasPrefix(m[1], "compose.") || strings.HasPrefix(m[1], "wit.") || strings.HasPrefix(m[1], "ask.") || strings.HasPrefix(m[1], "common.") || strings.HasPrefix(m[1], "error.") || strings.HasPrefix(m[1], "reader.") || strings.HasPrefix(m[1], "settings.") || strings.HasPrefix(m[1], "mail.") || strings.HasPrefix(m[1], "close.") || strings.HasPrefix(m[1], "link.") || strings.HasPrefix(m[1], "shell.") {
					out[m[1]] = true
				}
			}
			return err
		})
		require.NoError(t, err)
	}
	return out
}

// Every literal page key must be in the catalogs bundled with the frontend.
func TestWebCatalogMessages(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	keys := webKeys(t, "compose", "wit", "wit-page", "reader", "signature")
	for _, locale := range []string{"zh-CN", "en"} {
		i18n.SetLanguage(locale)
		for key := range keys {
			assert.NotEqual(t, key, i18n.T(key), "%s has no %s", locale, key)
		}
	}
}

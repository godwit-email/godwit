package webui

import (
	"testing"

	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
)

func TestComposeStringsExist(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh") })
	for _, lang := range []string{"zh", "en"} {
		i18n.SetLanguage(lang)
		for _, k := range composeKeys {
			assert.NotEqual(t, k, i18n.T(k), "%s has no %s text", k, lang)
		}
	}
	assert.NotContains(t, composeKeys, "compose.sent")
}

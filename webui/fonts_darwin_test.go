//go:build darwin

package webui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemFonts(t *testing.T) {
	fonts := systemFonts()
	assert.NotEmpty(t, fonts)

	// Must include core web-safe fonts
	assert.Contains(t, fonts, "Arial")
	assert.Contains(t, fonts, "Times New Roman")
	assert.Contains(t, fonts, "Georgia")
	assert.Contains(t, fonts, "Verdana")

	// Must NOT contain symbols, emojis or braille fonts
	for _, f := range fonts {
		lower := strings.ToLower(f)
		assert.False(t, strings.Contains(lower, "symbol"), "unexpected font: %s", f)
		assert.False(t, strings.Contains(lower, "emoji"), "unexpected font: %s", f)
		assert.False(t, strings.Contains(lower, "ding"), "unexpected font: %s", f)
		assert.False(t, strings.Contains(lower, "braille"), "unexpected font: %s", f)
		assert.False(t, strings.Contains(lower, "math"), "unexpected font: %s", f)
		assert.False(t, strings.HasPrefix(f, "."), "unexpected private font: %s", f)
	}
}

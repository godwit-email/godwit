package webui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBodyRoundTrip(t *testing.T) {
	for _, s := range []string{"一行", "a\n\nb", "\n首行空\n", "  缩进两格\n\t制表", "<b>x</b> & y", "末尾空行\n\n"} {
		assert.Equal(t, s, bodyText(bodyHTML(s)), "%q", s)
	}
}

func TestBodyTextOldMarkup(t *testing.T) {
	assert.Equal(t, "a b\n- x\n- y", bodyText("<div>a <b>b</b></div><ul><li>x</li><li>y</li></ul>"))
}

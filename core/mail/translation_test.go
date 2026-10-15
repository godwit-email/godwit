package mail

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMapHTMLTextKeepsMarkup(t *testing.T) {
	src := `<style>p{color:red}</style><table style="width:600px"><tr><td><img src="cid:logo"><a href="https://example.com">Hello <strong>world</strong></a></td></tr></table><code>go test <span>./...</span></code><pre>export TOKEN=example
<span>go run .</span></pre>`
	var seen []string
	got := MapHTMLText(src, func(s string) string {
		seen = append(seen, s)
		return map[string]string{"Hello ": "你好 ", "world": `<img src="https://evil.test">世界`}[s]
	})
	assert.Equal(t, []string{"Hello ", "world"}, seen)
	assert.Contains(t, got, `<style>p{color:red}</style>`)
	assert.Contains(t, got, `<table style="width:600px">`)
	assert.Contains(t, got, `<img src="cid:logo"/>`)
	assert.Contains(t, got, `<a href="https://example.com">你好 <strong>&lt;img`)
	assert.Contains(t, got, `<code>go test <span>./...</span></code>`)
	assert.Contains(t, got, "<pre>export TOKEN=example\n<span>go run .</span></pre>")
	assert.Equal(t, 1, strings.Count(got, "<img"), "translations cannot add markup")
}

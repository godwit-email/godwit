package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
)

func TestLinksNotNavigable(t *testing.T) {
	safe, _ := mail.Sanitize(`<a href="https://x">x</a><a href="javascript:1">j</a><a href="mailto:a@b.c">m</a>`, nil, false)
	assert.Contains(t, safe, `data-href="https://x"`)
	assert.Contains(t, safe, `data-href="mailto:a@b.c"`)
	assert.NotContains(t, safe, " href=")
	assert.NotContains(t, safe, "javascript")
	assert.NotContains(t, safe, "<html")
	assert.NotContains(t, safe, "<body")
}

func TestSanitizeHostile(t *testing.T) {
	safe, blocked := mail.Sanitize(mustParse(t, "hostile.eml").HTML, nil, false)
	for _, bad := range []string{"<script", "onerror", "javascript:", "<form", "<iframe", "<base", "http-equiv", "url(", "srcset", "t.example"} {
		assert.NotContains(t, safe, bad)
	}
	assert.Equal(t, 4, blocked)
	safe, _ = mail.Sanitize(mustParse(t, "hostile.eml").HTML, nil, true)
	assert.Contains(t, safe, "https://t.example/p.gif")
	assert.Contains(t, safe, "https://t.example/c")
	assert.NotContains(t, safe, "url(")
	assert.NotContains(t, safe, "srcset")
}

func inlineOf(t *testing.T, name string) map[string]mail.Attachment {
	m := map[string]mail.Attachment{}
	for _, a := range mustParse(t, name).Attachments {
		m[a.ContentID] = a
	}
	return m
}

func TestSanitizeInlineCID(t *testing.T) {
	p := mustParse(t, "inline-image.eml")
	safe, blocked := mail.Sanitize(p.HTML, inlineOf(t, "inline-image.eml"), false)
	assert.Contains(t, safe, "data:image/png;base64,")
	assert.NotContains(t, safe, "cid:")
	assert.Zero(t, blocked)

	p = mustParse(t, "cid-not-image.eml")
	safe, _ = mail.Sanitize(p.HTML, inlineOf(t, "cid-not-image.eml"), false)
	assert.NotContains(t, safe, "src")
	assert.NotContains(t, safe, "cid:")

	fake := map[string]mail.Attachment{"x": {ContentType: "image/png", Data: []byte("<html><script>alert(1)</script></html>")}}
	safe, _ = mail.Sanitize(`<img src="cid:x">`, fake, false)
	assert.NotContains(t, safe, "src")
}

func TestSanitizeStyles(t *testing.T) {
	safe, _ := mail.Sanitize(`<p style="color:red;margin-top:4px;position:fixed;background-color:url(x)">a</p>`, nil, false)
	assert.Contains(t, safe, "color: red")
	assert.Contains(t, safe, "margin-top: 4px")
	assert.NotContains(t, safe, "position")
	assert.NotContains(t, safe, "url(")
}

func TestSanitizeComposeStripsRemote(t *testing.T) {
	in := `<p><b>b</b><i>i</i><a href="https://x">a</a><img src="https://t.example/p"><img src="//t.example/q"></p><ul><li>l</li></ul><ol><li>o</li></ol><blockquote>q</blockquote><br><script>x</script>`
	safe := mail.SanitizeCompose(in)
	for _, keep := range []string{"<b>b</b>", "<i>i</i>", `href="https://x"`, "<ul>", "<ol>", "<li>", "<blockquote>", "<p>", "<br"} {
		assert.Contains(t, safe, keep)
	}
	assert.NotContains(t, safe, "t.example")
	assert.NotContains(t, safe, "<script")
}

func TestSanitizeImageSrcForms(t *testing.T) {
	bad := []string{"/tracking", "tracking.gif", "../x.gif", `\\host/x`, `/\host/x`, `\/host/x`, "data:image/png;base64,AAAA", "ftp://h/x", "https:///x", "https://h/\tx"}
	for _, src := range bad {
		in := `<img src="` + src + `">`
		for _, load := range []bool{false, true} {
			safe, _ := mail.Sanitize(in, nil, load)
			assert.NotContains(t, safe, "src", "%q load=%v -> %s", src, load, safe)
		}
		assert.NotContains(t, mail.SanitizeCompose(in), "src", "%q compose", src)
	}
	safe, _ := mail.Sanitize(`<img src="https://h.example/x.gif">`, nil, true)
	assert.Contains(t, safe, `src="https://h.example/x.gif"`)
}

func TestImageMapAreaLinksNotNavigable(t *testing.T) {
	safe, _ := mail.Sanitize(`<img src="https://x/i.png" usemap="#m"><map name="m"><area shape="rect" coords="0,0,9,9" href="https://x" alt="a"><area shape="rect" coords="0,0,9,9" href="javascript:alert(1)" alt="b"></map>`, nil, true)
	assert.Contains(t, safe, `<area`)
	assert.Contains(t, safe, `data-href="https://x"`)
	assert.NotContains(t, safe, ` href=`)
	assert.NotContains(t, safe, `javascript:`)
}

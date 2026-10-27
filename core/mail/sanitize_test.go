package mail_test

import (
	"strings"
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

func TestSanitizeDraftKeepsKnownSignatureImages(t *testing.T) {
	known := map[string]bool{"sig-aa@godwit": true}
	in := `<p>x</p><img src="cid:sig-aa@godwit"><img src="cid:sig-bb@godwit"><img src="cid:other"><img src="data:image/png;base64,AAAA"><img src="https://t.example/p">`
	safe := mail.SanitizeDraft(in, known)
	assert.Contains(t, safe, `src="cid:sig-aa@godwit"`)
	assert.Equal(t, 1, strings.Count(safe, "src="), safe)
	assert.Contains(t, mail.SanitizeDraft(safe, known), `src="cid:sig-aa@godwit"`, "kept on every save")
	assert.NotContains(t, mail.SanitizeCompose(in), "src=", "no set: every cid dropped")
}

func TestImageSources(t *testing.T) {
	in := `<p>a</p><img src="cid:sig-aa@godwit"><img src="cid:sig-bb@godwit">`
	assert.Equal(t, []string{"sig-aa@godwit", "sig-bb@godwit"}, mail.CIDs(in))
	out := mail.ReplaceImages(in, func(src string) string {
		if src == "cid:sig-aa@godwit" {
			return "data:image/png;base64,AA"
		}
		return ""
	})
	assert.Contains(t, out, `src="data:image/png;base64,AA"`)
	assert.Equal(t, 1, strings.Count(out, "src="), out)
	assert.Contains(t, out, "<p>a</p>")
}

func TestSanitizeBackgroundImage(t *testing.T) {
	src := `<table style="color:red; background: #fff url('https://cdn.example/bg.png') no-repeat"><tr><td style="background-image:url(//cdn.example/a.jpg)">x</td></tr></table>` +
		`<div style="background:url(data:image/png;base64,AAAA)" data-bg="https://evil.example/x.png">y</div>` +
		`<p style="background-image:url(javascript:1)">z</p>`
	safe, blocked := mail.Sanitize(src, nil, true)
	assert.Contains(t, safe, `data-bg="https://cdn.example/bg.png"`)
	assert.Contains(t, safe, `data-bg="https://cdn.example/a.jpg"`)
	assert.Contains(t, safe, "color: red")
	assert.NotContains(t, safe, "url(")
	assert.NotContains(t, safe, "evil.example") // a data-bg of the mail's own is not trusted
	assert.NotContains(t, safe, "javascript")
	assert.Equal(t, 4, blocked)

	safe, _ = mail.Sanitize(src, nil, false)
	assert.NotContains(t, safe, "data-bg")
	assert.NotContains(t, mail.SanitizeCompose(src), "data-bg")
}

func TestSanitizeDropsTrackingPixels(t *testing.T) {
	src := `<p>hi</p><img src="https://t.example/a.gif" width="1" height="1">` +
		`<img src="https://t.example/b.gif" style="width:1px;height:1px">` +
		`<img src="https://t.example/c.gif" width="0">` +
		`<img src="https://www.tiktok.com/edm/catcher?x=1" width="300">` +
		`<img src="https://t.example/logo.png" width="120" height="40">` +
		`<img src="https://t.example/rule.gif" width="600" height="1">`
	safe, blocked := mail.Sanitize(src, nil, true)
	assert.Equal(t, 2, blocked)
	assert.Contains(t, safe, "logo.png")
	assert.Contains(t, safe, "rule.gif")
	for _, gone := range []string{"a.gif", "b.gif", "c.gif", "catcher"} {
		assert.NotContains(t, safe, gone)
	}
}

// A link bluemonday could not parse (a space, a {placeholder}) still opens;
// one the opener's url.Parse refuses (a bad port) is no link; and a data-href
// in the mail itself is not taken for ours.
func TestSanitizeKeepsUnparseableLinks(t *testing.T) {
	safe, _ := mail.Sanitize(`<a href="https://x.com/a b|c{d}">1</a><a href="https://x.com:abc/">2</a><span data-href="https://evil">3</span><a data-href="https://evil" href="javascript:1">4</a>`, nil, false)
	assert.Contains(t, safe, `data-href="https://x.com/a b|c{d}"`)
	assert.NotContains(t, safe, "x.com:abc", "the opener could not open it")
	assert.NotContains(t, safe, "evil")
}

// A background image keeps its size, repeat and position: without
// background-size: cover a large cover shows only its top-left corner.
func TestSanitizeKeepsBackgroundSizing(t *testing.T) {
	safe, _ := mail.Sanitize(`<table><tr><td style="background-image:url(https://c.example/a.jpg);background-size:cover;background-repeat:no-repeat;background-position:center;color:red">x</td></tr></table>`, nil, true)
	for _, want := range []string{"background-size: cover", "background-repeat: no-repeat", "background-position: center"} {
		assert.Contains(t, safe, want)
	}
}

// The layout a card needs survives: its max-width, the page's own background
// on <body>, a button's fixed width and round corners.
func TestSanitizeKeepsLayout(t *testing.T) {
	safe, _ := mail.Sanitize(`<html><body style="background:#f2f2f2" bgcolor="#f2f2f2"><div style="max-width:600px;margin:0 auto;border-radius:16px;border-top-left-radius:4px;background:#fff"><a href="https://x.com" style="table-layout:fixed;min-width:140px;position:fixed;top:0;opacity:0.5">go</a></div></body></html>`, nil, false)
	for _, want := range []string{"max-width: 600px", "min-width: 140px", "table-layout: fixed", "border-top-left-radius: 4px", "background: #f2f2f2", `bgcolor="#f2f2f2"`} {
		assert.Contains(t, safe, want)
	}
	assert.NotContains(t, safe, "position", "an overlay must not survive")
}

// A table's spacing attributes survive, as numbers only: without cellspacing="0"
// a card of bgcolor cells shows the default 2px gaps between them.
func TestSanitizeKeepsTableSpacing(t *testing.T) {
	safe, _ := mail.Sanitize(`<table border="0" cellpadding="0" cellspacing="0"><tr><td bgcolor="#f5f5f5">x</td></tr></table>`+
		`<table cellspacing="0;x" cellpadding="javascript:1" border="1px"><tr><td>y</td></tr></table>`+
		`<div cellspacing="0">z</div>`, nil, false)
	assert.Contains(t, safe, `<table border="0" cellpadding="0" cellspacing="0">`)
	assert.Contains(t, safe, "<table><tbody><tr><td>y</td></tr></tbody></table>", "only numbers")
	assert.Contains(t, safe, "<div>z</div>", "only on tables")
}

const styled = `<html><head><style>
@import url(https://evil.example/x.css);
@font-face { font-family: x; src: url(https://evil.example/f.woff) }
body { background: #e6eaf0; margin: 0 }
html body .card, .wide > td { width: 720px; position: fixed; top: 0; color: red !important }
.bg { background-image: url(https://evil.example/b.png); height: 10px }
@media only screen and (max-width: 950px) { .wd360 { width: 360px !important } }
@media (prefers-color-scheme: dark) { .card { background: #202124; color: #e8eaed } }
@media screen { @media print { .x { color: blue } } }
body.dark .y { color: green }
.z:hover { color: expression(alert(1)) }
</style></head><body><div class="card wd360" id="godwit-body">x</div></body></html>`

// A mail's <style> is kept as scoped, whitelisted rules in front of its body.
func TestSanitizeStyleBlock(t *testing.T) {
	safe, _ := mail.Sanitize(styled, nil, false)
	css, rest, _ := strings.Cut(strings.TrimPrefix(safe, `<style data-dark="">`), "</style>")
	assert.True(t, strings.HasPrefix(safe, `<style data-dark="">`), "it has styles of its own for the dark scheme")
	assert.Contains(t, rest, `class="card wd360"`)
	assert.NotContains(t, rest, `id=`, "the reader's own id is not the mail's")
	for _, want := range []string{
		"#godwit-body{background:#e6eaf0;margin:0;}",
		"#godwit-body .card,#godwit-body .wide>td{width:720px;color:red !important;}",
		"@media only screen and (max-width:950px){#godwit-body .wd360{width:360px !important;}",
		"@media (min-width:0px){#godwit-body.godwit-dark .card{background:#202124;color:#e8eaed;}",
	} {
		assert.Contains(t, css, want)
	}
	for _, gone := range []string{"evil", "@import", "@font-face", "position", "url(", "expression", "print", "green", "blue"} {
		assert.NotContains(t, css, gone)
	}
	assert.NotContains(t, rest, "<style", "no second style block")
}

// No style leaves text for the plain-text form or the quote.
func TestStyleBlockIsNotText(t *testing.T) {
	safe, _ := mail.Sanitize(`<style>.a{color:red}</style><p class="a">hi</p>`, nil, false)
	assert.Equal(t, "hi", strings.TrimSpace(mail.HTMLToText(safe)))
}

func css(t *testing.T, style string) string {
	t.Helper()
	safe, _ := mail.Sanitize(`<style>`+style+`</style><p class="a">x</p>`, nil, false)
	if !strings.HasPrefix(safe, "<style") {
		return ""
	}
	out, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(safe, "<style>"), "<style data-dark>"), "</style>")
	return out
}

// Selectors and queries are read as CSS tokens: what is not plain goes, a
// scheme query scopes its rules, and nothing can leave the <style>.
func TestSanitizeStyleTokens(t *testing.T) {
	assert.Equal(t, "#godwit-body .a:hover,#godwit-body .b>i{color:red;}\n", css(t, `.a:hover, .b > i { color: red }`))
	assert.Equal(t, "#godwit-body{color:red;}\n#godwit-body >table{color:red;}\n", css(t, `html { color: red } body > table { color: red }`))
	assert.Equal(t, "#godwit-body{color:red;}\n", css(t, `html body { color: red }`))
	assert.Equal(t, "#godwit-body .a{color:red;}\n", css(t, `body.dark, body:hover, .a { color: red }`), "body.dark cannot match")
	assert.Equal(t, "#godwit-body a[href^=\"http\"]{color:red;}\n", css(t, `a[href^="http"] { color: red }`))
	assert.Equal(t, "#godwit-body li:nth-child(2n+1){color:red;}\n", css(t, `li:nth-child(2n+1) { color: red }`))
	// scheme queries scope under the class the reader sets from the theme
	assert.Equal(t, "@media (min-width:0px) and (max-width:600px){#godwit-body.godwit-light .a{color:red;}\n}\n",
		css(t, `@media (prefers-color-scheme: light) and (max-width: 600px) { .a { color: red } }`))
	assert.Equal(t, "", css(t, `@media (prefers-color-scheme: dark), print { .a { color: red } }`), "a scheme for some of the queries only")
	assert.Equal(t, "", css(t, `@media url(x) { .a { color: red } }`))
	assert.Equal(t, "", css(t, `@media (prefers-color-scheme: dark) { @media screen { .a { color: red } } }`), "no nesting")
	// nothing unsafe: functions, urls, text that could end the <style>
	for _, style := range []string{
		`.a { color: expression(alert(1)) }`,
		`.a { width: calc(1px + url(x)) }`,
		`.a { font-family: "</style><script>alert(1)</script>" }`,
		`.a[x="</style>"] { color: red }`,
		`.a { background: url(https://evil.example/x) }`,
		`.a { position: fixed }`,
		`.a:not(url(x)) { color: red }`,
		`@media (max-width: 1px) { .a { behavior: x } }`,
	} {
		assert.Equal(t, "", css(t, style), style)
		safe, _ := mail.Sanitize(`<style>`+style+`</style>`, nil, false)
		assert.NotContains(t, strings.ToLower(safe), "<script", style)
	}
	assert.Equal(t, "#godwit-body .a{color:rgb(1,2,3);width:calc(100% - 4px);}\n", css(t, `.a { color: rgb(1, 2, 3); width: calc(100% - 4px) }`))
}

// A ; where a selector is expected (douceur looped on it forever) is read as
// a browser does: part of the next rule's selector, which it makes invalid.
func TestSanitizeStyleStraySemicolon(t *testing.T) {
	assert.Equal(t, "#godwit-body .a{color:red;}\n#godwit-body .c{color:red;}\n", css(t, `.a{color:red}; .b{color:red} .c{color:red}`))
	assert.Equal(t, "@media (max-width:1px){#godwit-body .a{color:red;}\n#godwit-body .c{color:red;}\n}\n",
		css(t, `@media (max-width: 1px) { .a{color:red}; .b{color:red} .c{color:red} }`))
	assert.Equal(t, "#godwit-body .a{color:red;}\n", css(t, `@import "x"; .a{color:red;}`))
}

// A class or id the mail's styles select by stays, only when plain, and the
// reader's own container id never comes from a mail.
func TestSanitizeClassAndID(t *testing.T) {
	safe, _ := mail.Sanitize(`<p class="a b-c d_e f<g" id="x1">1</p><p id="godwit-body">2</p><p id="a b">3</p>`, nil, false)
	assert.Contains(t, safe, `class="a b-c d_e"`)
	assert.Contains(t, safe, `id="x1"`)
	assert.NotContains(t, safe, "godwit-body")
	assert.NotContains(t, safe, `id="a b"`)
}

// Shadows and gradients are plain tokens; a url() in the same property is not.
func TestSanitizeShadowAndGradient(t *testing.T) {
	assert.Equal(t, "#godwit-body .a{box-shadow:0 4px 22px rgba(46,92,255,0.10),inset 1.2px 1.6px 1.6px rgba(255,255,255,0.66);}\n",
		css(t, `.a { box-shadow: 0 4px 22px rgba(46,92,255,0.10),inset 1.2px 1.6px 1.6px rgba(255,255,255,0.66) }`))
	assert.Equal(t, "#godwit-body .a{background-image:linear-gradient(160deg,rgba(255,255,255,0.55) 0%,#eee 100%) !important;}\n",
		css(t, `.a { background-image: linear-gradient(160deg, rgba(255,255,255,0.55) 0%, #eee 100%) !important }`))
	assert.Equal(t, "", css(t, `.a { background-image: url(https://evil.example/x.png) }`))
	assert.Equal(t, "", css(t, `.a { background-image: linear-gradient(red, url(https://evil.example/x.png)) }`))
	safe, _ := mail.Sanitize(`<p style="box-shadow:0 3px 20px rgba(0,0,0,0.12)">x</p>`, nil, false)
	assert.Contains(t, safe, "box-shadow: 0 3px 20px rgba(0,0,0,0.12)")
}

// A background shorthand's address moves to data-bg, and what it says of
// position, size and repeat stays; without them a large image shows as its corner.
func TestSanitizeBackgroundShorthand(t *testing.T) {
	safe, _ := mail.Sanitize(`<div style="width:140px; background: url('https://c.example/h.png') center / contain no-repeat; border-radius:20px">x</div>`, nil, true)
	assert.Contains(t, safe, `data-bg="https://c.example/h.png"`)
	assert.Contains(t, safe, "background: center / contain no-repeat")
	assert.Contains(t, safe, "width: 140px")
	assert.NotContains(t, safe, "url(")
	// layers (a gif and its poster): the first with an address is shown, the others go with their url()
	safe, _ = mail.Sanitize(`<div style="width:146px;background:url('https://c.example/a.gif') center / contain no-repeat, url('https://c.example/p.png') center / contain no-repeat;border-radius:20px">x</div>`, nil, true)
	assert.Contains(t, safe, `data-bg="https://c.example/a.gif"`)
	assert.Contains(t, safe, "background: center / contain no-repeat;")
	assert.Contains(t, safe, "width: 146px")
	assert.NotContains(t, safe, "p.png")
	assert.NotContains(t, safe, "url(")
	safe, _ = mail.Sanitize(`<div style="background-image:url(//c.example/h.png);color:red">x</div>`, nil, true)
	assert.Contains(t, safe, `data-bg="https://c.example/h.png"`)
	assert.NotContains(t, safe, "background-image")
}

// A <style> with a media attribute holds only there: its rules keep the
// condition (a scheme's as the reader's class), or go when it cannot be kept.
func TestSanitizeStyleMedia(t *testing.T) {
	safe, _ := mail.Sanitize(`<style media="(prefers-color-scheme: dark)">.a { color: #eee }</style>`+
		`<style media="screen and (max-width: 600px)">.b { width: 100% }</style>`+
		`<style media="url(x)">.c { color: red }</style><p class="a b c">x</p>`, nil, false)
	css, _, _ := strings.Cut(strings.TrimPrefix(safe, `<style data-dark="">`), "</style>")
	assert.True(t, strings.HasPrefix(safe, `<style data-dark="">`), "dark-aware: its dark sheet is")
	assert.Contains(t, css, "@media (min-width:0px){#godwit-body.godwit-dark .a{color:#eee;}")
	assert.Contains(t, css, "@media screen and (max-width: 600px){#godwit-body .b{width:100%;}")
	assert.NotContains(t, css, ".c", "a media it cannot keep: the sheet goes")
}

// A body's own background image comes along with its wrapper.
func TestSanitizeBodyBackgroundImage(t *testing.T) {
	safe, _ := mail.Sanitize(`<html><body style="background:#eee url(https://c.example/bg.png) repeat-x"><p>x</p></body></html>`, nil, true)
	assert.Contains(t, safe, `data-bg="https://c.example/bg.png"`)
	assert.Contains(t, safe, "background: #eee repeat-x")
}

// Inline styles with trailing newlines, indentation, or trailing semicolons
// are normalised so bluemonday does not drop the style attribute.
func TestSanitizeStyleWhitespace(t *testing.T) {
	input := `<span style="
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      text-decoration: none;
      color: #7A7A7A;
      font-size: 14px;
      line-height: 20px;
      font-weight: 500;
  ">Receipt</span>`
	safe, _ := mail.Sanitize(input, nil, false)
	assert.Contains(t, safe, `color: #7A7A7A`)
	assert.Contains(t, safe, `font-size: 14px`)
	assert.Contains(t, safe, `font-weight: 500`)

	// Card with multi-line styles and background-color
	card := `<table style="
      width: 100%;
      background-color: #ffffff;
      border-radius: 12px;
  "><tr><td>content</td></tr></table>`
	safeCard, _ := mail.Sanitize(card, nil, false)
	assert.Contains(t, safeCard, `background-color: #ffffff`)
	assert.Contains(t, safeCard, `border-radius: 12px`)
}

// A naturally dark canvas does not claim that the author supplied dark CSS.
func TestSanitizeDarkCanvasDoesNotClaimAuthorTheme(t *testing.T) {
	safe, _ := mail.Sanitize(`<body style="background:#111"><p>Dark canvas</p></body>`, nil, false)
	assert.True(t, strings.HasPrefix(safe, `<style data-dark-canvas="">`), safe)
	assert.NotContains(t, safe, `<style data-dark="">`)
	author, _ := mail.Sanitize(`<style>@media (prefers-color-scheme:dark){p{color:#eee}}</style><body style="background:#111"><p>Author theme</p></body>`, nil, false)
	assert.True(t, strings.HasPrefix(author, `<style data-dark="">`), author)
}

func TestSanitizeDarkCanvas(t *testing.T) {
	// Table container with dark background
	stripe := `<table class="st-Background" bgcolor="#000000" width="100%"><tr><td><div style="background-color:#ffffff;color:#1a1a1a">Slip</div></td></tr></table>`
	safeStripe, _ := mail.Sanitize(stripe, nil, false)
	assert.True(t, strings.HasPrefix(safeStripe, `<style data-dark-canvas="">`), "dark container marks its canvas")
	assert.Contains(t, safeStripe, `bgcolor="#000000"`)
	assert.Contains(t, safeStripe, `background-color: #ffffff`)

	// Body with dark bgcolor
	darkBody := `<html><body bgcolor="#1a1a1a"><p>Hello</p></body></html>`
	safeBody, _ := mail.Sanitize(darkBody, nil, false)
	assert.True(t, strings.HasPrefix(safeBody, `<style data-dark-canvas="">`), "dark body bgcolor marks its canvas")
	assert.Contains(t, safeBody, `background-color: #1a1a1a`, "wrapper div receives background-color style from body bgcolor")

	// Meta color-scheme dark
	metaDark := `<html><head><meta name="color-scheme" content="dark"></head><body><p>Hello</p></body></html>`
	safeMeta, _ := mail.Sanitize(metaDark, nil, false)
	assert.True(t, strings.HasPrefix(safeMeta, `<style data-dark-canvas="">`), "meta dark marks its canvas")

	// Standard light email remains without data-dark
	light := `<html><body bgcolor="#ffffff"><p>Hello</p></body></html>`
	safeLight, _ := mail.Sanitize(light, nil, false)
	assert.False(t, strings.HasPrefix(safeLight, `<style data-dark-canvas="">`), "light email has no dark canvas")
}

// A ; inside quotes or a data: URL does not split a declaration, so the ones
// around it survive.
func TestSanitizeStyleSemicolonInValue(t *testing.T) {
	safe, _ := mail.Sanitize(`<span style="
      font-family: 'A;B', sans-serif;
      list-style-image: url(data:image/png;base64,AAAA);
      color: #123456;
  ">x</span>`, nil, false)
	assert.Contains(t, safe, "color: #123456")
}

// What marks a mail as painted on a dark canvas, and what does not.
func TestSanitizeDarkCanvasBounds(t *testing.T) {
	dark := func(src string) bool {
		safe, _ := mail.Sanitize(src, nil, false)
		return strings.HasPrefix(safe, `<style data-dark-canvas="">`)
	}
	assert.True(t, dark(`<div style="width:100%;background-color:#111">x</div>`), "a full-width div by style")
	assert.True(t, dark(`<center><table width="100%" style="background:rgb(20, 20, 20)"><tr><td>x</td></tr></table></center>`), "through one wrapper")
	assert.True(t, dark(`<div style="width:100%;background:no-repeat rgb(0 0 0 / 0.9)">x</div>`), "a shorthand's colour, space-separated")
	assert.True(t, dark(`<meta name="color-scheme" content="only dark"><p>x</p>`))

	assert.False(t, dark(`<table width="100%" bgcolor="#00000000"><tr><td>x</td></tr></table>`), "transparent black, by hex")
	assert.False(t, dark(`<div style="width:100%;background-color:rgba(0,0,0,0)">x</div>`), "transparent black, by rgba")
	assert.False(t, dark(`<div style="width:100%;background-color:#zzz">x</div>`), "a colour it cannot read")
	assert.False(t, dark(`<div style="background-color:#111">x</div>`), "not full width")
	assert.False(t, dark(`<table width="100%" bgcolor="#000" role="button"><tr><td>x</td></tr></table>`), "a button")
	assert.False(t, dark(`<meta name="color-scheme" content="light dark"><p>x</p>`), "either scheme is not a dark one")
	assert.False(t, dark(`<table width="100%"><tr><td><table width="600"><tr><td>Hello`+
		`<table width="100%" bgcolor="#222"><tr><td>Footer</td></tr></table></td></tr></table></td></tr></table>`),
		"a dark footer deep in a light mail")
}

package richtext

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sanitized(t *testing.T, src string) string {
	t.Helper()
	safe, _ := mail.Sanitize(src, nil, false)
	return safe
}

func TestFromMailHTMLBasic(t *testing.T) {
	src := `<h2>Hello</h2>
<p>Some <b>bold <i>and italic</i></b> text,   a <a href="https://example.com/x">link</a>.</p>
<p>line one<br>line two</p>
<ul><li>one</li><li>two<ol><li>nested</li></ol></li></ul>
<blockquote><p>quoted</p><blockquote>deeper</blockquote></blockquote>
<p><span style="color: #c00">red</span> plain</p>`
	d, trunc := FromMailHTML(sanitized(t, src), DefaultLimits)
	assert.False(t, trunc)
	want := []Block{
		{Kind: Heading, Level: 2, Inlines: []Inline{{Text: "Hello"}}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "Some "}, {Text: "bold ", Bold: true}, {Text: "and italic", Bold: true, Italic: true},
			{Text: " text, a "}, {Text: "link", Link: "https://example.com/x"}, {Text: "."}}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "line one\nline two"}}},
		{Kind: ListItem, Level: 1, Inlines: []Inline{{Text: "one"}}},
		{Kind: ListItem, Level: 1, Inlines: []Inline{{Text: "two"}}},
		{Kind: ListItem, Level: 2, Ordered: true, Inlines: []Inline{{Text: "nested"}}},
		{Kind: Quote, Level: 1, Inlines: []Inline{{Text: "quoted"}}},
		{Kind: Quote, Level: 2, Inlines: []Inline{{Text: "deeper"}}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "red", Color: "#c00"}, {Text: " plain"}}},
	}
	assert.Equal(t, want, d.Blocks)
	assert.Equal(t, "Hello\nSome bold and italic text, a link.\nline one\nline two\none\ntwo\nnested\nquoted\ndeeper\nred plain", d.PlainText())
}

func TestFromMailHTMLPreAndImages(t *testing.T) {
	d, _ := FromMailHTML(`<pre>a  b
  c</pre><hr><img src="data:image/png;base64,AAAA" alt="pic" width="20" height="10"><img src="https://h.example/x.png"><img src="data:text/html;base64,AAAA" alt="bad">`, DefaultLimits)
	assert.Equal(t, []Block{
		{Kind: Pre, Inlines: []Inline{{Text: "a  b\n  c"}}},
		{Kind: Rule},
		{Kind: ImageBlock, Image: &Image{Src: "data:image/png;base64,AAAA", Alt: "pic", Width: 20, Height: 10}},
		{Kind: ImageBlock, Image: &Image{Src: "https://h.example/x.png"}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "bad"}}},
	}, d.Blocks)
}

func TestFromComposeHTML(t *testing.T) {
	src := mail.SanitizeCompose(`<p>Hi <b>there</b> <a href="https://x.example/">site</a> <u>u</u> <code>c</code></p><h1>Title</h1><ul><li>a</li></ul><blockquote>q</blockquote><table><tr><td>cell</td></tr></table><hr>`)
	d := FromComposeHTML(src)
	assert.Equal(t, []Block{
		{Kind: Paragraph, Inlines: []Inline{{Text: "Hi "}, {Text: "there", Bold: true}, {Text: " "}, {Text: "site", Link: "https://x.example/"}, {Text: " u c"}}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "Title"}}},
		{Kind: ListItem, Level: 1, Inlines: []Inline{{Text: "a"}}},
		{Kind: Quote, Level: 1, Inlines: []Inline{{Text: "q"}}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "cell"}}},
	}, d.Blocks)
}

func TestTables(t *testing.T) {
	d, trunc := FromMailHTML(sanitized(t, `<table><tr><th>A</th><th>B</th><th>C</th></tr><tr><td>1</td><td><b>2</b></td><td>3</td></tr></table>`), DefaultLimits)
	assert.False(t, trunc)
	require.Len(t, d.Blocks, 1)
	b := d.Blocks[0]
	assert.Equal(t, Table, b.Kind)
	assert.True(t, b.Header)
	assert.Equal(t, [][]Cell{
		{{Inlines: []Inline{{Text: "A"}}}, {Inlines: []Inline{{Text: "B"}}}, {Inlines: []Inline{{Text: "C"}}}},
		{{Inlines: []Inline{{Text: "1"}}}, {Inlines: []Inline{{Text: "2", Bold: true}}}, {Inlines: []Inline{{Text: "3"}}}},
	}, b.Rows)

	// A layout table (no header, nested table) becomes the flow of its cells.
	d, _ = FromMailHTML(sanitized(t, `<table><tr><td><p>left</p></td><td><table><tr><td>inner</td></tr></table></td></tr></table>`), DefaultLimits)
	assert.Equal(t, []Block{
		{Kind: Paragraph, Inlines: []Inline{{Text: "left"}}},
		{Kind: Paragraph, Inlines: []Inline{{Text: "inner"}}},
	}, d.Blocks)

	var sb strings.Builder
	sb.WriteString("<table><tr><th>h1</th><th>h2</th></tr>")
	for i := range 1499 {
		fmt.Fprintf(&sb, "<tr><td>%d</td><td>x</td></tr>", i)
	}
	sb.WriteString("</table>")
	d, trunc = FromMailHTML(sanitized(t, sb.String()), DefaultLimits)
	assert.True(t, trunc)
	require.Len(t, d.Blocks, 1)
	assert.Equal(t, 2000, cells(d.Blocks[0]))
}

func cells(b Block) (n int) {
	for _, r := range b.Rows {
		n += len(r)
	}
	return n
}

func hostileInputs(t *testing.T) map[string]string {
	sample, err := os.ReadFile("testdata/hostile.html")
	require.NoError(t, err)
	var big strings.Builder
	for big.Len() < 2<<20 {
		big.WriteString(`<p>lorem <b>ipsum</b> 中文Go混排 <a data-href="https://e.example/">dolor</a> sit amet</p>`)
	}
	var row strings.Builder
	row.WriteString("<table><tr><th>a</th></tr>")
	for range 20000 {
		row.WriteString("<tr><td>r</td></tr>")
	}
	row.WriteString("</table>")
	return map[string]string{
		"2MB":        big.String(),
		"nested":     strings.Repeat("<div>", 500) + "deep" + strings.Repeat("</div>", 500),
		"rows":       row.String(),
		"javascript": `<p><a data-href="javascript:alert(1)">click</a><a href="javascript:alert(2)">two</a></p>`,
		"sample":     string(sample),
		"sanitized":  sanitized(t, string(sample)),
	}
}

func TestHostileHTML(t *testing.T) {
	in := hostileInputs(t)
	for name, src := range in {
		d, trunc := FromMailHTML(src, DefaultLimits)
		assert.LessOrEqual(t, len(d.Blocks), DefaultLimits.MaxBlocks, name)
		for _, b := range d.Blocks {
			assert.LessOrEqual(t, len(b.Inlines), DefaultLimits.MaxInlines, name)
			assert.LessOrEqual(t, cells(b), DefaultLimits.MaxCells, name)
			for _, in := range b.Inlines {
				assert.NotContains(t, in.Link, "javascript", name)
			}
		}
		text := d.PlainText()
		assert.NotContains(t, text, "alert", name)
		switch name {
		case "2MB", "nested", "rows":
			assert.True(t, trunc, name)
		}
	}
	d, _ := FromMailHTML(in["nested"], DefaultLimits)
	assert.Contains(t, d.PlainText(), "deep")
	d, _ = FromMailHTML(in["javascript"], DefaultLimits)
	assert.Equal(t, []Block{{Kind: Paragraph, Inlines: []Inline{{Text: "clicktwo"}}}}, d.Blocks)
}

func TestHostilePerformance(t *testing.T) {
	if raceEnabled {
		t.Skip("timings are meaningless under -race")
	}
	src := hostileInputs(t)["2MB"]
	start := time.Now()
	d, _ := FromMailHTML(src, DefaultLimits)
	assert.Less(t, time.Since(start), time.Second, "convert")
	start = time.Now()
	tt := ui.NewTester(func(c *ui.Context) { ui.Scroll(c).Fill().Children(func() { Render(c, d, RenderOptions{}) }) }, 800, 600)
	tt.Frame()
	assert.Less(t, time.Since(start), time.Second, "first frame")
}

func TestMarkdownTable(t *testing.T) {
	d := FromMarkdown("# Title\n\n| a | b |\n|---|---|\n| 1 | ~~2~~ |\n")
	require.Len(t, d.Blocks, 2)
	assert.Equal(t, Block{Kind: Heading, Level: 1, Inlines: []Inline{{Text: "Title"}}}, d.Blocks[0])
	assert.Equal(t, Block{Kind: Table, Header: true, Rows: [][]Cell{
		{{Inlines: []Inline{{Text: "a"}}}, {Inlines: []Inline{{Text: "b"}}}},
		{{Inlines: []Inline{{Text: "1"}}}, {Inlines: []Inline{{Text: "2", Strike: true}}}},
	}}, d.Blocks[1])
}

func TestMarkdownLinksSurvive(t *testing.T) {
	d := FromMarkdown("See [docs](https://docs.example/a?b=1&c=2), https://auto.example and [bad](javascript:alert(1)). <b>raw</b>")
	require.Len(t, d.Blocks, 1)
	var links []string
	for _, in := range d.Blocks[0].Inlines {
		if in.Link != "" {
			links = append(links, in.Link)
		}
		assert.False(t, in.Bold, "raw HTML is not rendered")
	}
	assert.Equal(t, []string{"https://docs.example/a?b=1&c=2", "https://auto.example"}, links)
	assert.Contains(t, d.PlainText(), "bad")
}

func TestRenderLinkActivation(t *testing.T) {
	d, _ := FromMailHTML(`<p>Go to <a data-href="https://x.example/">the site</a> now</p>`, DefaultLimits)
	var got []string
	o := RenderOptions{OnLink: func(u string) { got = append(got, u) }}
	tt := ui.NewTester(func(c *ui.Context) { Render(c, d, o) }, 600, 400)
	require.NoError(t, tt.Click("the site"))
	assert.Equal(t, []string{"https://x.example/"}, got)
	assert.Empty(t, tt.OpenedURLs(), "the host opens links, not the renderer")
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyEnter)
	assert.Equal(t, []string{"https://x.example/", "https://x.example/"}, got)
}

func TestLowContrastColorIgnored(t *testing.T) {
	white := ui.RGB(255, 255, 255)
	assert.Nil(t, textColor("#fafafa", white), "near-white on white")
	assert.Nil(t, textColor("rgba(255, 0, 0, 0.1)", white), "faint")
	assert.Nil(t, textColor("nonsense", white))
	assert.NotNil(t, textColor("#c00", white))
	assert.NotNil(t, textColor("navy", white))
	assert.NotNil(t, textColor("rgb(0, 0, 0)", white))
	assert.Nil(t, textColor("navy", ui.RGB(0, 0, 0)), "navy on black")
}

func TestAutospace(t *testing.T) {
	texts := func(s string) (out []string) {
		for _, sp := range Autospace(s) {
			out = append(out, sp.Text)
		}
		return out
	}
	assert.Equal(t, []string{"用", autospace, "Go", autospace, "写"}, texts("用Go写"))
	assert.Equal(t, []string{"用 Go 写"}, texts("用 Go 写"))
	assert.Equal(t, []string{"第", autospace, "3", autospace, "章，Hello"}, texts("第3章，Hello"))
	assert.Equal(t, []string{"plain"}, texts("plain"))
	assert.Nil(t, Autospace(""))
}

func TestToHTMLRoundTrip(t *testing.T) {
	src := `<p>Hi <b>bold <i>both</i></b> <a href="https://x.example/?a=1&amp;b=2">link &lt;3</a></p><p><br></p><p>a<br>b</p>` +
		`<ul><li>one<ul><li>sub</li></ul></li><li>two</li></ul><ol><li>first</li></ol><blockquote><p>q1</p><blockquote>q2</blockquote><p>q1b</p></blockquote><p>end 中文</p>`
	d := FromComposeHTML(mail.SanitizeCompose(src))
	require.NotEmpty(t, d.Blocks)
	h := ToHTML(d)
	assert.Equal(t, d, FromComposeHTML(h), h)
	assert.Equal(t, h, ToHTML(FromComposeHTML(h)), "stable")
}

func TestToHTMLOffsets(t *testing.T) {
	d := Doc{Blocks: []Block{
		{Kind: Paragraph, Inlines: []Inline{{Text: "中文"}, {Text: "a&b", Bold: true}}},
		{Kind: ListItem, Level: 1, Inlines: []Inline{{Text: "x"}}},
	}}
	h, off := ToHTMLOffsets(d)
	assert.Equal(t, "<p>中文<b>a&amp;b</b></p><ul><li>x</li></ul>", h)
	at := func(b, r int) string { return h[off(Pos{b, r}):] }
	assert.True(t, strings.HasPrefix(at(0, 0), "中文"))
	assert.True(t, strings.HasPrefix(at(0, 1), "文"))
	assert.True(t, strings.HasPrefix(at(0, 2), "a&amp;"))
	assert.True(t, strings.HasPrefix(at(0, 3), "&amp;b"))
	assert.True(t, strings.HasPrefix(at(0, 4), "b</b>"))
	assert.True(t, strings.HasPrefix(at(0, 5), "</b></p>"))
	assert.True(t, strings.HasPrefix(at(1, 0), "x</li>"))
	assert.Equal(t, len(h), off(Pos{9, 0}), "past the end")
}

package richtext

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/theme"
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
			assert.LessOrEqual(t, tableInlines(b), DefaultLimits.MaxInlines, name)
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

type sp struct {
	text   string
	spaced bool
}

func simple(spans []ui.Span) (out []sp) {
	for _, s := range spans {
		out = append(out, sp{s.Text, s.LetterSpacing != 0})
	}
	return out
}

func TestAutospace(t *testing.T) {
	assert.Equal(t, []sp{{"用", true}, {"G", false}, {"o", true}, {"写", false}}, simple(Autospace("用Go写", 16)))
	assert.Equal(t, float32(4), Autospace("用Go写", 16)[0].LetterSpacing, "a quarter em")
	assert.Equal(t, []sp{{"用 Go 写", false}}, simple(Autospace("用 Go 写", 16)))
	assert.Equal(t, []sp{{"第", true}, {"3", true}, {"章，Hello", false}}, simple(Autospace("第3章，Hello", 16)))
	assert.Equal(t, []sp{{"plain", false}}, simple(Autospace("plain", 16)))
	assert.Nil(t, Autospace("", 16))
	var text strings.Builder
	for _, s := range Autospace("中文English混排", 16) {
		text.WriteString(s.Text)
	}
	assert.Equal(t, "中文English混排", text.String(), "no characters are inserted")
}

func TestAutospaceAcrossInlines(t *testing.T) {
	d, _ := FromMailHTML(`<p>用<b>Go</b>写<a data-href="https://x.example/">链接link</a></p>`, DefaultLimits)
	r := &renderer{o: RenderOptions{Background: ui.RGB(255, 255, 255)}, t: theme.Light()}
	var got [][]sp
	for _, spans := range r.runs(d.Blocks[0].Inlines, 16, false) {
		got = append(got, simple(spans))
	}
	assert.Equal(t, [][]sp{{{"用", true}}, {{"G", false}, {"o", true}}, {{"写", false}}, {{"链", false}, {"接", true}, {"link", false}}}, got)
}

func TestCodeContrastUsesCodeBackground(t *testing.T) {
	r := &renderer{o: RenderOptions{Background: ui.RGB(255, 255, 255)}, t: theme.Light()}
	grey := Inline{Text: "x", Color: "#939393"} // 3.07:1 on white, 2.8:1 on neutral-100
	assert.NotEqual(t, ui.Color{}, r.runs([]Inline{grey}, 16, false)[0][0].Color)
	code := grey
	code.Code = true
	assert.Equal(t, ui.Color{}, r.runs([]Inline{code}, 16, false)[0][0].Color, "inline code")
	assert.Equal(t, ui.Color{}, r.runs([]Inline{grey}, 16, true)[0][0].Color, "pre")
}

func TestListNumbering(t *testing.T) {
	d, _ := FromMailHTML(`<ol><li>A</li><li>A2</li></ol><ul><li>B</li></ul><ol><li>C</li><ul><li>D</li></ul><li>E</li></ol>`, DefaultLimits)
	tt := ui.NewTester(func(c *ui.Context) { Render(c, d, RenderOptions{}) }, 600, 600)
	var markers []string
	for _, s := range tt.Texts() {
		if strings.HasSuffix(s, ".") || s == "•" {
			markers = append(markers, s)
		}
	}
	assert.Equal(t, []string{"1.", "2.", "•", "1.", "•", "2."}, markers)

	d, _ = FromMailHTML(`<ul><li>A</li></ul><ol><li>B</li></ol>`, DefaultLimits)
	tt = ui.NewTester(func(c *ui.Context) { Render(c, d, RenderOptions{}) }, 600, 400)
	assert.True(t, tt.HasText("1."), "%q", tt.Texts())
	assert.False(t, tt.HasText("2."))
}

func TestTrailingBreaks(t *testing.T) {
	d, _ := FromMailHTML("<pre>a\n</pre>", DefaultLimits)
	assert.Equal(t, []Inline{{Text: "a\n"}}, d.Blocks[0].Inlines)
	d = FromComposeHTML(`<p>a<br><br></p><p><br></p><p>b<br></p>`)
	assert.Equal(t, []Block{
		{Kind: Paragraph, Inlines: []Inline{{Text: "a\n"}}},
		{Kind: Paragraph},
		{Kind: Paragraph, Inlines: []Inline{{Text: "b"}}},
	}, d.Blocks)
	for range 2 {
		next := FromComposeHTML(ToHTML(d))
		assert.Equal(t, d, next, ToHTML(d))
		d = next
	}
	pre := Doc{Blocks: []Block{{Kind: Pre, Inlines: []Inline{{Text: "x\n\n"}}}}}
	back, _ := FromMailHTML(ToHTML(pre), DefaultLimits)
	assert.Equal(t, pre, back, ToHTML(pre))
}

func TestTableInlineBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<table><tr><th>h</th><th>h</th></tr>")
	for range 300 {
		sb.WriteString("<tr>")
		for range 2 {
			sb.WriteString("<td>" + strings.Repeat("a<b>b</b>", 25) + "<p>x</p></td>")
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</table>")
	d, trunc := FromMailHTML(sb.String(), DefaultLimits)
	assert.True(t, trunc)
	require.Len(t, d.Blocks, 1)
	assert.LessOrEqual(t, tableInlines(d.Blocks[0]), DefaultLimits.MaxInlines)
	assert.Greater(t, tableInlines(d.Blocks[0]), DefaultLimits.MaxInlines-200)
}

func tableInlines(b Block) (n int) {
	for _, r := range b.Rows {
		for _, c := range r {
			n += len(c.Inlines)
		}
	}
	return n
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

func TestWideTableFlattens(t *testing.T) {
	row := func(tag string, n int) string {
		var sb strings.Builder
		sb.WriteString("<tr>")
		for i := range n {
			fmt.Fprintf(&sb, "<%s>%s%d</%s>", tag, tag, i, tag)
		}
		return sb.String() + "</tr>"
	}
	d, _ := FromMailHTML(sanitized(t, "<table>"+row("th", 8)+row("td", 8)+"</table>"), DefaultLimits)
	require.Len(t, d.Blocks, 1)
	assert.Equal(t, Table, d.Blocks[0].Kind, "8 columns stay a table")

	d, _ = FromMailHTML(sanitized(t, "<table>"+row("th", 2)+row("td", 9)+"</table>"), DefaultLimits)
	for _, b := range d.Blocks {
		assert.NotEqual(t, Table, b.Kind, "9 columns become paragraphs")
	}
	assert.Contains(t, d.PlainText(), "td8")
}

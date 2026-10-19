package richtext

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// FromMailHTML converts a received mail's sanitized HTML (Message.HTML after
// mail.Sanitize): links come from data-href, images from data: or http(s)
// src. truncated reports that lim cut something off.
func FromMailHTML(safe string, lim Limits) (Doc, bool) {
	c := &conv{lim: lim}
	c.run(safe)
	return Doc{Blocks: c.blocks}, c.truncated
}

// FromComposeHTML converts compose content (mail.SanitizeCompose output or a
// draft's editable part): links come from href, and only paragraphs, bold,
// italic, links, lists, quotes and data: images (a draft's signature images,
// for showing) stay; everything else becomes paragraph text.
func FromComposeHTML(safe string) Doc {
	c := &conv{lim: DefaultLimits, compose: true}
	c.run(safe)
	return Doc{Blocks: c.blocks}
}

// ctx is what the enclosing elements say about the text inside them.
type ctx struct {
	style       Inline // Text unused
	kind        BlockKind
	level       int
	ordered     bool
	quote, list int
	pre         bool
	depth       int
}

type conv struct {
	lim       Limits
	compose   bool
	truncated bool
	blocks    []Block

	// the open block: its finished runs, the last run's style and text
	open      bool
	block     Block
	runs      []Inline
	last      Inline
	buf       strings.Builder
	lastSpace bool
}

func (c *conv) run(src string) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return
	}
	for n := doc.FirstChild; n != nil; n = n.NextSibling {
		if n.DataAtom == atom.Html {
			for b := n.FirstChild; b != nil; b = b.NextSibling {
				if b.DataAtom == atom.Body {
					c.walk(b, ctx{})
				}
			}
		}
	}
	c.flush()
}

func (c *conv) full() bool {
	if len(c.blocks) >= c.lim.MaxBlocks {
		c.truncated = true
		return true
	}
	return false
}

func (c *conv) walk(n *html.Node, x ctx) {
	for k := n.FirstChild; k != nil && !c.full(); k = k.NextSibling {
		switch k.Type {
		case html.TextNode:
			c.text(k.Data, x)
		case html.ElementNode:
			c.element(k, x)
		}
	}
}

var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true, atom.Template: true, atom.Noscript: true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Textarea: true, atom.Select: true, atom.Svg: true, atom.Math: true,
}

// blockish elements end the text before them and start a new block.
var blockish = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Center: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Main: true, atom.Nav: true, atom.Aside: true, atom.Address: true, atom.Figure: true,
	atom.Figcaption: true, atom.Dl: true, atom.Dt: true, atom.Dd: true, atom.Form: true, atom.Fieldset: true,
	atom.Details: true, atom.Summary: true, atom.Caption: true, atom.Tbody: true, atom.Thead: true, atom.Tfoot: true,
	atom.Tr: true, atom.Td: true, atom.Th: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true,
	atom.H5: true, atom.H6: true, atom.Pre: true,
}

func (c *conv) element(n *html.Node, x ctx) {
	if skipped[n.DataAtom] {
		return
	}
	if x.depth >= c.lim.MaxDepth {
		c.truncated = true
		c.text(textContent(n), x)
		return
	}
	x.depth++
	if !c.compose {
		c.styleAttrs(n, &x.style)
	}
	switch a := n.DataAtom; a {
	case atom.Br:
		c.lineBreak(x)
	case atom.B, atom.Strong:
		x.style.Bold = true
		c.walk(n, x)
	case atom.I, atom.Em:
		x.style.Italic = true
		c.walk(n, x)
	case atom.Code, atom.Tt, atom.Kbd, atom.Samp:
		x.style.Code = !c.compose
		c.walk(n, x)
	case atom.S, atom.Strike, atom.Del:
		x.style.Strike = true
		c.walk(n, x)
	case atom.U, atom.Ins:
		x.style.Underline = true
		c.walk(n, x)
	case atom.A:
		key := "data-href"
		if c.compose {
			key = "href"
		}
		if l := attr(n, key); safeLink(l) {
			x.style.Link = l
		}
		c.walk(n, x)
	case atom.Img:
		c.image(n, x)
	case atom.Hr:
		if !c.compose {
			c.flush()
			c.emit(Block{Kind: Rule})
		}
	case atom.Ul, atom.Ol:
		c.flush()
		x.list++
		x.ordered = a == atom.Ol
		c.walk(n, x)
		c.flush()
	case atom.Li:
		x.kind, x.level = ListItem, max(x.list, 1)
		c.block1(n, x)
	case atom.Blockquote:
		x.quote++
		x.kind, x.level = Quote, x.quote
		c.block1(n, x)
	case atom.Table:
		c.table(n, x)
	default:
		if !c.compose {
			if lv := headingLevel(a); lv > 0 {
				x.kind, x.level = Heading, lv
			} else if a == atom.Pre {
				x.kind, x.level, x.pre = Pre, 0, true
			}
		}
		if blockish[a] {
			c.block1(n, x)
		} else {
			c.walk(n, x)
		}
	}
}

func (c *conv) block1(n *html.Node, x ctx) {
	c.flush()
	c.walk(n, x)
	c.flush()
}

func headingLevel(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	case atom.H6:
		return 6
	}
	return 0
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func safeLink(l string) bool {
	l = strings.ToLower(strings.TrimSpace(l))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:")
}

var boldWeight = regexp.MustCompile(`^(bold|bolder|[6-9]00)$`)

// styleAttrs applies the sanitized inline style and <font color>.
func (c *conv) styleAttrs(n *html.Node, s *Inline) {
	if col := strings.TrimSpace(attr(n, "color")); col != "" && n.DataAtom == atom.Font {
		s.Color = col
	}
	for decl := range strings.SplitSeq(attr(n, "style"), ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		lv := strings.ToLower(v)
		switch k {
		case "color":
			s.Color = v
		case "font-weight":
			s.Bold = boldWeight.MatchString(lv)
		case "font-style":
			s.Italic = lv == "italic" || lv == "oblique"
		case "text-decoration":
			s.Underline = s.Underline || strings.Contains(lv, "underline")
			s.Strike = s.Strike || strings.Contains(lv, "line-through")
		}
	}
}

var imageData = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp)[;,]`)

func (c *conv) image(n *html.Node, x ctx) {
	src, alt := strings.TrimSpace(attr(n, "src")), attr(n, "alt")
	l := strings.ToLower(src)
	if c.compose && !imageData.MatchString(l) { // a draft shows only its signature images, as data:
		return
	}
	if !imageData.MatchString(l) && !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
		c.text(alt, x)
		return
	}
	w, _ := strconv.Atoi(attr(n, "width"))
	h, _ := strconv.Atoi(attr(n, "height"))
	c.flush()
	c.emit(Block{Kind: ImageBlock, Image: &Image{Src: src, Alt: alt, Width: max(w, 0), Height: max(h, 0)}})
}

// maxColumns is the widest table kept as a table.
const maxColumns = 8

// table keeps a data table (header cells, at most maxColumns columns, no
// nested table or image) as a table and lays any other out as the flow of
// its cells.
func (c *conv) table(n *html.Node, x ctx) {
	c.flush()
	var rows []*html.Node
	header, layout := false, c.compose
	walkTable(n, func(tr *html.Node) {
		rows = append(rows, tr)
		cols := 0
		for td := tr.FirstChild; td != nil; td = td.NextSibling {
			if td.DataAtom == atom.Td || td.DataAtom == atom.Th {
				cols++
			}
		}
		layout = layout || cols > maxColumns
	})
	for k := range descendants(n) {
		switch k.DataAtom {
		case atom.Th:
			header = true
		case atom.Table, atom.Img:
			layout = true
		}
	}
	if layout || !header || len(rows) == 0 {
		c.walk(n, x)
		c.flush()
		return
	}
	b := Block{Kind: Table}
	total, inlines := 0, 0 // the table's cells and inlines, against MaxCells and MaxInlines
	for i, tr := range rows {
		var row []Cell
		allTh, n := true, 0
		for td := tr.FirstChild; td != nil; td = td.NextSibling {
			if td.DataAtom != atom.Td && td.DataAtom != atom.Th {
				continue
			}
			allTh = allTh && td.DataAtom == atom.Th
			cell := c.cell(td, x)
			n += len(cell.Inlines)
			row = append(row, cell)
		}
		if total+len(row) > c.lim.MaxCells || inlines+n > c.lim.MaxInlines {
			c.truncated = true
			break
		}
		total, inlines = total+len(row), inlines+n
		if i == 0 {
			b.Header = allTh && len(row) > 0
		}
		b.Rows = append(b.Rows, row)
	}
	c.emit(b)
}

// cell is a table cell's text; its blocks become lines.
func (c *conv) cell(td *html.Node, x ctx) Cell {
	sub := &conv{lim: c.lim}
	x.kind, x.level = Paragraph, 0
	sub.walk(td, x)
	sub.flush()
	c.truncated = c.truncated || sub.truncated
	var ins []Inline
	for i, b := range sub.blocks {
		if i > 0 {
			ins = append(ins, Inline{Text: "\n"})
		}
		ins = append(ins, b.Inlines...)
	}
	return Cell{Inlines: ins}
}

// walkTable calls f for the rows of table t, not those of nested tables.
func walkTable(t *html.Node, f func(*html.Node)) {
	for k := t.FirstChild; k != nil; k = k.NextSibling {
		switch k.DataAtom {
		case atom.Tr:
			f(k)
		case atom.Thead, atom.Tbody, atom.Tfoot:
			walkTable(k, f)
		}
	}
}

func descendants(n *html.Node) func(func(*html.Node) bool) {
	return func(yield func(*html.Node) bool) {
		var rec func(*html.Node) bool
		rec = func(n *html.Node) bool {
			for k := n.FirstChild; k != nil; k = k.NextSibling {
				if !yield(k) || !rec(k) {
					return false
				}
			}
			return true
		}
		rec(n)
	}
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	for k := range descendants(n) {
		if k.Type == html.TextNode && (k.Parent == nil || !skipped[k.Parent.DataAtom]) {
			sb.WriteString(k.Data)
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

var space = regexp.MustCompile(`[ \t\n\f\r]+`)

// text adds s in x's style, collapsing white space as HTML does outside <pre>.
func (c *conv) text(s string, x ctx) {
	if x.pre {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	} else {
		s = space.ReplaceAllString(s, " ")
		if c.lastSpace || !c.open {
			s = strings.TrimLeft(s, " ")
		}
	}
	if s == "" {
		return
	}
	c.add(s, x)
	c.lastSpace = !x.pre && strings.HasSuffix(s, " ")
}

func (c *conv) lineBreak(x ctx) {
	c.trimSpace()
	c.add("\n", x)
	c.lastSpace = true
}

func (c *conv) add(s string, x ctx) {
	if !c.open {
		c.open, c.lastSpace = true, false
		c.block = Block{Kind: x.kind, Level: x.level, Ordered: x.ordered && x.kind == ListItem}
	}
	if c.buf.Len() > 0 && c.last == x.style {
		c.buf.WriteString(s)
		return
	}
	c.closeRun()
	c.last = x.style
	c.buf.WriteString(s)
}

func (c *conv) closeRun() {
	if c.buf.Len() == 0 {
		return
	}
	if len(c.runs) >= c.lim.MaxInlines {
		c.truncated = true
	} else {
		in := c.last
		in.Text = c.buf.String()
		c.runs = append(c.runs, in)
	}
	c.buf.Reset()
}

// trimSpace drops a space at the end of the open run (HTML does not show it
// before a line break or the end of a block).
func (c *conv) trimSpace() {
	if s := c.buf.String(); strings.HasSuffix(s, " ") && c.block.Kind != Pre {
		c.buf.Reset()
		c.buf.WriteString(strings.TrimRight(s, " "))
	}
}

// flush ends the open block. Outside <pre> a block's last line break only
// holds its line open, as in HTML, so it is not text (ToHTML writes it
// back); a block of only that break stays as an empty block (a blank line).
func (c *conv) flush() {
	if !c.open {
		return
	}
	c.trimSpace()
	if s := c.buf.String(); strings.HasSuffix(s, "\n") && c.block.Kind != Pre {
		c.buf.Reset()
		c.buf.WriteString(s[:len(s)-1])
	}
	c.closeRun()
	b := c.block
	b.Inlines, c.runs, c.open = c.runs, nil, false
	c.emit(b)
}

func (c *conv) emit(b Block) {
	if !c.full() {
		c.blocks = append(c.blocks, b)
	}
}

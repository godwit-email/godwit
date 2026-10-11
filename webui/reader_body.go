package webui

import (
	"strings"

	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/godwit-email/godwit/internal/richtext"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// bodyLimits bound a body's structure with the native reader's numbers
// (richtext.DefaultLimits): element nesting, block elements, cells of one
// table, and inline nodes (non-empty text nodes and inline elements) of the
// whole body, table cells included. They are performance guards, counted
// simply: no parity with richtext's merged runs is meant. Past the block or
// inline limit the rest of the body goes: only its beginning is shown, as
// the native truncated notice says.
var bodyLimits = richtext.DefaultLimits

// blockElems are the elements counted as blocks.
var blockElems = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Center: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Main: true, atom.Nav: true, atom.Aside: true, atom.Address: true, atom.Figure: true,
	atom.Figcaption: true, atom.Dl: true, atom.Dt: true, atom.Dd: true, atom.Details: true, atom.Summary: true,
	atom.Caption: true, atom.Tbody: true, atom.Thead: true, atom.Tfoot: true, atom.Tr: true, atom.Td: true,
	atom.Th: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Pre: true, atom.Ul: true, atom.Ol: true, atom.Li: true, atom.Blockquote: true, atom.Table: true,
	atom.Hr: true,
}

// prepareBody readies safe, a sanitized body (mail.Sanitize), for the
// reader page:
//   - Every data: image must be a PNG, JPEG, GIF or WebP within the
//     per-image limits and what budget has left (count, bytes, pixels),
//     which the message's remote images share; one that is not loses its
//     src (its alt stays).
//   - The structure is held to bodyLimits. An element nested deeper than
//     MaxDepth becomes its text; a table's rows past MaxCells cells go; past
//     MaxBlocks blocks or MaxInlines inline nodes, the rest of the body
//     goes. Cuts fall on element boundaries; the result is
//     well-formed.
//
// remote is the http(s) image sources left in the result, as the DOM reads
// their src attributes; cut reports that the structure was cut. A body
// needing no change comes back as it was.
func prepareBody(safe string, budget *imagefetch.Budget) (out string, remote map[string]bool, cut bool) {
	root := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(safe), root)
	if err != nil {
		return "", map[string]bool{}, true
	}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	p := &preparer{budget: budget, remote: map[string]bool{}}
	p.walk(root, 0, nil)
	if !p.changed {
		return safe, p.remote, false
	}
	var b strings.Builder
	for n := root.FirstChild; n != nil; n = n.NextSibling {
		if err := html.Render(&b, n); err != nil {
			return "", map[string]bool{}, true
		}
	}
	return b.String(), p.remote, p.cut
}

type preparer struct {
	budget  *imagefetch.Budget
	remote  map[string]bool
	blocks  int
	inlines int
	stopped bool // a limit ended the body: everything after goes
	cut     bool
	changed bool
}

// table is a table's cells so far, against MaxCells; full once a row did
// not fit.
type table struct {
	cells int
	full  bool
}

// walk keeps n's children within the limits; depth is n's nesting, t the
// table n is in.
func (p *preparer) walk(n *html.Node, depth int, t *table) {
	for k := n.FirstChild; k != nil; {
		next := k.NextSibling
		if p.stopped {
			n.RemoveChild(k)
			k = next
			continue
		}
		switch k.Type {
		case html.TextNode:
			if strings.TrimSpace(k.Data) != "" && !p.inline() {
				n.RemoveChild(k)
			}
		case html.ElementNode:
			p.element(n, k, depth+1, t)
		}
		k = next
	}
}

// inline counts one inline node; false (and the body stops) past
// MaxInlines.
func (p *preparer) inline() bool {
	if p.inlines >= bodyLimits.MaxInlines {
		p.stop()
		return false
	}
	p.inlines++
	return true
}

func (p *preparer) stop() { p.stopped, p.cut, p.changed = true, true, true }

func (p *preparer) element(parent, k *html.Node, depth int, t *table) {
	if depth > bodyLimits.MaxDepth {
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: textOf(k)}, k)
		parent.RemoveChild(k)
		p.cut, p.changed = true, true
		return
	}
	if k.DataAtom == atom.Tr && t != nil {
		row := 0
		for c := k.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom == atom.Td || c.DataAtom == atom.Th {
				row++
			}
		}
		if t.full || t.cells+row > bodyLimits.MaxCells {
			t.full = true
			parent.RemoveChild(k)
			p.cut, p.changed = true, true
			return
		}
		t.cells += row
	}
	if blockElems[k.DataAtom] {
		if p.blocks >= bodyLimits.MaxBlocks {
			parent.RemoveChild(k)
			p.stop()
			return
		}
		p.blocks++
		if k.DataAtom == atom.Table {
			t = &table{}
		}
		p.walk(k, depth, t)
		return
	}
	if !p.inline() {
		parent.RemoveChild(k)
		return
	}
	if k.DataAtom == atom.Img {
		p.image(k)
	}
	p.walk(k, depth, t)
}

// image checks img's src: a data: image against the budget (its src goes
// when it does not fit), an http(s) one noted as remote.
func (p *preparer) image(img *html.Node) {
	i := attrIndex(img, "src")
	if i < 0 {
		return
	}
	src := img.Attr[i].Val
	switch lower := strings.ToLower(src); {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		p.remote[src] = true
	case strings.HasPrefix(lower, "data:"):
		if p.inlineImage(src) == nil {
			return
		}
		fallthrough
	default:
		img.Attr = append(img.Attr[:i], img.Attr[i+1:]...)
		p.changed = true
	}
}

// inlineImage takes a data: image from the budget: nil when it fits.
func (p *preparer) inlineImage(src string) error {
	data, err := imagefetch.DataURL(src)
	if err != nil {
		return err
	}
	_, px, err := imagefetch.Config(data)
	if err != nil {
		return err
	}
	return p.budget.TakeInline(int64(len(data)), px)
}

func attrIndex(n *html.Node, key string) int {
	for i, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return i
		}
	}
	return -1
}

// textOf is n's text, all of it.
func textOf(n *html.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			b.WriteString(d.Data)
		}
	}
	return b.String()
}

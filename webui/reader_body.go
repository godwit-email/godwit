package webui

import (
	"strings"

	"github.com/godwit-email/godwit/internal/imagefetch"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// prepareBody readies safe, a sanitized body (mail.Sanitize), for the
// reader page: every data: image must be a PNG, JPEG, GIF or WebP within the
// per-image limits and what budget has left (count, bytes, pixels), which
// the message's remote images share; one that is not loses its src (its alt
// stays). The structure is not limited: the browser renders the whole body.
//
// remote is the http(s) image sources left in the result, as the DOM reads
// their src attributes. A body needing no change comes back as it was.
func prepareBody(safe string, budget *imagefetch.Budget) (out string, remote map[string]bool) {
	root := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(safe), root)
	if err != nil {
		return "", map[string]bool{}
	}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	p := &preparer{budget: budget, remote: map[string]bool{}}
	for n := range root.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Img {
			p.image(n)
		}
	}
	if !p.changed {
		return safe, p.remote
	}
	var b strings.Builder
	for n := root.FirstChild; n != nil; n = n.NextSibling {
		if err := html.Render(&b, n); err != nil {
			return "", map[string]bool{}
		}
	}
	return b.String(), p.remote
}

type preparer struct {
	budget  *imagefetch.Budget
	remote  map[string]bool
	changed bool
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

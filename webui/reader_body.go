package webui

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// bgAttr is where the sanitizer (core/mail) keeps an element's remote CSS
// background image.
const bgAttr = "data-bg"

// prepareBody readies safe, a sanitized body (mail.Sanitize), for the
// reader page: every data: image must be a PNG, JPEG, GIF or WebP within the
// per-image limits and what budget has left (count, bytes, pixels); one that
// is not loses its src (its alt stays). The structure is not limited: the
// browser renders the whole body.
//
// Each remote image (an http(s) src, a data-bg) is pointed at the image
// proxy, godwit-img://m<message>/<token>; tokens maps each token to its
// URL. An <img> with no size gets the one dims knows (the image is cached),
// so its space is kept before it loads. A body needing no change comes back
// as it was.
func prepareBody(safe string, budget *imagefetch.Budget, message int64,
	dims func(src string) (w, h int, ok bool)) (out string, tokens map[string]string) {
	root := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(safe), root)
	if err != nil {
		return "", map[string]string{}
	}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	p := &preparer{budget: budget, message: message, dims: dims, tokens: map[string]string{}}
	for n := range root.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if n.DataAtom == atom.Img {
			p.image(n)
		}
		p.background(n)
	}
	if !p.changed {
		return safe, p.tokens
	}
	var b strings.Builder
	for n := root.FirstChild; n != nil; n = n.NextSibling {
		if err := html.Render(&b, n); err != nil {
			return "", map[string]string{}
		}
	}
	return b.String(), p.tokens
}

type preparer struct {
	budget  *imagefetch.Budget
	message int64
	dims    func(src string) (w, h int, ok bool)
	tokens  map[string]string
	changed bool
}

// proxy is the godwit-img: URL of the remote image src, or "" when the
// message has used up its image count (and, for cached images, pixels): the
// page loads every image at once, so a message cannot hold unbounded ones.
func (p *preparer) proxy(src string) string {
	token := imagecache.Key(src)
	if _, seen := p.tokens[token]; !seen {
		if p.budget.Take() != nil {
			return ""
		}
		if w, h, ok := p.dims(src); ok && p.budget.TakePixels(int64(w)*int64(h)) != nil {
			return ""
		}
	}
	p.tokens[token] = src
	p.changed = true
	return (&url.URL{Scheme: ImageScheme, Host: "m" + strconv.FormatInt(p.message, 10), Path: "/" + token}).String()
}

// image checks img's src: a data: image against the budget (its src goes
// when it does not fit), an http(s) one goes through the proxy.
func (p *preparer) image(img *html.Node) {
	i := attrIndex(img, "src")
	if i < 0 {
		return
	}
	src := img.Attr[i].Val
	switch lower := strings.ToLower(src); {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		if u := p.proxy(src); u != "" {
			img.Attr[i].Val = u
			p.reserve(img, src)
			return
		}
		img.Attr = append(img.Attr[:i], img.Attr[i+1:]...)
		p.changed = true
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

// reserve gives img the cached size of src when it declares none.
func (p *preparer) reserve(img *html.Node, src string) {
	if attrIndex(img, "width") >= 0 || attrIndex(img, "height") >= 0 {
		return
	}
	if st := attrIndex(img, "style"); st >= 0 {
		if v := strings.ToLower(img.Attr[st].Val); strings.Contains(v, "width") || strings.Contains(v, "height") {
			return
		}
	}
	if w, h, ok := p.dims(src); ok {
		img.Attr = append(img.Attr, html.Attribute{Key: "width", Val: strconv.Itoa(w)}, html.Attribute{Key: "height", Val: strconv.Itoa(h)})
	}
}

// background turns n's data-bg into an inline background-image through the proxy.
func (p *preparer) background(n *html.Node) {
	i := attrIndex(n, bgAttr)
	if i < 0 {
		return
	}
	src := n.Attr[i].Val
	n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
	p.changed = true
	if l := strings.ToLower(src); !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
		return
	}
	u := p.proxy(src)
	if u == "" {
		return
	}
	decl := "background-image:url(" + u + ")"
	if st := attrIndex(n, "style"); st >= 0 {
		n.Attr[st].Val = strings.TrimRight(strings.TrimSpace(n.Attr[st].Val), ";") + ";" + decl
		return
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "style", Val: decl})
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

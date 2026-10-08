package mail

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const cidScheme = "godwit-cid:" // bluemonday drops cid:, so it is swapped for this until the final pass

// Safe CSS values: no parentheses except rgb()/rgba(), so url(), expression() and @import cannot occur.
var cssValue = regexp.MustCompile(`(?i)^([a-z0-9#%.,\s'"\-+_]|rgba?\([0-9.,\s%]*\))+$`)

var cssProps = []string{
	"color", "background-color", "font-family", "font-size", "font-weight", "font-style", "text-align",
	"text-decoration", "line-height", "width", "height", "display", "vertical-align",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left", "border-color", "border-style",
	"border-width", "border-collapse", "border-spacing", "border-radius",
}

func policy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("table", "thead", "tbody", "tfoot", "tr", "td", "th", "caption", "colgroup", "col", "div", "span", "font", "center")
	p.AllowAttrs("colspan", "rowspan", "align", "valign", "width", "height", "bgcolor", "color", "size", "face").Globally()
	p.AllowAttrs("style").Globally()
	p.AllowStyles(cssProps...).Matching(cssValue).Globally()
	p.AllowURLSchemes("http", "https", "mailto", strings.TrimSuffix(cidScheme, ":"))
	p.RequireNoFollowOnLinks(false)
	return p
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func delAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func isSlash(b byte) bool { return b == '/' || b == '\\' }

// protoRelative also covers browser-normalised forms like `\\host/x` and `/\host`.
func protoRelative(u string) bool { return len(u) >= 2 && isSlash(u[0]) && isSlash(u[1]) }

func isRemote(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || protoRelative(u)
}

// safeRemoteImage accepts only plain absolute http(s) URLs with a host and no backslashes.
func safeRemoteImage(u string) bool {
	if strings.ContainsAny(u, "\\\t\r\n ") {
		return false
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

func walkNodes(n *html.Node, f func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		f(c)
		walkNodes(c, f)
	}
}

func render(parent *html.Node) string {
	var sb strings.Builder
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&sb, c)
	}
	return sb.String()
}

// prepare counts remote resources in the original HTML and swaps cid: for the
// placeholder scheme, returning the body-only markup for bluemonday.
func prepare(src string) (string, int) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", 0
	}
	blocked := 0
	var body *html.Node
	walkNodes(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.DataAtom == atom.Body {
			body = n
		}
		if s, ok := attr(n, "style"); ok && strings.Contains(strings.ToLower(s), "url(") {
			blocked++
		}
		if n.DataAtom != atom.Img {
			return
		}
		if _, ok := attr(n, "srcset"); ok {
			blocked++
		}
		s, _ := attr(n, "src")
		s = strings.TrimSpace(s)
		switch {
		case len(s) >= 4 && strings.EqualFold(s[:4], "cid:"):
			setAttr(n, "src", cidScheme+url.QueryEscape(cleanID(s[4:])))
		case isRemote(s):
			blocked++
			if strings.HasPrefix(s, "//") { // backslash forms stay as-is and are rejected later
				setAttr(n, "src", "https:"+s)
			}
		}
	})
	if body == nil {
		return "", blocked
	}
	return render(body), blocked
}

var allowedImage = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// finish applies the display contract on bluemonday's output.
func finish(safe string, inline map[string]Attachment, loadRemote, compose bool) string {
	ctx := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}
	nodes, err := html.ParseFragment(strings.NewReader(safe), ctx)
	if err != nil {
		return ""
	}
	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	walkNodes(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		delAttr(n, "srcset")
		switch n.DataAtom {
		case atom.Img:
			src, _ := attr(n, "src")
			switch {
			case strings.HasPrefix(src, cidScheme):
				id, _ := url.QueryUnescape(strings.TrimPrefix(src, cidScheme))
				a, ok := inline[id]
				ct := http.DetectContentType(a.Data)
				if compose || !ok || !allowedImage[ct] {
					delAttr(n, "src")
				} else {
					setAttr(n, "src", "data:"+ct+";base64,"+base64.StdEncoding.EncodeToString(a.Data))
				}
			case !compose && loadRemote && safeRemoteImage(src):
				// validated absolute http(s): keep
			default: // relative, backslash forms, data:, other schemes, remote when not allowed
				delAttr(n, "src")
			}
		case atom.A:
			href, ok := attr(n, "href")
			if !ok || compose {
				return
			}
			delAttr(n, "href")
			if l := strings.ToLower(strings.TrimSpace(href)); strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:") {
				setAttr(n, "data-href", href)
			}
		}
	})
	return render(root)
}

// Sanitize cleans received HTML. inline is keyed by Content-ID. Links become
// data-href (not navigable); remote images are dropped unless loadRemote.
// blockedRemote counts remote resources found in the original HTML.
func Sanitize(src string, inline map[string]Attachment, loadRemote bool) (safe string, blockedRemote int) {
	prepared, blocked := prepare(src)
	return finish(policy().Sanitize(prepared), inline, loadRemote, false), blocked
}

// SanitizeCompose cleans pasted content in the composer: same whitelist, no remote resources.
func SanitizeCompose(src string) string {
	prepared, _ := prepare(src)
	return finish(policy().Sanitize(prepared), nil, false, true)
}

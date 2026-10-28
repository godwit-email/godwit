package mail

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gorilla/css/scanner"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// bgAttr carries the remote URL of an element's CSS background image: the
// style allowlist drops url(), so prepare moves it here.
const bgAttr = "data-bg"

var remoteURL = regexp.MustCompile(`(?i)^https?://[^\s\\]+$`)

const cidScheme = "godwit-cid:" // bluemonday drops cid:, so it is swapped for this until the final pass

// Safe CSS values: no parentheses except rgb()/rgba(), so url(), expression() and @import cannot occur.
var cssValue = regexp.MustCompile(`(?i)^([a-z0-9#%.,/\s'"\-+_]|rgba?\([0-9.,\s%]*\))+$`)

var cssProps = []string{
	"color", "background-color", "font-family", "font-size", "font-weight", "font-style", "text-align",
	"text-decoration", "line-height", "width", "height", "display", "vertical-align",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left", "border-color", "border-style",
	"border-width", "border-collapse", "border-spacing", "border-radius",
	// a background image shows at its natural size without these (a big cover is just its corner)
	"background-size", "background-repeat", "background-position",
	// layout: a card is centred at a max-width, buttons keep their width, corners stay round
	"max-width", "min-width", "max-height", "min-height", "box-sizing", "float",
	"border-top-left-radius", "border-top-right-radius", "border-bottom-left-radius", "border-bottom-right-radius",
	"border-top-width", "border-right-width", "border-bottom-width", "border-left-width",
	"border-top-style", "border-right-style", "border-bottom-style", "border-left-style",
	"border-top-color", "border-right-color", "border-bottom-color", "border-left-color",
	"overflow", "overflow-x", "overflow-y", "opacity", "letter-spacing", "text-transform", "text-indent",
	"white-space", "word-break", "list-style-type", "background", "font", "box-shadow", "text-shadow",
	"table-layout", "clear", "outline", "empty-cells", "direction",
}

func policy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("table", "thead", "tbody", "tfoot", "tr", "td", "th", "caption", "colgroup", "col", "div", "span", "font", "center")
	p.AllowAttrs("colspan", "rowspan", "align", "valign", "width", "height", "bgcolor", "color", "size", "face").Globally()
	p.AllowAttrs("style").Globally()
	// cellspacing="0" closes the gaps in a card made of bgcolor cells
	p.AllowAttrs("cellspacing", "cellpadding", "border").Matching(bluemonday.Integer).OnElements("table")
	p.AllowAttrs(bgAttr).Matching(remoteURL).Globally() // set by prepare from a background url()
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

// cleanNames keeps of n's class names only the plain ones (letters, digits, - and _),
// and its id when it is one and not the reader's own container.
func cleanNames(n *html.Node) {
	plain := func(s string) bool {
		for _, r := range s {
			if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
		return s != ""
	}
	if c, ok := attr(n, "class"); ok {
		var keep []string
		for _, name := range strings.Fields(c) {
			if plain(name) {
				keep = append(keep, name)
			}
		}
		setAttr(n, "class", strings.Join(keep, " "))
	}
	if id, ok := attr(n, "id"); ok && (!plain(id) || strings.EqualFold(id, "godwit-body")) {
		delAttr(n, "id")
	}
}

// openable reports whether the reader can open href: http, https or mailto,
// as url.Parse reads it, as the reader's opener does (webui.ReaderPage.OpenLink);
// a link it would refuse (a bad port) is no link.
func openable(href string) bool {
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
		return true
	}
	return false
}

// nodeText is the text directly inside n (a <style>'s sheet).
func nodeText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
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

// prepare counts remote resources and tracking pixels (dropped, never counted
// as remote) in the original HTML and swaps cid: for the placeholder scheme, returning the body-only markup for bluemonday. For
// received mail the links move to data-href here: bluemonday drops an href it
// cannot parse (a space, a {placeholder}, a bad port), which would leave the
// link dead. It also detects whether the email has a dark canvas (via meta
// color-scheme or a dark background on body/containers), separately from
// author-provided dark CSS.
func prepare(src string, received bool) (string, int, int, []styleSheet, bool) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", 0, 0, nil, false
	}
	blocked, trackers := 0, 0
	var body *html.Node
	var pixels, styles []*html.Node
	var sheets []styleSheet
	darkCanvas := false
	walkNodes(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if name, _ := attr(n, "name"); received && n.DataAtom == atom.Meta && strings.EqualFold(name, "color-scheme") {
			// "light dark" only says the mail can be shown either way, not that it is dark
			content, _ := attr(n, "content")
			if f := strings.Fields(strings.ToLower(content)); slices.Contains(f, "dark") && !slices.Contains(f, "light") {
				darkCanvas = true
			}
		}
		if n.DataAtom == atom.Body {
			body = n
			if received && isDarkColor(elementBackground(n)) {
				darkCanvas = true
			}
		}
		if received && (n.DataAtom == atom.Table || n.DataAtom == atom.Div) {
			if role, _ := attr(n, "role"); role != "button" && isContainer(n) && topLevel(n) && isDarkColor(elementBackground(n)) {
				darkCanvas = true
			}
		}
		if n.DataAtom == atom.Style {
			if received { // kept by sanitizeCSS, in what Sanitize returns
				media, _ := attr(n, "media") // the sheet holds only where it does
				sheets = append(sheets, styleSheet{text: nodeText(n), media: media})
			}
			styles = append(styles, n)
			return
		}
		delAttr(n, bgAttr)
		if received {
			delAttr(n, "data-href") // only ours below; the policy lets it through
			cleanNames(n)
			if href, ok := attr(n, "href"); ok && (n.DataAtom == atom.A || n.DataAtom == atom.Area) {
				if href = strings.TrimSpace(href); openable(href) {
					setAttr(n, "data-href", href)
				}
				delAttr(n, "href")
			}
		}
		if s, ok := attr(n, "style"); ok {
			st := s
			if strings.Contains(strings.ToLower(s), "url(") {
				blocked++
				if rest, u := splitBackground(s); u != "" {
					setAttr(n, bgAttr, u)
					st = rest
				}
			}
			if received { // bluemonday drops a whole style it cannot parse, as "a: b;\n  " can be
				st = cleanStyle(st)
			}
			if st != s {
				setAttr(n, "style", st)
			}
		}
		if n.DataAtom != atom.Img {
			return
		}
		if isTracker(n) {
			pixels = append(pixels, n)
			trackers++
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
	for _, n := range append(pixels, styles...) {
		n.Parent.RemoveChild(n)
	}
	if body == nil {
		return "", blocked, trackers, nil, false
	}
	if received { // the page's own background sits on <body>, which only its children survive: wrap them
		w := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
		for _, k := range []string{"style", "bgcolor", bgAttr} { // bgAttr: the body's own background image
			if v, ok := attr(body, k); ok {
				setAttr(w, k, v)
			}
		}
		if bg, ok := attr(body, "bgcolor"); ok && bg != "" {
			st, _ := attr(w, "style")
			if !strings.Contains(strings.ToLower(st), "background") {
				if st != "" && !strings.HasSuffix(strings.TrimSpace(st), ";") {
					st += ";"
				}
				if st != "" {
					st += " "
				}
				st += "background-color: " + bg
				setAttr(w, "style", st)
			}
		}
		if len(w.Attr) > 0 {
			for c := body.FirstChild; c != nil; c = body.FirstChild {
				body.RemoveChild(c)
				w.AppendChild(c)
			}
			body.AppendChild(w)
		}
	}
	return render(body), blocked, trackers, sheets, darkCanvas
}

var allowedImage = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// finish applies the display contract on bluemonday's output. In compose,
// a cid in keep stays a cid: and every other image source goes.
func finish(safe string, inline map[string]Attachment, keep map[string]bool, loadRemote, compose bool) string {
	root := parseBody(safe)
	walkNodes(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		delAttr(n, "srcset")
		if bg, ok := attr(n, bgAttr); ok && (compose || !loadRemote || !safeRemoteImage(bg)) {
			delAttr(n, bgAttr)
		}
		switch n.DataAtom {
		case atom.Img:
			src, _ := attr(n, "src")
			switch {
			case strings.HasPrefix(src, cidScheme):
				id, _ := url.QueryUnescape(strings.TrimPrefix(src, cidScheme))
				a, ok := inline[id]
				ct := http.DetectContentType(a.Data)
				if compose && keep[id] {
					setAttr(n, "src", "cid:"+id)
				} else if compose || !ok || !allowedImage[ct] {
					delAttr(n, "src")
				} else {
					setAttr(n, "src", "data:"+ct+";base64,"+base64.StdEncoding.EncodeToString(a.Data))
				}
			case !compose && loadRemote && safeRemoteImage(src):
				// validated absolute http(s): keep
			default: // relative, backslash forms, data:, other schemes, remote when not allowed
				delAttr(n, "src")
			}
		case atom.A, atom.Area:
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
// blockedRemote counts remote resources found in the original HTML; trackers
// counts the tracking pixels, dropped whatever loadRemote says.
func Sanitize(src string, inline map[string]Attachment, loadRemote bool) (safe string, blockedRemote, trackers int) {
	prepared, blocked, trackers, sheets, darkCanvas := prepare(src, true)
	p := policy()
	p.AllowAttrs("data-href").OnElements("a", "area")
	p.AllowAttrs("class", "id").Globally() // what the mail's own <style> selects by; prepare cleaned them
	safe = finish(p.Sanitize(prepared), inline, nil, loadRemote, false)
	css, darkCSS := sanitizeCSS(sheets)
	if darkCSS {
		// Keep the marker stable through html.Render when the image proxy rewrites the body.
		safe = `<style data-dark="">` + css + "</style>" + safe
	} else if darkCanvas {
		safe = `<style data-dark-canvas="">` + css + "</style>" + safe
	} else if css != "" {
		safe = "<style>" + css + "</style>" + safe
	}
	return safe, blocked, trackers
}

// SanitizeCompose cleans pasted content in the composer: same whitelist, no
// remote resources. data-godwit survives only as a draft marker: "quote" or "signature".
func SanitizeCompose(src string) string { return SanitizeDraft(src, nil) }

// SanitizeDraft is SanitizeCompose keeping the images whose src is cid: of a
// Content-ID in cids (the draft account's signature images); any other cid,
// data: or remote source is dropped.
func SanitizeDraft(src string, cids map[string]bool) string {
	prepared, _, _, _, _ := prepare(src, false)
	p := policy()
	p.AllowAttrs(marker).Matching(markerValue).Globally()
	return finish(p.Sanitize(prepared), nil, cids, false, true)
}

// cleanStyle rejoins an element's inline declarations trimmed, dropping empty
// ones; read as tokens, so a ; in a string or a url() does not end one.
func cleanStyle(s string) string {
	ts, ok := tokens(s)
	if !ok {
		return s
	}
	var out []string
	for _, d := range splitAt(ts, ";") {
		if d = trimS(d); len(d) > 0 {
			out = append(out, join(d))
		}
	}
	return strings.Join(out, "; ")
}

// isDarkColor reports whether the first colour in a background value is dark
// and opaque enough to be the mail's canvas. One it cannot read is not: guessing
// black would keep a light mail uninverted.
func isDarkColor(val string) bool {
	ts, ok := tokens(val)
	if !ok {
		return false
	}
	dark := func(c []float64) bool { return c[3] >= 0.5 && 0.299*c[0]+0.587*c[1]+0.114*c[2] < 128 }
	for i, t := range ts {
		switch {
		case t.Type == scanner.TokenIdent && slices.Contains(darkNames, strings.ToLower(t.Value)):
			return true
		case t.Type == scanner.TokenHash:
			h := t.Value[1:]
			if len(h) == 3 || len(h) == 4 { // #abc is #aabbcc
				var b strings.Builder
				for i := range len(h) {
					b.WriteString(h[i:i+1] + h[i:i+1])
				}
				h = b.String()
			}
			v, err := strconv.ParseUint(h, 16, 32)
			if err != nil || len(h) != 6 && len(h) != 8 {
				return false
			}
			a := uint64(255)
			if len(h) == 8 {
				a, v = v&0xff, v>>8
			}
			return dark([]float64{float64(v >> 16), float64(v >> 8 & 0xff), float64(v & 0xff), float64(a) / 255})
		case t.Type == scanner.TokenFunction && (strings.EqualFold(t.Value, "rgb(") || strings.EqualFold(t.Value, "rgba(")):
			c, n := []float64{0, 0, 0, 1}, 0
			for _, t := range ts[i+1:] {
				switch {
				case isChar(t, ")"):
					return (n == 3 || n == 4) && dark(c)
				case t.Type == scanner.TokenNumber && n < 4:
					f, err := strconv.ParseFloat(t.Value, 64)
					if err != nil {
						return false
					}
					c[n], n = f, n+1
				case t.Type != scanner.TokenS && !isChar(t, ",/"):
					return false // a percentage: not read
				}
			}
			return false
		}
	}
	return false
}

var darkNames = []string{"black", "navy", "darkblue", "midnightblue", "darkgreen", "darkslategray", "maroon"}

// elementBackground is n's background as written: bgcolor, else its style's
// background-color or background (a shorthand, which isDarkColor reads the colour of).
func elementBackground(n *html.Node) string {
	if bg, ok := attr(n, "bgcolor"); ok && bg != "" {
		return bg
	}
	decls := styleDecls(n)
	if c := decls["background-color"]; c != "" {
		return c
	}
	return decls["background"]
}

// topLevel reports whether n is a child of <body>, or a grandchild through one
// wrapping element (a table's rows and cells not counted): a dark footer or banner
// deeper in does not make the canvas dark.
func topLevel(n *html.Node) bool {
	wrappers := 0
	for p := n.Parent; p != nil; p = p.Parent {
		switch p.DataAtom {
		case atom.Body:
			return true
		case atom.Tbody, atom.Thead, atom.Tfoot, atom.Tr, atom.Td, atom.Th:
		default:
			if wrappers++; wrappers > 1 {
				return false
			}
		}
	}
	return false
}

// isContainer reports whether n spans the page's width, as a mail's canvas does.
func isContainer(n *html.Node) bool {
	if w, ok := attr(n, "width"); ok && (w == "100%" || w == "100") {
		return true
	}
	decls := styleDecls(n)
	if w := decls["width"]; w == "100%" || strings.HasPrefix(w, "100vw") {
		return true
	}
	if mw := decls["min-width"]; mw == "100%" {
		return true
	}
	return false
}

// ReplaceImages returns src with every <img> source s replaced by f(s); an
// empty result drops the source. It does not sanitize.
func ReplaceImages(src string, f func(s string) string) string {
	root := parseBody(src)
	walkNodes(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.DataAtom != atom.Img {
			return
		}
		s, _ := attr(n, "src")
		if s = f(s); s == "" {
			delAttr(n, "src")
		} else {
			setAttr(n, "src", s)
		}
	})
	return render(root)
}

// RemoteImages are the http(s) image sources of a sanitized body (kept by
// Sanitize with loadRemote): its <img> sources and CSS background images, in
// order, each once.
func RemoteImages(safe string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if safeRemoteImage(u) && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	walkNodes(parseBody(safe), func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.DataAtom == atom.Img {
			s, _ := attr(n, "src")
			add(s)
		}
		bg, _ := attr(n, bgAttr)
		add(bg)
	})
	return out
}

// CIDs are the Content-IDs src's images refer to as cid:, in order.
func CIDs(src string) []string {
	var out []string
	ReplaceImages(src, func(s string) string {
		if len(s) >= 4 && strings.EqualFold(s[:4], "cid:") {
			out = append(out, cleanID(s[4:]))
		}
		return s
	})
	return out
}

// Draft markers: NewDraft puts the quoted mail in <blockquote data-godwit="quote">
// and the signature in <div data-godwit="signature">, at the top level.
const marker = "data-godwit"

var markerValue = regexp.MustCompile(`^(quote|signature)$`)

func parseBody(src string) *html.Node {
	ctx := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}
	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}
	nodes, _ := html.ParseFragment(strings.NewReader(src), ctx)
	for _, n := range nodes {
		root.AppendChild(n)
	}
	return root
}

// StripMarkers removes every data-godwit attribute, in any letter case (the
// parser lowercases names), so marked HTML from a model or another mail
// becomes ordinary content.
func StripMarkers(src string) string {
	root := parseBody(src)
	walkNodes(root, func(n *html.Node) { delAttr(n, marker) })
	return render(root)
}

// SplitDraft splits a sanitized draft body into its editable part, its quote
// and its signature: the first top-level elements marked "quote" and
// "signature"; everything else is editable. JoinDraft puts them back together.
func SplitDraft(src string) (editable, quote, signature string) {
	var ed strings.Builder
	for n := parseBody(src).FirstChild; n != nil; n = n.NextSibling {
		var sb strings.Builder
		_ = html.Render(&sb, n)
		switch v, _ := attr(n, marker); {
		case n.Type == html.ElementNode && v == "quote" && quote == "":
			quote = sb.String()
		case n.Type == html.ElementNode && v == "signature" && signature == "":
			signature = sb.String()
		default:
			ed.WriteString(sb.String())
		}
	}
	return ed.String(), quote, signature
}

// Blank reports whether HTML has no content at all: only whitespace and the
// editor's empty placeholders (p, div, br). An image, a rule or a table is content.
func Blank(src string) bool {
	blank := true
	walkNodes(parseBody(src), func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			blank = blank && strings.TrimSpace(n.Data) == ""
		case html.ElementNode:
			blank = blank && (n.DataAtom == atom.P || n.DataAtom == atom.Div || n.DataAtom == atom.Br)
		}
	})
	return blank
}

// JoinDraft is the draft body of its parts, as SplitDraft returns them.
func JoinDraft(editable, quote, signature string) string { return editable + quote + signature }

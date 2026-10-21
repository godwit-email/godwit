package mail

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// The recognizers here read how an HTML mail looks, not its words, so they
// work in any language: the code set apart in its own block, large, bold,
// letter-spaced or monospaced; the one link drawn as a button. Much other
// mail looks like that too (an order number in bold, a "Shop now" button),
// so callers use them only on mail already known to carry a code or a
// sign-in link (the classifier's Codes label), and only where the
// lexicon-based SignInCode and ActionLink found nothing.

// EmphasizedCode is the one code an HTML body sets apart: alone in its
// block, shown large, bold, letter-spaced or monospaced. "" for none or
// several.
func EmphasizedCode(subject, text, htmlBody string) string {
	if htmlBody == "" || replySubject.MatchString(simplified(subject)) {
		return ""
	}
	_, doc := unquoted(text, htmlBody)
	if doc == nil {
		return ""
	}
	code := ""
	conflict := false
	walkBody(doc, func(n *html.Node) bool {
		if n.Data == "a" { // a code is copied, not followed
			return false
		}
		l := label(n)
		v, ok := codeShaped(l)
		if !ok {
			return true // its children may hold one
		}
		if p := n.Parent; p != nil && p.Type == html.ElementNode && p.Data != "body" && p.Data != "html" && label(p) == l {
			return true // not yet its own block: the code is all its parent says too
		}
		if emphasized(n) {
			if code != "" && code != v {
				conflict = true
			}
			code = v
		}
		return false // this block is the code; nothing inside it is another
	})
	if conflict {
		return ""
	}
	return code
}

// PrimaryButton is the one https link an HTML body draws as a button, and
// what it does: confirming an address or setting a password when the mail
// speaks of that, else signing in. "" for none, several, or a security
// alert, whose button reviews activity.
func PrimaryButton(subject, text, htmlBody string) (link, kind string) {
	subject = simplified(subject)
	if htmlBody == "" || replySubject.MatchString(subject) {
		return "", ""
	}
	plain, doc := unquoted(text, htmlBody)
	if doc == nil {
		return "", ""
	}
	body := simplified(plain)
	if linkAlert.MatchString(subject) || linkAlert.MatchString(body) {
		return "", ""
	}
	conflict := false
	walkBody(doc, func(n *html.Node) bool {
		if n.Data != "a" {
			return true
		}
		href, _ := attr(n, "href")
		u, err := url.Parse(strings.TrimSpace(href))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || !buttonLike(n) {
			return false
		}
		if link != "" && link != u.String() {
			conflict = true
		}
		link = u.String()
		return false
	})
	switch {
	case link == "" || conflict:
		return "", ""
	case confirmContext.MatchString(subject) || confirmContext.MatchString(body):
		return link, LinkConfirm
	case resetContext.MatchString(subject) || resetContext.MatchString(body):
		return link, LinkReset
	}
	return link, LinkSignIn
}

// walkBody calls f on each element of the document's body, in order, and
// goes inside one only while f says to. Links' insides, and what a reader
// never sees, are skipped.
func walkBody(n *html.Node, f func(*html.Node) bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "head", "style", "script", "title", "template":
			continue
		case "html", "body":
			walkBody(c, f)
			continue
		}
		if hidden(c) {
			continue
		}
		if f(c) && c.Data != "a" {
			walkBody(c, f)
		}
	}
}

// codeShaped reads a block's whole text as a code: four to eight digits, or
// four to ten letters and digits with a digit among them, written whole,
// in groups of three ("482 913"), or a character at a time ("4 8 2 9 1 3",
// as letter boxes render). v is the code without its spaces.
func codeShaped(s string) (v string, ok bool) {
	groups := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '-' })
	if len(groups) == 0 {
		return "", false
	}
	if len(groups) > 1 {
		size := len(groups[0])
		for _, g := range groups {
			if len(g) != size || size != 1 && size != 3 {
				return "", false
			}
		}
	}
	v = strings.Join(groups, "")
	digits, letters := 0, 0
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letters++
		default:
			return "", false
		}
	}
	if letters == 0 {
		return v, len(v) >= 4 && len(v) <= 8
	}
	return v, digits > 0 && len(v) >= 4 && len(v) <= 10
}

// emphasized: n, or an element inside it, is styled apart from body text.
func emphasized(n *html.Node) bool {
	if emphasizing(n) {
		return true
	}
	found := false
	walkNodes(n, func(c *html.Node) {
		found = found || c.Type == html.ElementNode && emphasizing(c)
	})
	return found
}

// emphasizing: an element's own tag or inline style sets its text apart.
func emphasizing(n *html.Node) bool {
	switch n.Data {
	case "b", "strong", "h1", "h2", "h3", "code", "pre", "kbd", "samp", "tt", "big":
		return true
	case "font":
		if size, ok := attr(n, "size"); ok {
			if v, err := strconv.Atoi(strings.TrimPrefix(size, "+")); err == nil && (v >= 5 || strings.HasPrefix(size, "+") && v >= 2) {
				return true
			}
		}
	}
	for prop, value := range styleDecls(n) {
		switch prop {
		case "font-size":
			if large(value) {
				return true
			}
		case "font-weight":
			if value == "bold" || value == "bolder" {
				return true
			}
			if w, err := strconv.Atoi(value); err == nil && w >= 600 {
				return true
			}
		case "letter-spacing":
			if v, ok := length(value); ok && v > 0 {
				return true
			}
		case "font-family":
			for _, mono := range []string{"mono", "courier", "menlo", "consolas", "monaco"} {
				if strings.Contains(value, mono) {
					return true
				}
			}
		}
	}
	return false
}

// large: a font size well above body text's 14 to 16px.
func large(value string) bool {
	switch value {
	case "large", "x-large", "xx-large", "xxx-large", "larger":
		return true
	}
	v, ok := length(value)
	switch {
	case !ok:
		return false
	case strings.HasSuffix(value, "pt"):
		return v >= 15
	case strings.HasSuffix(value, "em"), strings.HasSuffix(value, "rem"):
		return v >= 1.25
	case strings.HasSuffix(value, "%"):
		return v >= 125
	}
	return v >= 20 // px, or a bare number as Outlook writes it
}

// length is a CSS length's number, whatever its unit.
func length(value string) (float64, bool) {
	num := strings.TrimRightFunc(value, unicode.IsLetter)
	num = strings.TrimSuffix(num, "%")
	v, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	return v, err == nil
}

// buttonLike: a link drawn as a button, by itself or by the cells around it
// that hold nothing else: a background color, a button's class or role.
func buttonLike(a *html.Node) bool {
	l := label(a)
	if l == "" && !hasImage(a) {
		return false
	}
	for n := a; n != nil && n.Type == html.ElementNode; n = n.Parent {
		if n != a && label(n) != l {
			break
		}
		if role, _ := attr(n, "role"); role == "button" {
			return true
		}
		if class, _ := attr(n, "class"); class != "" {
			for token := range strings.FieldsSeq(strings.ToLower(class)) {
				if strings.Contains(token, "button") || strings.Contains(token, "btn") || strings.Contains(token, "cta") {
					return true
				}
			}
		}
		if bg, ok := attr(n, "bgcolor"); ok && filled(strings.ToLower(bg)) {
			return true
		}
		decls := styleDecls(n)
		if filled(decls["background-color"]) || filled(decls["background"]) {
			return true
		}
	}
	return false
}

// filled: a background that paints something other than the page's white.
func filled(value string) bool {
	switch strings.TrimSpace(strings.TrimSuffix(value, "!important")) {
	case "", "none", "transparent", "inherit", "initial", "unset", "white", "#fff", "#ffffff", "#ffffffff", "rgb(255,255,255)", "rgb(255, 255, 255)":
		return false
	}
	return !strings.Contains(value, "url(") // an image alone is decoration more often than a button
}

func hasImage(n *html.Node) bool {
	found := false
	walkNodes(n, func(c *html.Node) { found = found || c.Type == html.ElementNode && c.Data == "img" })
	return found
}

// hidden: an element mail hides from the reader, as preheaders are.
func hidden(n *html.Node) bool {
	if _, ok := attr(n, "hidden"); ok {
		return true
	}
	decls := styleDecls(n)
	return decls["display"] == "none" || decls["visibility"] == "hidden" || decls["mso-hide"] == "all" || transparent(decls["opacity"]) || decls["max-height"] == "0" || decls["max-height"] == "0px" || decls["font-size"] == "0" || decls["font-size"] == "0px"
}

// transparent: an opacity that shows nothing ("0", "0.0", "0%").
func transparent(opacity string) bool {
	v, ok := length(opacity)
	return ok && v == 0
}

// styleDecls are an element's inline declarations, lowercased.
func styleDecls(n *html.Node) map[string]string {
	style, _ := attr(n, "style")
	if style == "" {
		return nil
	}
	out := map[string]string{}
	for decl := range strings.SplitSeq(strings.ToLower(style), ";") {
		prop, value, ok := strings.Cut(decl, ":")
		if ok {
			out[strings.TrimSpace(prop)] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "!important"))
		}
	}
	return out
}

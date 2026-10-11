package webui

import (
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/godwit-email/godwit/core/mail"
)

// bodyHTML is the editable plain text as the draft body: one <p> per line,
// escaped; an empty line is <p></p>. A space next to another space, or at
// either end of a line, is written &nbsp; so HTML keeps it; the text's own
// U+00A0 is wrapped as <span>&nbsp;</span> so bodyText can tell the two
// apart. CRLF becomes LF (a browser textarea's value only has LF).
func bodyHTML(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		b.WriteString("<p>")
		rs := []rune(line)
		for i, r := range rs {
			if r == '\u00a0' {
				b.WriteString("<span>&nbsp;</span>")
			} else if r == ' ' && (i == 0 || i == len(rs)-1 || rs[i-1] == ' ' || rs[i+1] == ' ') {
				b.WriteString("&nbsp;")
			} else {
				b.WriteString(html.EscapeString(string(r)))
			}
		}
		b.WriteString("</p>")
	}
	return b.String()
}

// bodyText is the inverse of bodyHTML. Any other markup (a body written by the
// old compose page) is converted per top-level element with mail.HTMLToText.
func bodyText(editable string) string {
	ctx := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(strings.NewReader(editable), ctx)
	if err != nil {
		return mail.HTMLToText(editable)
	}
	var lines []string
	for _, n := range nodes {
		switch {
		case n.Type == xhtml.TextNode && strings.TrimSpace(n.Data) == "":
		case n.Type == xhtml.ElementNode && n.Data == "p" && onlyText(n):
			lines = append(lines, textOf(n))
		default:
			var sb strings.Builder
			_ = xhtml.Render(&sb, n)
			lines = append(lines, strings.TrimRight(mail.HTMLToText(sb.String()), "\n"))
		}
	}
	return strings.Join(lines, "\n")
}

// onlyText reports whether n has only text children and bodyHTML's
// <span>text</span> wrappers.
func onlyText(n *xhtml.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == xhtml.TextNode:
		case c.Type == xhtml.ElementNode && c.Data == "span" && c.FirstChild != nil && c.FirstChild.Type == xhtml.TextNode && c.FirstChild.NextSibling == nil:
		default:
			return false
		}
	}
	return true
}

// textOf is n's text: a &nbsp; is a preserved space, except inside a span,
// where it is the text's own U+00A0.
func textOf(n *xhtml.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode {
			sb.WriteString(c.FirstChild.Data)
		} else {
			sb.WriteString(strings.ReplaceAll(c.Data, "\u00a0", " "))
		}
	}
	return sb.String()
}

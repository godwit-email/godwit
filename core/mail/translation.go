package mail

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// MapHTMLText replaces visible text nodes, leaving markup and attributes intact.
// The callback's result is text: html.Render escapes it, including any model markup.
func MapHTMLText(src string, replace func(string) string) string {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(src), body)
	if err != nil {
		return src
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.DataAtom == atom.Style || n.DataAtom == atom.Script {
			return
		}
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
			n.Data = replace(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var out strings.Builder
	for _, n := range nodes {
		walk(n)
		if err := html.Render(&out, n); err != nil {
			return src
		}
	}
	return out.String()
}

package richtext

import (
	"fmt"
	"html"
	"strings"
)

// ToHTML serializes d as stable HTML that the From functions read back:
// FromComposeHTML(ToHTML(d)) == d for compose content.
func ToHTML(d Doc) string {
	w := &writer{}
	for _, b := range d.Blocks {
		w.container(b)
		switch b.Kind {
		case Rule:
			w.WriteString("<hr>")
		case ImageBlock:
			im := b.Image
			fmt.Fprintf(w, `<img src="%s" alt="%s"`, html.EscapeString(im.Src), html.EscapeString(im.Alt))
			if im.Width > 0 {
				fmt.Fprintf(w, ` width="%d"`, im.Width)
			}
			if im.Height > 0 {
				fmt.Fprintf(w, ` height="%d"`, im.Height)
			}
			w.WriteString(">")
		case Table:
			w.table(b)
		default:
			tag := blockTag(b)
			w.WriteString("<" + tag + ">")
			if len(b.Inlines) == 0 {
				w.WriteString("<br>")
			} else {
				w.inlines(b.Inlines)
				if t := b.Inlines[len(b.Inlines)-1].Text; b.Kind != Pre && strings.HasSuffix(t, "\n") {
					w.WriteString("<br>") // holds the last line open, as FromComposeHTML reads it
				}
			}
			if b.Kind != ListItem { // the item stays open for a nested list
				w.WriteString("</" + tag + ">")
			}
		}
	}
	w.container(Block{})
	return w.String()
}

func blockTag(b Block) string {
	switch b.Kind {
	case Heading:
		return fmt.Sprintf("h%d", min(max(b.Level, 1), 6))
	case Pre:
		return "pre"
	case ListItem:
		return "li"
	}
	return "p"
}

type list struct {
	ordered, li bool
}

type writer struct {
	strings.Builder
	quotes int
	lists  []list
}

// container closes and opens the blockquotes and lists around b.
func (w *writer) container(b Block) {
	depth := 0
	if b.Kind == ListItem {
		depth = max(b.Level, 1)
	}
	for len(w.lists) > depth || len(w.lists) == depth && depth > 0 && w.lists[depth-1].ordered != b.Ordered {
		w.closeList()
	}
	if len(w.lists) == depth && depth > 0 && w.lists[depth-1].li {
		w.WriteString("</li>")
	}
	quotes := 0
	if b.Kind == Quote {
		quotes = max(b.Level, 1)
	}
	for ; w.quotes > quotes; w.quotes-- {
		w.WriteString("</blockquote>")
	}
	for ; w.quotes < quotes; w.quotes++ {
		w.WriteString("<blockquote>")
	}
	for len(w.lists) < depth {
		w.lists = append(w.lists, list{ordered: b.Ordered})
		w.WriteString(listTag(b.Ordered, ""))
	}
	if depth > 0 {
		w.lists[depth-1].li = true
	}
}

func (w *writer) closeList() {
	l := w.lists[len(w.lists)-1]
	if l.li {
		w.WriteString("</li>")
	}
	w.WriteString(listTag(l.ordered, "/"))
	w.lists = w.lists[:len(w.lists)-1]
}

func listTag(ordered bool, slash string) string {
	if ordered {
		return "<" + slash + "ol>"
	}
	return "<" + slash + "ul>"
}

func (w *writer) table(b Block) {
	w.WriteString("<table>")
	for i, r := range b.Rows {
		w.WriteString("<tr>")
		tag := "td"
		if i == 0 && b.Header {
			tag = "th"
		}
		for _, c := range r {
			w.WriteString("<" + tag + ">")
			w.inlines(c.Inlines)
			w.WriteString("</" + tag + ">")
		}
		w.WriteString("</tr>")
	}
	w.WriteString("</table>")
}

// inlines writes runs, each in a fixed tag order.
func (w *writer) inlines(ins []Inline) {
	for _, in := range ins {
		var open, close []string
		tag := func(o, c string, on bool) {
			if on {
				open, close = append(open, o), append([]string{c}, close...)
			}
		}
		tag(`<a href="`+html.EscapeString(in.Link)+`">`, "</a>", in.Link != "")
		tag(`<span style="color: `+html.EscapeString(in.Color)+`">`, "</span>", in.Color != "")
		var font []string
		if in.FontSize != "" {
			font = append(font, "font-size: "+in.FontSize)
		}
		if in.FontFamily != "" {
			font = append(font, "font-family: "+in.FontFamily)
		}
		tag(`<span style="`+html.EscapeString(strings.Join(font, "; "))+`">`, "</span>", font != nil)
		tag("<b>", "</b>", in.Bold)
		tag("<i>", "</i>", in.Italic)
		tag("<u>", "</u>", in.Underline)
		tag("<s>", "</s>", in.Strike)
		tag("<code>", "</code>", in.Code)
		w.WriteString(strings.Join(open, ""))
		w.WriteString(strings.ReplaceAll(html.EscapeString(in.Text), "\n", "<br>"))
		w.WriteString(strings.Join(close, ""))
	}
}

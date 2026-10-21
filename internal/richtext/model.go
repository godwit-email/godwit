// Package richtext is Godwit's rich text model: mail bodies, compose content
// and Markdown become a flat list of blocks that the native UI draws,
// and compose content goes back to HTML.
package richtext

import "strings"

// Doc is a document: blocks from top to bottom.
type Doc struct{ Blocks []Block }

// BlockKind is what a block is.
type BlockKind int

const (
	Paragraph BlockKind = iota
	Heading             // Level 1–6
	Quote               // Level is the quote depth, from 1
	ListItem            // Level is the list depth, from 1; Ordered for <ol>
	Pre
	Rule
	Table // Rows; Header when the first row is a header
	ImageBlock
)

// Block is one block. Which fields count depends on Kind.
type Block struct {
	Kind    BlockKind
	Level   int
	Ordered bool
	Inlines []Inline
	Rows    [][]Cell
	Header  bool
	Image   *Image
}

// Inline is a run of text in one style. "\n" in Text is a line break.
type Inline struct {
	Text                                  string
	Bold, Italic, Code, Strike, Underline bool
	Link                                  string
	Color                                 string // a CSS color, from the sanitized inline style
	// FontSize and FontFamily are CSS values, from compose content's inline style.
	FontSize, FontFamily string
}

// Cell is a table cell.
type Cell struct{ Inlines []Inline }

// Image is an image block. Src is a data: URL (PNG, JPEG, GIF, WebP) or an
// http(s) URL; Width and Height are the HTML attributes, 0 when unknown.
type Image struct {
	Src, Alt      string
	Width, Height int
}

// Pos is a place in a document: a rune of a block's text (its inlines' text
// together).
type Pos struct{ Block, Rune int }

// Limits bound what a conversion produces: element nesting (deeper
// subtrees become plain text), blocks, cells of one table and inlines of
// one block.
type Limits struct{ MaxDepth, MaxBlocks, MaxCells, MaxInlines int }

// DefaultLimits are the budget for one mail (global constraints).
var DefaultLimits = Limits{20, 5000, 2000, 20000}

func inlineText(ins []Inline) string {
	var sb strings.Builder
	for _, in := range ins {
		sb.WriteString(in.Text)
	}
	return sb.String()
}

// PlainText is the document's text: a line per block, table cells
// separated by tabs, images by their alt text.
func (d Doc) PlainText() string {
	lines := make([]string, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		switch b.Kind {
		case Table:
			rows := make([]string, len(b.Rows))
			for i, r := range b.Rows {
				cs := make([]string, len(r))
				for j, c := range r {
					cs[j] = inlineText(c.Inlines)
				}
				rows[i] = strings.Join(cs, "\t")
			}
			lines = append(lines, strings.Join(rows, "\n"))
		case ImageBlock:
			lines = append(lines, b.Image.Alt)
		default:
			lines = append(lines, inlineText(b.Inlines))
		}
	}
	return strings.Join(lines, "\n")
}

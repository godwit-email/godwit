package editor

import (
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// line is a block laid out: at x, y in the element, its text from start.
type line struct {
	x, y  float32
	l     *ui.TextLayout
	start int
}

func (e *Editor) indexAt(p ui.Point) int {
	e.layout()
	i := len(e.lines) - 1
	for k, ln := range e.lines {
		if _, h := ln.l.Size(); p.Y < ln.y+h+e.gap() {
			i = k
			break
		}
	}
	ln := e.lines[i]
	return ln.start + ln.l.IndexAt(ui.Point{X: p.X - ln.x, Y: p.Y - ln.y})
}

// lineOf is the block offset i is in.
func (e *Editor) lineOf(i int) int {
	e.layout()
	k := 0
	for j, ln := range e.lines {
		if ln.start <= i {
			k = j
		}
	}
	return k
}

func (e *Editor) caretRect(i int) ui.Rect {
	ln := e.lines[e.lineOf(i)]
	c := ln.l.Caret(i - ln.start)
	c.X += ln.x
	c.Y += ln.y
	return c
}

// View shows the editor.
func (e *Editor) View(c *ui.Context, o Options) ui.Element {
	t := theme.Of(c)
	if o.FontSize == 0 {
		o.FontSize = t.BodyFontSize
	}
	unit := c.Theme().Space(1)
	if o.FontSize != e.o.FontSize || unit != e.unit || t.Dark != e.tok.Dark || !slices.Equal(o.Highlights, e.o.Highlights) {
		e.dirty = true
	}
	e.o, e.tok, e.svc, e.unit = o, t, c.Services(), unit
	if o.ReadOnly {
		e.cancelComposition()
	}
	e.layout()
	el := ui.Box(c).HandleTextInput(e).HandleInput(e.handle).Draw(e.draw).Role(ui.RoleTextField).Label(o.Label).
		MinHeight(e.height)
	e.focused = el.Focused()
	return el
}

func (e *Editor) gap() float32 { return 2 * e.unit }

// indent is where a block's text starts: list items leave room for their
// marker, quotes for their bars.
func (e *Editor) indent(b Block) float32 {
	switch b.Kind {
	case richtext.ListItem:
		return 6 * e.unit * float32(max(b.Level, 1))
	case richtext.Quote:
		return 4 * e.unit * float32(max(b.Level, 1))
	}
	return 0
}

// layout shapes the blocks again after a change.
func (e *Editor) layout() {
	if !e.dirty && len(e.lines) == len(e.doc.Blocks) {
		return
	}
	if e.o.FontSize == 0 {
		e.o.FontSize = theme.Light().BodyFontSize
	}
	e.lines = e.lines[:0]
	var y float32
	off := 0
	for i, b := range e.doc.Blocks {
		x := e.indent(b)
		w := float32(0)
		if e.width > 0 {
			w = max(e.width-x, 1)
		}
		l := ui.ShapeRichText(e.spans(i, b), ui.Font{Size: e.o.FontSize}, w)
		_, h := l.Size()
		e.lines = append(e.lines, line{x: x, y: y, l: l, start: off})
		y += h + e.gap()
		off += ui.UTF16Len(blockText(b)) + 1
	}
	e.height = y - e.gap()
	e.dirty = false
}

// decoration is a highlight, or the composition's underline, in a block.
type decoration struct {
	from, to  Pos
	bg, fg    ui.Color
	strike    bool
	underline bool
}

// spans are block i's styled runs, cut where highlights start and end.
func (e *Editor) spans(i int, b Block) []ui.Span {
	var decos []decoration
	for _, h := range e.o.Highlights {
		from, to := h.From, h.To
		if less(to, from) {
			from, to = to, from
		}
		decos = append(decos, decoration{from: from, to: to, bg: h.Bg, fg: h.Fg, strike: h.Strike})
	}
	if e.mark != nil {
		decos = append(decos, decoration{from: posAt(e.doc, e.mark.Start), to: posAt(e.doc, e.mark.End), underline: true})
	}
	cuts := []int{}
	for _, d := range decos {
		for _, p := range []Pos{d.from, d.to} {
			if p.Block == i {
				cuts = append(cuts, p.Rune)
			}
		}
	}
	var out []ui.Span
	r := 0
	for _, in := range b.Inlines {
		rs := []rune(in.Text)
		at := 0
		for at < len(rs) {
			end := len(rs)
			for _, c := range cuts {
				if c-r > at && c-r < end {
					end = c - r
				}
			}
			from, to := Pos{Block: i, Rune: r + at}, Pos{Block: i, Rune: r + end}
			for _, sp := range richtext.Autospace(string(rs[at:end]), e.o.FontSize) {
				sp.Italic, sp.Underline = in.Italic, in.Link != ""
				if in.Bold {
					sp.Weight = theme.BoldWeight
				}
				for _, d := range decos {
					if less(from, d.from) || less(d.to, to) {
						continue
					}
					sp.Strikethrough = sp.Strikethrough || d.strike
					sp.Underline = sp.Underline || d.underline
					if d.bg.A > 0 {
						sp.Background = d.bg
					}
					if d.fg.A > 0 {
						sp.Color = d.fg
					}
				}
				out = append(out, sp)
			}
			at = end
		}
		r += len(rs)
	}
	return out
}

func (e *Editor) draw(p *ui.Painter, r ui.Rect) {
	if r.W != e.width {
		e.width, e.dirty = r.W, true
	}
	h := e.height
	e.layout()
	if e.height != h {
		e.svc.Invalidate() // the element's height follows the text
	}
	t := e.tok
	if e.o.Placeholder != "" && e.mark == nil && len(e.doc.Blocks) == 1 && runeLen(e.doc.Blocks[0]) == 0 {
		p.Text(r.X, r.Y, e.o.Placeholder, e.o.FontSize, t.Muted)
	}
	sel := e.sel.Range
	var numbers []int // the item numbers of the open lists, by level
	for i, ln := range e.lines {
		b := e.doc.Blocks[i]
		x, y := r.X+ln.x, r.Y+ln.y
		_, lh := ln.l.Size()
		switch b.Kind {
		case richtext.ListItem:
			lv := max(b.Level, 1)
			numbers = numbers[:min(len(numbers), lv)]
			for len(numbers) < lv {
				numbers = append(numbers, 0)
			}
			numbers[lv-1]++
			marker := "•"
			if b.Ordered {
				marker = strconv.Itoa(numbers[lv-1]) + "."
			}
			p.Text(x-4*e.unit, y, marker, e.o.FontSize, t.Muted)
		case richtext.Quote:
			numbers = numbers[:0]
			for k := range max(b.Level, 1) {
				p.Fill(ui.Rect{X: r.X + 4*e.unit*float32(k), Y: y, W: theme.QuoteBar, H: lh}, t.Border, 0)
			}
		default:
			numbers = numbers[:0]
		}
		n := ui.UTF16Len(blockText(b))
		if from, to := max(sel.Start-ln.start, 0), min(sel.End-ln.start, n); sel.Start != sel.End && from < to {
			for _, s := range ln.l.SelectionRects(ui.TextInputRange{Start: from, End: to}) {
				s.X += x
				s.Y += y
				p.Fill(s, p.Theme().Selection, 0)
			}
		}
		p.TextLayout(ln.l, x, y, t.Foreground)
	}
	if e.focused && !e.o.ReadOnly && sel.Start == sel.End {
		c := e.caretRect(sel.Start)
		c.X += r.X
		c.Y += r.Y
		p.Fill(c, t.Foreground, 0)
	}
}

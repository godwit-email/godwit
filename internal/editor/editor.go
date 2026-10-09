// Package editor is Godwit's native rich text editor for compose content
// (paragraphs, bold, italic, links, lists and quotes): an application-owned
// ui.TextInputClient over a richtext.Doc, with input methods, selection,
// keyboard and pointer editing, sanitized paste and undo.
package editor

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// Options are how View shows the editor. FontSize defaults to the body font
// size. PasteHTML returns the clipboard's HTML, "" for none; OnReadOnlyInput
// is told of each change ignored while ReadOnly.
type Options struct {
	Label, Placeholder string
	FontSize           float32
	ReadOnly           bool
	Highlights         []Highlight
	PasteHTML          func() string
	OnReadOnlyInput    func()
}

// Highlight marks the text from From to To, as a revision's insertions and
// deletions: Bg behind it, Fg (when set) for it, struck through on Strike.
type Highlight struct {
	From, To richtext.Pos
	Bg, Fg   ui.Color
	Strike   bool
}

// Editor is the editor's state; keep one per document across frames. All
// offsets of its ui.TextInputClient methods are UTF-16 code units of the
// document's text with a newline between blocks, the composition included.
type Editor struct {
	// Now is the clock that ends a step of typing after a pause; time.Now
	// when nil.
	Now func() time.Time

	doc     Doc // the composition is in it, at mark
	sel     ui.TextInputSelection
	mark    *ui.TextInputRange
	pending *Inline // the style of the next text typed, after Cmd+B at a caret
	hist    history
	version uint64

	o        Options
	tok      theme.Tokens
	svc      ui.Services
	unit     float32 // c.Theme().Space(1)
	focused  bool
	dragging bool

	width  float32 // the width last drawn at
	lines  []line  // a laid out block each
	height float32
	dirty  bool
}

var _ ui.TextInputClient = (*Editor)(nil)

// line is a block laid out: at x, y in the element, its text from start.
type line struct {
	x, y  float32
	l     *ui.TextLayout
	start int
}

// New is an editor of d, with the caret at its start.
func New(d richtext.Doc) *Editor { return &Editor{doc: own(d), dirty: true} }

// own is a copy of d that shares nothing with it, with a block at least.
func own(d Doc) Doc {
	out := Doc{Blocks: slices.Clone(d.Blocks)}
	for i := range out.Blocks {
		out.Blocks[i].Inlines = slices.Clone(out.Blocks[i].Inlines)
	}
	if len(out.Blocks) == 0 {
		out.Blocks = []Block{{}}
	}
	return out
}

func caretAt(i int) ui.TextInputSelection {
	return ui.TextInputSelection{Range: ui.TextInputRange{Start: i, End: i}}
}

func (e *Editor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Editor) total() int { return ui.UTF16Len(plain(e.doc)) }

// committed is the document without the composition, and the selection in
// it.
func (e *Editor) committed() (Doc, ui.TextInputSelection) {
	if e.mark == nil {
		return e.doc, e.sel
	}
	d, _ := replace(e.doc, posAt(e.doc, e.mark.Start), posAt(e.doc, e.mark.End), Doc{})
	return d, caretAt(e.mark.Start)
}

// Doc is the document, without text an input method is composing.
func (e *Editor) Doc() richtext.Doc { d, _ := e.committed(); return own(d) }

// HTML is Doc as compose HTML.
func (e *Editor) HTML() string { d, _ := e.committed(); return richtext.ToHTML(d) }

// DocumentSelection is the selection in Doc, start first.
func (e *Editor) DocumentSelection() (start, end richtext.Pos) {
	d, s := e.committed()
	return posAt(d, s.Range.Start), posAt(d, s.Range.End)
}

// Version counts the changes to Doc.
func (e *Editor) Version() uint64 { return e.version }

// Composing reports whether an input method is composing text.
func (e *Editor) Composing() bool { return e.mark != nil }

// Replace makes d the document, as one step to undo, read-only or not.
func (e *Editor) Replace(d richtext.Doc) {
	e.change(false, -1, func() {
		e.doc, e.mark = own(d), nil
		e.sel = caretAt(min(e.sel.Range.Start, e.total()))
	})
}

// change makes a change to the document with f as one step to undo, or as
// part of the typing step before it when typing at that step's caret.
func (e *Editor) change(typing bool, at int, f func()) {
	d, s := e.committed()
	e.hist.push(snapshot{d, s}, typing, e.now(), at)
	f()
	e.version++
	e.dirty = true
	if typing {
		e.hist.typed(e.sel.Caret())
	}
}

// writable reports whether the user may change the document, telling
// OnReadOnlyInput when not.
func (e *Editor) writable() bool {
	if e.o.ReadOnly {
		if e.o.OnReadOnlyInput != nil {
			e.o.OnReadOnlyInput()
		}
		return false
	}
	return true
}

// put replaces the range r with frag, putting the caret after it.
func (e *Editor) put(r ui.TextInputRange, frag Doc) {
	d, end := replace(e.doc, posAt(e.doc, r.Start), posAt(e.doc, r.End), frag)
	e.doc, e.dirty = d, true
	e.sel = caretAt(offset(d, end))
}

// typed is s as text typed at offset at.
func (e *Editor) typed(at int, s string) Doc {
	p := posAt(e.doc, at)
	st := styleAt(e.doc, p)
	if e.pending != nil {
		st = *e.pending
	}
	return textFrag(s, st, e.doc.Blocks[p.Block])
}

func (e *Editor) target(r *ui.TextInputRange) ui.TextInputRange {
	switch {
	case r != nil:
		n := e.total()
		return ui.TextInputRange{Start: min(max(r.Start, 0), n), End: min(max(r.End, r.Start, 0), n)}
	case e.mark != nil:
		return *e.mark
	}
	return e.sel.Range
}

// TextForRange is the text of r, clamped to the document.
func (e *Editor) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	s := plain(e.doc)
	a, z := ui.UTF16ByteOffset(s, max(r.Start, 0)), ui.UTF16ByteOffset(s, max(r.End, r.Start, 0))
	return s[a:z], ui.TextInputRange{Start: ui.UTF16Len(s[:a]), End: ui.UTF16Len(s[:z])}
}

// Selection is the selection, as input methods see it.
func (e *Editor) Selection() ui.TextInputSelection { return e.sel }

// MarkedRange is the composition's range.
func (e *Editor) MarkedRange() (ui.TextInputRange, bool) {
	if e.mark == nil {
		return ui.TextInputRange{}, false
	}
	return *e.mark, true
}

// ReplaceText types s in place of r: the composition, if any, otherwise the
// selection for nil.
func (e *Editor) ReplaceText(r *ui.TextInputRange, s string) {
	if !e.writable() {
		return
	}
	t := e.target(r)
	if s == "" && t.Start == t.End && e.mark == nil {
		return
	}
	e.change(s != "", t.Start, func() {
		e.put(t, e.typed(t.Start, s))
		e.mark = nil
	})
}

// SetMarkedText shows s as the composition in place of r, with selected
// (relative to s) selected. The document does not change until it ends.
func (e *Editor) SetMarkedText(r *ui.TextInputRange, s string, selected ui.TextInputRange) {
	if !e.writable() {
		return
	}
	t := e.target(r)
	if e.mark == nil && t.Start != t.End {
		// Composing over a selection takes the selection out first.
		e.change(false, t.Start, func() { e.put(t, Doc{}) })
		t.End = t.Start
	}
	e.put(t, e.typed(t.Start, s))
	if s == "" {
		e.mark = nil
		return
	}
	e.mark = &ui.TextInputRange{Start: t.Start, End: t.Start + ui.UTF16Len(s)}
	e.sel = ui.TextInputSelection{Range: ui.TextInputRange{Start: e.mark.Start + selected.Start, End: e.mark.Start + selected.End}}
}

// UnmarkText ends the composition, keeping its text.
func (e *Editor) UnmarkText() {
	if e.mark == nil {
		return
	}
	at := e.mark.Start
	e.change(true, at, func() { e.mark = nil })
}

// cancelComposition takes the composition out.
func (e *Editor) cancelComposition() {
	if e.mark != nil {
		e.put(*e.mark, Doc{})
		e.mark = nil
	}
}

// BoundsForRange is the box of r's start, or of its first line of text,
// in the element, and the part of r it covers.
func (e *Editor) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	e.layout()
	i := e.lineOf(r.Start)
	ln := e.lines[i]
	n := ui.UTF16Len(blockText(e.doc.Blocks[i]))
	local := ui.TextInputRange{Start: r.Start - ln.start, End: min(max(r.End, r.Start)-ln.start, n)}
	b := ln.l.Caret(local.Start)
	if local.End > local.Start {
		if rs := ln.l.SelectionRects(local); len(rs) > 0 {
			b = rs[0]
		}
	}
	b.X += ln.x
	b.Y += ln.y
	return b, ui.TextInputRange{Start: r.Start, End: ln.start + local.End}, true
}

// IndexForPoint is the offset nearest p, in the element.
func (e *Editor) IndexForPoint(p ui.Point) (int, bool) { return e.indexAt(p), true }

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

// boundary is the grapheme boundary after i, or before it on back; the
// newline between blocks is one.
func (e *Editor) boundary(i int, back bool) int {
	k := e.lineOf(i)
	ln := e.lines[k]
	local, n := i-ln.start, ui.UTF16Len(blockText(e.doc.Blocks[k]))
	switch {
	case back && local == 0:
		return max(i-1, 0)
	case back:
		return ln.start + ln.l.PreviousBoundary(local)
	case local >= n:
		return min(i+1, e.total())
	}
	return ln.start + ln.l.NextBoundary(local)
}

// moveTo moves the caret to i, extending the selection on extend.
func (e *Editor) moveTo(i int, extend bool) {
	e.pending = nil
	if !extend {
		e.sel = caretAt(i)
		return
	}
	anchor := e.sel.Range.Start
	if e.sel.Reversed {
		anchor = e.sel.Range.End
	}
	e.sel = ui.TextInputSelection{Range: ui.TextInputRange{Start: min(anchor, i), End: max(anchor, i)}, Reversed: i < anchor}
}

func (e *Editor) handle(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputPointerDown:
		e.UnmarkText()
		i := e.indexAt(ui.Point{X: ev.X, Y: ev.Y})
		if ev.Clicks == 2 {
			p := posAt(e.doc, i)
			a, b := wordAt(blockText(e.doc.Blocks[p.Block]), p.Rune)
			e.sel = ui.TextInputSelection{Range: ui.TextInputRange{
				Start: offset(e.doc, Pos{Block: p.Block, Rune: a}), End: offset(e.doc, Pos{Block: p.Block, Rune: b})}}
			e.pending = nil
			return true
		}
		e.moveTo(i, ev.Mods&ui.Shift != 0)
		e.dragging = true
		return true
	case ui.InputPointerMove:
		if e.dragging {
			e.moveTo(e.indexAt(ui.Point{X: ev.X, Y: ev.Y}), true)
			return true
		}
	case ui.InputPointerUp:
		e.dragging = false
		return true
	case ui.InputKeyDown:
		return e.key(ev.Mods, ev.Key)
	case ui.InputCommand:
		return e.command(ev.Text)
	}
	return false
}

func (e *Editor) key(mods ui.Modifiers, k ui.Key) bool {
	if e.mark != nil {
		// The input method has the keys while it composes, but Esc.
		if k == ui.KeyEscape {
			e.cancelComposition()
			return true
		}
		return false
	}
	shift, m := mods&ui.Shift != 0, mods&^ui.Shift
	r := e.sel.Range
	switch {
	case mods == ui.Cmd && k == ui.KeyB:
		e.ToggleBold()
		return true
	case mods == ui.Cmd && k == ui.KeyI:
		e.ToggleItalic()
		return true
	case m == ui.Cmd && k == ui.KeyUp:
		e.moveTo(0, shift)
		return true
	case m == ui.Cmd && k == ui.KeyDown:
		e.moveTo(e.total(), shift)
		return true
	case m == ui.Cmd && (k == ui.KeyLeft || k == ui.KeyRight):
		k = map[ui.Key]ui.Key{ui.KeyLeft: ui.KeyHome, ui.KeyRight: ui.KeyEnd}[k]
	case m != 0:
		return false
	}
	switch k {
	case ui.KeyLeft, ui.KeyRight:
		back := k == ui.KeyLeft
		if !shift && r.Start != r.End {
			e.moveTo(map[bool]int{true: r.Start, false: r.End}[back], false)
		} else {
			e.moveTo(e.boundary(e.sel.Caret(), back), shift)
		}
	case ui.KeyUp, ui.KeyDown:
		c := e.caretRect(e.sel.Caret())
		y := c.Y - e.gap() - 1
		if k == ui.KeyDown {
			y = c.Y + c.H + e.gap() + 1
		}
		to := e.indexAt(ui.Point{X: c.X, Y: y})
		if y < 0 {
			to = 0
		} else if y > e.height {
			to = e.total()
		}
		e.moveTo(to, shift)
	case ui.KeyHome, ui.KeyEnd:
		c := e.caretRect(e.sel.Caret())
		x := float32(0)
		if k == ui.KeyEnd {
			ln := e.lines[e.lineOf(e.sel.Caret())]
			w, _ := ln.l.Size()
			x = ln.x + w
		}
		e.moveTo(e.indexAt(ui.Point{X: x, Y: c.Y + c.H/2}), shift)
	case ui.KeyBackspace, ui.KeyDelete:
		e.deleteKey(k == ui.KeyDelete)
	case ui.KeyEnter:
		e.enter()
	default:
		return false
	}
	return true
}

func (e *Editor) deleteKey(forward bool) {
	if !e.writable() {
		return
	}
	r := e.sel.Range
	if r.Start == r.End {
		p := posAt(e.doc, r.Start)
		if b := e.doc.Blocks[p.Block]; !forward && p.Rune == 0 && (b.Kind == richtext.ListItem || b.Kind == richtext.Quote) {
			// At an item's or a quote's start, it becomes a paragraph first.
			e.change(false, -1, func() { e.doc = setBlocks(e.doc, p.Block, p.Block, paragraph) })
			return
		}
		n := e.boundary(r.Start, !forward)
		r = ui.TextInputRange{Start: min(n, r.Start), End: max(n, r.Start)}
		if r.Start == r.End {
			return
		}
	}
	e.change(false, -1, func() { e.put(r, Doc{}) })
}

func paragraph(b *Block) { b.Kind, b.Level, b.Ordered = richtext.Paragraph, 0, false }

// enter splits the block; an empty list item leaves the list instead.
func (e *Editor) enter() {
	if !e.writable() {
		return
	}
	r := e.sel.Range
	p := posAt(e.doc, r.Start)
	b := e.doc.Blocks[p.Block]
	if r.Start == r.End && b.Kind == richtext.ListItem && runeLen(b) == 0 {
		e.change(false, -1, func() { e.doc = setBlocks(e.doc, p.Block, p.Block, paragraph) })
		return
	}
	like := Block{Kind: b.Kind, Level: b.Level, Ordered: b.Ordered}
	e.change(false, -1, func() { e.put(r, Doc{Blocks: []Block{like, like}}) })
}

func (e *Editor) command(name string) bool {
	r := e.sel.Range
	switch name {
	case "selectAll":
		e.sel = ui.TextInputSelection{Range: ui.TextInputRange{End: e.total()}}
	case "copy":
		if r.Start != r.End {
			s, _ := e.TextForRange(r)
			e.svc.WriteClipboard(s)
		}
	case "cut", "delete":
		if !e.writable() || r.Start == r.End {
			return true
		}
		if name == "cut" {
			s, _ := e.TextForRange(r)
			e.svc.WriteClipboard(s)
		}
		e.change(false, -1, func() { e.put(r, Doc{}) })
	case "paste":
		if e.writable() {
			e.paste()
		}
	case "undo", "redo":
		if e.writable() {
			e.cancelComposition()
			if s, ok := e.hist.step(snapshot{e.doc, e.sel}, name == "redo"); ok {
				e.doc, e.sel, e.dirty, e.pending = s.doc, s.sel, true, nil
				e.version++
			}
		}
	default:
		return false
	}
	return true
}

// paste inserts the clipboard's HTML, sanitized, or else its text.
func (e *Editor) paste() {
	var frag Doc
	if e.o.PasteHTML != nil {
		if h := e.o.PasteHTML(); h != "" {
			frag = richtext.FromComposeHTML(mail.SanitizeCompose(h))
		}
	}
	if len(frag.Blocks) == 0 {
		s := e.svc.ReadClipboard()
		if s == "" {
			return
		}
		frag = e.typed(e.sel.Range.Start, s)
	}
	r := e.sel.Range
	e.change(false, -1, func() { e.put(r, frag) })
}

// selPos is the selection's ends in the document.
func (e *Editor) selPos() (Pos, Pos) {
	return posAt(e.doc, e.sel.Range.Start), posAt(e.doc, e.sel.Range.End)
}

// toggle turns a style on over the selection, or off where it all has it;
// at a caret, for the text typed next.
func (e *Editor) toggle(has func(Inline) bool, set func(*Inline, bool)) {
	if !e.writable() || e.mark != nil {
		return
	}
	a, b := e.selPos()
	if a == b {
		st := styleAt(e.doc, a)
		if e.pending != nil {
			st = *e.pending
		}
		set(&st, !has(st))
		e.pending = &st
		return
	}
	on := !every(e.doc, a, b, has)
	e.change(false, -1, func() { e.doc = eachRun(e.doc, a, b, func(in *Inline) { set(in, on) }) })
}

// ToggleBold makes the selection bold, or not.
func (e *Editor) ToggleBold() {
	e.toggle(func(in Inline) bool { return in.Bold }, func(in *Inline, on bool) { in.Bold = on })
}

// ToggleItalic makes the selection italic, or not.
func (e *Editor) ToggleItalic() {
	e.toggle(func(in Inline) bool { return in.Italic }, func(in *Inline, on bool) { in.Italic = on })
}

// SetLink links the selection to u, an http, https or mailto URL; at a
// caret it inserts u linked. Other URLs change nothing.
func (e *Editor) SetLink(u string) {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || !(p.Scheme == "mailto" && p.Opaque != "" || (p.Scheme == "http" || p.Scheme == "https") && p.Host != "") {
		return
	}
	if !e.writable() || e.mark != nil {
		return
	}
	u = p.String()
	a, b := e.selPos()
	if a == b {
		r := e.sel.Range
		e.change(false, -1, func() {
			e.put(r, Doc{Blocks: []Block{{Inlines: []Inline{{Text: u, Link: u}}}}})
		})
		return
	}
	e.change(false, -1, func() { e.doc = eachRun(e.doc, a, b, func(in *Inline) { in.Link = u }) })
}

// setKind makes the selected blocks list items or quotes, or paragraphs
// when they all already are.
func (e *Editor) setKind(is func(Block) bool, set func(*Block)) {
	if !e.writable() || e.mark != nil {
		return
	}
	a, b := e.selPos()
	all := true
	for _, blk := range e.doc.Blocks[a.Block : b.Block+1] {
		all = all && is(blk)
	}
	if all {
		set = paragraph
	}
	e.change(false, -1, func() { e.doc = setBlocks(e.doc, a.Block, b.Block, set) })
}

// ToggleList makes the selected blocks items of a list, numbered on
// ordered, or paragraphs again.
func (e *Editor) ToggleList(ordered bool) {
	e.setKind(func(b Block) bool { return b.Kind == richtext.ListItem && b.Ordered == ordered }, func(b *Block) {
		if b.Kind != richtext.ListItem {
			b.Level = 1
		}
		b.Kind, b.Ordered = richtext.ListItem, ordered
	})
}

// ToggleQuote makes the selected blocks a quote, or paragraphs again.
func (e *Editor) ToggleQuote() {
	e.setKind(func(b Block) bool { return b.Kind == richtext.Quote }, func(b *Block) {
		b.Kind, b.Level, b.Ordered = richtext.Quote, 1, false
	})
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

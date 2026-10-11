// Package editor is Godwit's native rich text editor for compose content
// (paragraphs, bold, italic, links, lists and quotes): an application-owned
// ui.TextInputClient over a richtext.Doc, with input methods, selection,
// keyboard and pointer editing, sanitized paste and undo.
package editor

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
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
	doc     Doc // the composition is in it, at mark
	sel     ui.TextInputSelection
	mark    *ui.TextInputRange
	base    *snapshot // the document and selection before the composition
	pending *Inline   // the style of the next text typed, after Cmd+B at a caret
	hist    history
	now     func() time.Time // ends a step of typing after a pause
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

// New is an editor of d, with the caret at its start; now is the clock
// that ends a step of typing after a pause.
func New(d richtext.Doc, now func() time.Time) *Editor {
	return &Editor{doc: own(d), now: now, dirty: true}
}

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

func (e *Editor) total() int { return ui.UTF16Len(plain(e.doc)) }

// committed is the document and the selection without the composition:
// as they were before it.
func (e *Editor) committed() (Doc, ui.TextInputSelection) {
	if e.base != nil {
		return e.base.doc, e.base.sel
	}
	return e.doc, e.sel
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
// part of the typing step before it when typing at that step's caret. A
// composition ending is a step from before it began; over a selection, a
// step of its own.
func (e *Editor) change(typing bool, at int, f func()) {
	d, s := e.committed()
	if s.Range.Start != s.Range.End {
		typing = false
	}
	e.hist.push(snapshot{d, s}, typing, e.now(), at)
	e.base = nil
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

func paragraph(b *Block) { b.Kind, b.Level, b.Ordered = richtext.Paragraph, 0, false }

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
		e.hist.endTyping()
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

package editor

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/richtext"
)

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
	if s == "" {
		e.cancelComposition()
		return
	}
	if e.mark == nil {
		e.base = &snapshot{e.doc, e.sel}
	}
	e.put(t, e.typed(t.Start, s))
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

// cancelComposition takes the composition out, giving back the document
// and the selection from before it.
func (e *Editor) cancelComposition() {
	if e.base != nil {
		e.doc, e.sel, e.dirty = e.base.doc, e.base.sel, true
	}
	e.base, e.mark = nil, nil
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
	e.hist.endTyping()
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
			e.hist.endTyping()
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
		e.hist.endTyping()
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
	a, b := e.selPos()
	// Pasted over whole blocks, or into an empty one, the first block keeps
	// its kind; otherwise its text joins the block pasted into.
	whole := a.Rune == 0 && b.Rune == runeLen(e.doc.Blocks[b.Block])
	e.change(false, -1, func() {
		e.put(r, frag)
		if k := frag.Blocks[0]; whole {
			e.doc = setBlocks(e.doc, a.Block, a.Block, func(x *Block) { x.Kind, x.Level, x.Ordered = k.Kind, k.Level, k.Ordered })
		}
	})
}

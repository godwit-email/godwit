package webui

import (
	"sync"
	"time"

	"github.com/egoist/mygo"
)

var (
	// Closing tells a compose page its window is closing: it flushes its
	// input and calls ComposePage.Closed.
	Closing = mygo.NewEvent[struct{}]("closing")
	// ThemeChanged carries the theme to every page when the settings change.
	ThemeChanged = mygo.NewEvent[Theme]("theme")
)

// closeWait is how long a closing window waits for its page before asking.
const closeWait = 5 * time.Second

// Window is one compose window, as Windows drives it.
type Window interface {
	ID() int
	Focus()
	// Destroy closes the window without asking again.
	Destroy()
	// EmitClosing tells the page to flush and call Closed.
	EmitClosing()
}

// win is an open window; stop is set while it waits for its page.
type win struct {
	w     Window
	draft int64
	stop  func() bool
}

// closeBatch is a CloseAll waiting for its windows: left are the ones not
// settled yet, kept whether the user kept any.
type closeBatch struct {
	left map[int]bool
	kept bool
	done func(ok bool)
}

// Windows opens one compose window per draft and lets none close or quit
// with input unsaved: a window closes once its page reports its last input
// saved, or the user chose to close it anyway. No timeout closes one.
type Windows struct {
	c         *Compose
	newWindow func(draftID int64) Window
	ask       func(w Window) bool
	after     func(d time.Duration, f func()) (stop func() bool) // the clock; tests swap it

	mu      sync.Mutex // never held while calling a Window, ask or a done
	byDraft map[int64]*win
	byID    map[int]*win
	batches []*closeBatch
}

// NewWindows opens windows with newWindow. ask shows 「有修改没有保存」 for
// a window whose last input could not be saved: true closes it anyway.
// Windows hears the pages' final flush through c.OnClosed.
func NewWindows(c *Compose, newWindow func(draftID int64) Window, ask func(w Window) bool) *Windows {
	ws := &Windows{c: c, newWindow: newWindow, ask: ask, byDraft: map[int64]*win{}, byID: map[int]*win{},
		after: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }}
	c.OnClosed(ws.answer)
	return ws
}

// Open shows draftID's window, opening it unless it is open already.
func (ws *Windows) Open(draftID int64) {
	ws.mu.Lock()
	x := ws.byDraft[draftID]
	ws.mu.Unlock()
	if x != nil {
		x.w.Focus()
		return
	}
	w := ws.newWindow(draftID)
	ws.c.Attach(w.ID(), draftID)
	x = &win{w: w, draft: draftID}
	ws.mu.Lock()
	ws.byDraft[draftID], ws.byID[w.ID()] = x, x
	ws.mu.Unlock()
}

// RequestClose starts closing window winID, from its OnClose (always
// prevented): the page flushes, then the window closes once the page
// reports saved, or asks on a failed save or after 5 s without an answer.
func (ws *Windows) RequestClose(winID int) {
	ws.mu.Lock()
	x := ws.byID[winID]
	if x == nil || x.stop != nil { // gone, or already waiting for its page
		ws.mu.Unlock()
		return
	}
	x.stop = ws.after(closeWait, func() { ws.answer(winID, false) })
	ws.mu.Unlock()
	x.w.EmitClosing()
}

// answer settles a closing window: saved closes it; otherwise the user
// chooses. An answer for a window not waiting, as a late one, is ignored.
func (ws *Windows) answer(winID int, saved bool) {
	ws.mu.Lock()
	x := ws.byID[winID]
	if x == nil || x.stop == nil {
		ws.mu.Unlock()
		return
	}
	x.stop()
	x.stop = nil
	ws.mu.Unlock()
	if saved || ws.ask(x.w) {
		ws.mu.Lock()
		dones := ws.forget(x)
		ws.mu.Unlock()
		run(dones)
		x.w.Destroy()
		return
	}
	ws.mu.Lock()
	dones := ws.settle(winID, true)
	ws.mu.Unlock()
	run(dones)
}

// Closed is told when window winID went: its page loses the draft.
func (ws *Windows) Closed(winID int) {
	ws.c.Detach(winID)
	ws.mu.Lock()
	var dones []func()
	if x := ws.byID[winID]; x != nil {
		dones = ws.forget(x)
	}
	ws.mu.Unlock()
	run(dones)
}

// Count is the number of open windows.
func (ws *Windows) Count() int {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return len(ws.byID)
}

// CloseAll closes every window as RequestClose does, for quitting; done
// reports whether all closed, false when the user kept one.
func (ws *Windows) CloseAll(done func(ok bool)) {
	ws.mu.Lock()
	if len(ws.byID) == 0 {
		ws.mu.Unlock()
		done(true)
		return
	}
	b := &closeBatch{left: map[int]bool{}, done: done}
	var ids []int
	for id := range ws.byID {
		b.left[id] = true
		ids = append(ids, id)
	}
	ws.batches = append(ws.batches, b)
	ws.mu.Unlock()
	for _, id := range ids {
		ws.RequestClose(id)
	}
}

// forget drops x as closed; ws.mu is held.
func (ws *Windows) forget(x *win) []func() {
	id := x.w.ID()
	delete(ws.byID, id)
	if ws.byDraft[x.draft] == x {
		delete(ws.byDraft, x.draft)
	}
	return ws.settle(id, false)
}

// settle marks window id closed or kept in every batch waiting for it and
// returns the dones of the batches it completes; ws.mu is held.
func (ws *Windows) settle(id int, kept bool) []func() {
	var dones []func()
	rest := ws.batches[:0]
	for _, b := range ws.batches {
		if b.left[id] {
			delete(b.left, id)
			b.kept = b.kept || kept
		}
		if len(b.left) == 0 {
			ok := !b.kept
			done := b.done
			dones = append(dones, func() { done(ok) })
			continue
		}
		rest = append(rest, b)
	}
	ws.batches = rest
	return dones
}

func run(dones []func()) {
	for _, d := range dones {
		d()
	}
}

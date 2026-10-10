package webui

import (
	"sync"
	"time"

	"github.com/egoist/mygo"
)

var (
	// Closing tells a compose page its window is closing: it flushes its
	// input and calls ComposePage.Closed with the payload, the close
	// attempt, so an answer to an earlier attempt counts for nothing.
	Closing = mygo.NewEvent[int]("closing")
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
	// EmitClosing tells the page to flush and call Closed with gen.
	EmitClosing(gen int)
}

// winState is where a window is in closing.
type winState int

const (
	idle       winState = iota
	waiting             // for its page's answer, or the timeout
	asking              // the user whether to close it anyway
	destroying          // until it reports Closed
)

// win is an open window. gen counts its close attempts, so a timer of an
// earlier one does nothing.
type win struct {
	w     Window
	draft int64
	state winState
	gen   int
	stop  func() bool
}

// closeBatch is a CloseAll waiting for its windows: left are the ones not
// gone or kept yet, kept whether the user kept any.
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
	batch   *closeBatch // the CloseAll in progress
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
// A window already closing goes on as it was: one flush, one question.
func (ws *Windows) RequestClose(winID int) {
	ws.mu.Lock()
	x := ws.byID[winID]
	if x == nil || x.state != idle {
		ws.mu.Unlock()
		return
	}
	x.state = waiting
	x.gen++
	gen := x.gen
	x.stop = ws.after(closeWait, func() { ws.answer(winID, gen, false) })
	ws.mu.Unlock()
	x.w.EmitClosing(gen)
}

// answer settles a waiting window, from its page or its timer: saved
// closes it; otherwise the user chooses. gen is the close attempt answered:
// an answer for a window not waiting, or for an earlier attempt, does
// nothing.
func (ws *Windows) answer(winID, gen int, saved bool) {
	ws.mu.Lock()
	x := ws.byID[winID]
	if x == nil || x.state != waiting || gen != x.gen {
		ws.mu.Unlock()
		return
	}
	x.stop()
	if !saved {
		x.state = asking
		ws.mu.Unlock()
		closeAnyway := ws.ask(x.w)
		ws.mu.Lock()
		if ws.byID[winID] != x { // it went meanwhile
			ws.mu.Unlock()
			return
		}
		if !closeAnyway {
			x.state = idle
			done := ws.settle(winID, true)
			ws.mu.Unlock()
			done()
			return
		}
	}
	x.state = destroying
	ws.mu.Unlock()
	x.w.Destroy()
}

// Closed is told when window winID went: its page loses the draft, and a
// CloseAll counts it closed.
func (ws *Windows) Closed(winID int) {
	ws.c.Detach(winID)
	ws.mu.Lock()
	done := func() {}
	if x := ws.byID[winID]; x != nil {
		delete(ws.byID, winID)
		if ws.byDraft[x.draft] == x {
			delete(ws.byDraft, x.draft)
		}
		done = ws.settle(winID, false)
	}
	ws.mu.Unlock()
	done()
}

// Count is the number of open windows.
func (ws *Windows) Count() int {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return len(ws.byID)
}

// CloseAll closes every window as RequestClose does, for quitting; done
// reports, once every one has gone or been kept, whether all went. A
// CloseAll while one is in progress joins it: only the first done runs.
func (ws *Windows) CloseAll(done func(ok bool)) {
	ws.mu.Lock()
	if ws.batch != nil {
		ws.mu.Unlock()
		return
	}
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
	ws.batch = b
	ws.mu.Unlock()
	for _, id := range ids {
		ws.RequestClose(id)
	}
}

// settle marks window id gone or kept in the CloseAll in progress and
// returns what to run once ws.mu is released: its done, when id was the
// last it waited for. ws.mu is held.
func (ws *Windows) settle(id int, kept bool) func() {
	b := ws.batch
	if b == nil || !b.left[id] {
		return func() {}
	}
	delete(b.left, id)
	b.kept = b.kept || kept
	if len(b.left) > 0 {
		return func() {}
	}
	ws.batch = nil
	return func() { b.done(!b.kept) }
}

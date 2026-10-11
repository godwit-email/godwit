package webui

import (
	"log"
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
	spawn     func(f func())                                     // runs a release; tests swap it

	mu      sync.Mutex // never held while calling a Window, ask or a done
	byDraft map[int64]*win
	byID    map[int]*win
	batch   *closeBatch // the CloseAll in progress
	// releasing holds the drafts whose closed window is being released:
	// true when an Open waits for that to finish.
	releasing map[int64]bool
	// opening holds the drafts whose window is being made: true when an
	// Open joined it, to focus the window once it exists.
	opening map[int64]bool

	// OnChange, when set (before any window opens), is called after a window
	// registers and after a closed window's release, the empty-draft delete
	// included: IsOpen has changed. Never under mu.
	OnChange func()
}

// IsOpen reports whether draftID's window is open or being made.
func (ws *Windows) IsOpen(draftID int64) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	_, making := ws.opening[draftID]
	return making || ws.byDraft[draftID] != nil
}

func (ws *Windows) changed() {
	if ws.OnChange != nil {
		ws.OnChange()
	}
}

// NewWindows opens windows with newWindow. ask shows 「有修改没有保存」 for
// a window whose last input could not be saved: true closes it anyway.
// Windows hears the pages' final flush through c.OnClosed.
func NewWindows(c *Compose, newWindow func(draftID int64) Window, ask func(w Window) bool) *Windows {
	ws := &Windows{c: c, newWindow: newWindow, ask: ask, byDraft: map[int64]*win{}, byID: map[int]*win{}, releasing: map[int64]bool{}, opening: map[int64]bool{},
		after: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		spawn: func(f func()) { go f() }}
	c.OnClosed(ws.answer)
	c.OnDone(ws.Done)
	return ws
}

// Open shows draftID's window, opening it unless it is open already. While
// the draft's closed window is still being released, the Open waits for
// that: it opens then, unless the release deleted the draft as empty. While
// the window is being made, the Open joins that: it is focused once made.
func (ws *Windows) Open(draftID int64) {
	ws.mu.Lock()
	if x := ws.byDraft[draftID]; x != nil {
		ws.mu.Unlock()
		x.w.Focus()
		return
	}
	if _, ok := ws.releasing[draftID]; ok {
		ws.releasing[draftID] = true
		ws.mu.Unlock()
		return
	}
	if _, ok := ws.opening[draftID]; ok {
		ws.opening[draftID] = true
		ws.mu.Unlock()
		return
	}
	ws.opening[draftID] = false
	ws.mu.Unlock()
	ws.make(draftID)
}

// make makes and registers draftID's window, reserved in opening, and
// focuses it if an Open joined meanwhile.
func (ws *Windows) make(draftID int64) {
	w := ws.newWindow(draftID)
	ws.c.Attach(w.ID(), draftID)
	x := &win{w: w, draft: draftID}
	ws.mu.Lock()
	focus := ws.opening[draftID]
	delete(ws.opening, draftID)
	ws.byDraft[draftID], ws.byID[w.ID()] = x, x
	ws.mu.Unlock()
	if focus {
		w.Focus()
	}
	ws.changed()
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
		if ws.byID[winID] != x || x.state != asking { // it went meanwhile (Done)
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

// Done destroys window winID now, without a flush or a question: its draft
// was sent or discarded. A close or quit waiting for it settles once it has
// gone.
func (ws *Windows) Done(winID int) {
	ws.mu.Lock()
	x := ws.byID[winID]
	if x == nil || x.state == destroying {
		ws.mu.Unlock()
		return
	}
	if x.state == waiting {
		x.stop()
	}
	x.state = destroying
	ws.mu.Unlock()
	x.w.Destroy()
}

// CloseDraft destroys draftID's window, if one is open, as Done: the draft
// was deleted elsewhere.
func (ws *Windows) CloseDraft(draftID int64) {
	ws.mu.Lock()
	x := ws.byDraft[draftID]
	ws.mu.Unlock()
	if x != nil {
		ws.Done(x.w.ID())
	}
}

// Closed is told when window winID went, on mygo's main thread: it returns
// at once, and its page's session is released (Compose.Release, which
// waits for a call in progress and deletes an empty draft) on another
// goroutine. The window counts as open, and a CloseAll waits for it, until
// that release is done, so a quit never exits in the middle of it.
func (ws *Windows) Closed(winID int) {
	ws.mu.Lock()
	x := ws.byID[winID]
	if x != nil {
		x.state = destroying
		if ws.byDraft[x.draft] == x {
			delete(ws.byDraft, x.draft)
			ws.releasing[x.draft] = false
		}
	}
	ws.mu.Unlock()
	ws.spawn(func() {
		ws.c.Release(winID)
		if x != nil {
			// The reopen is reserved in opening under the same lock that ends
			// the release, so no Open slips in between to make a second window.
			ws.mu.Lock()
			reopen := ws.releasing[x.draft]
			delete(ws.releasing, x.draft)
			if reopen {
				ws.opening[x.draft] = false
			}
			ws.mu.Unlock()
			if reopen {
				if ws.c.exists(x.draft) {
					ws.make(x.draft)
				} else {
					ws.mu.Lock()
					delete(ws.opening, x.draft)
					ws.mu.Unlock()
					log.Printf("not opening draft %d: deleted as empty when its window closed", x.draft)
				}
			}
		}
		ws.mu.Lock()
		done := func() {}
		if x != nil && ws.byID[winID] == x {
			delete(ws.byID, winID)
			done = ws.settle(winID, false)
		}
		ws.mu.Unlock()
		ws.changed()
		done()
	})
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

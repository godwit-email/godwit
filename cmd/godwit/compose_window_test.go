package main

import (
	"testing"

	"github.com/egoist/mygo"
)

// fakeWindows is the compose windows as the quit hook sees them.
type fakeWindows struct {
	n  int
	ok bool // what CloseAll reports
}

func (w *fakeWindows) Count() int { return w.n }
func (w *fakeWindows) CloseAll(done func(bool)) {
	if w.ok {
		w.n = 0
	}
	done(w.ok)
}

func TestQuitHookWebThenNative(t *testing.T) {
	quits := 0
	old := quitApp
	quitApp = func() { quits++ }
	t.Cleanup(func() { quitApp = old })
	natives := 0
	ws := &fakeWindows{n: 2}
	resets := 0
	hook := quitHook(ws, func(*mygo.QuitEvent) { natives++ }, func() { resets++ })

	e := &mygo.QuitEvent{}
	hook(e)
	if !e.DefaultPrevented() || natives != 0 || quits != 0 {
		t.Fatalf("a window kept: prevented %v, native %d, quit %d", e.DefaultPrevented(), natives, quits)
	}

	ws.ok = true
	e = &mygo.QuitEvent{}
	hook(e)
	if !e.DefaultPrevented() || natives != 0 || quits != 1 {
		t.Fatalf("all closed: prevented %v, native %d, quit %d", e.DefaultPrevented(), natives, quits)
	}

	e = &mygo.QuitEvent{} // the quit again, with no compose window
	hook(e)
	if natives != 1 || quits != 1 || resets != 2 {
		t.Fatalf("no windows: native %d, quit %d, reset %d", natives, quits, resets)
	}
}

// A quit the native flush ran for, then cancelled by a compose window the
// user kept, flushes again on the next quit instead of closing the App.
func TestQuitCancelledThenNativeFlushesAgain(t *testing.T) {
	old := quitApp
	quitApp = func() {}
	t.Cleanup(func() { quitApp = old })
	var proceed func()
	flushes, closes, quits := 0, 0, 0
	nq := &nativeQuit{beforeClose: func(p func()) { flushes++; proceed = p }, close: func() { closes++ }, quit: func() { quits++ }}
	ws := &fakeWindows{}
	hook := quitHook(ws, nq.onQuit, nq.reset)

	e := &mygo.QuitEvent{} // e.g. the main window's close: no compose window yet
	hook(e)
	if !e.DefaultPrevented() || flushes != 1 {
		t.Fatalf("first quit: prevented %v, flushes %d", e.DefaultPrevented(), flushes)
	}
	proceed() // saved; it quits again
	if quits != 1 || !nq.flushed {
		t.Fatalf("after the flush: quits %d, flushed %v", quits, nq.flushed)
	}

	ws.n = 1 // a compose window opened meanwhile; the user keeps it
	e = &mygo.QuitEvent{}
	hook(e)
	if !e.DefaultPrevented() || closes != 0 {
		t.Fatalf("kept window: prevented %v, closes %d", e.DefaultPrevented(), closes)
	}

	ws.n = 0 // the user edits in the main window, closes the compose window, quits
	e = &mygo.QuitEvent{}
	hook(e)
	if !e.DefaultPrevented() || flushes != 2 || closes != 0 {
		t.Fatalf("next quit: prevented %v, flushes %d, closes %d", e.DefaultPrevented(), flushes, closes)
	}
	proceed()
	e = &mygo.QuitEvent{}
	hook(e)
	if e.DefaultPrevented() || closes != 1 {
		t.Fatalf("flushed quit: prevented %v, closes %d", e.DefaultPrevented(), closes)
	}
}

// A native save still running when a later quit's reset ran (the user then
// kept a compose window) must not quit the App when it completes.
func TestStaleNativeFlushDoesNotQuit(t *testing.T) {
	var proceed func()
	quits := 0
	nq := &nativeQuit{beforeClose: func(p func()) { proceed = p }, close: func() {}, quit: func() { quits++ }}
	e := &mygo.QuitEvent{}
	nq.onQuit(e) // native save starts
	old := proceed
	nq.reset() // a second quit found a compose window; the user kept it
	old()      // the first native save completes
	if quits != 0 || nq.flushed {
		t.Fatalf("stale proceed: quits %d, flushed %v", quits, nq.flushed)
	}
	nq.onQuit(&mygo.QuitEvent{}) // the next quit flushes again and its own proceed quits
	proceed()
	if quits != 1 || !nq.flushed {
		t.Fatalf("fresh proceed: quits %d, flushed %v", quits, nq.flushed)
	}
}

func TestComposeWindowNavigation(t *testing.T) {
	for _, entry := range []string{"mygo://localhost/compose.html", "http://localhost:5173/compose.html"} {
		if !sameDocument(entry, entry) || !sameDocument(entry, entry+"#x") {
			t.Errorf("%s denied itself", entry)
		}
		for _, u := range []string{"https://example.com/", "file:///etc/passwd", "mygo://localhost/other.html", "", "javascript:alert(1)"} {
			if sameDocument(entry, u) {
				t.Errorf("%s allowed %q", entry, u)
			}
		}
	}
	if sameDocument("", "") {
		t.Error("no entry yet allowed a navigation")
	}
}

func TestComposeWindowOptions(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		o := composeWindowOptions(goos)
		if o.URL != "/compose.html" || o.Width != 860 || o.Height != 720 || o.MinWidth != 640 || o.MinHeight != 480 || o.StateKey != "compose" || o.Title != "新邮件" {
			t.Fatalf("%s: options %+v", goos, o)
		}
		want := mygo.TitleBarStyle("") // the page draws no window controls of Windows or Linux
		if goos == "darwin" {
			want = mygo.TitleBarHiddenInset // transparent, the traffic lights over the page's top bar
		}
		if o.TitleBarStyle != want {
			t.Errorf("%s: title bar %q, want %q", goos, o.TitleBarStyle, want)
		}
	}
}

// fakePlaced is a window with a position.
type fakePlaced struct{ x, y int }

func (w *fakePlaced) Position() (int, int) { return w.x, w.y }
func (w *fakePlaced) SetPosition(x, y int) { w.x, w.y = x, y }

func TestComposeWindowCascade(t *testing.T) {
	var c cascade
	first := &fakePlaced{100, 80} // where the saved state put it
	c.place(first)
	if *first != (fakePlaced{100, 80}) {
		t.Fatalf("the first window moved to %+v", *first)
	}
	second := &fakePlaced{100, 80}
	c.place(second)
	if *second != (fakePlaced{124, 104}) {
		t.Fatalf("second at %+v", *second)
	}
	second.SetPosition(300, 200) // the user moves it
	third := &fakePlaced{100, 80}
	c.place(third)
	if *third != (fakePlaced{324, 224}) {
		t.Fatalf("third at %+v, want past the last open one", *third)
	}
	c.closed(third)
	c.closed(second)
	fourth := &fakePlaced{100, 80}
	c.place(fourth)
	if *fourth != (fakePlaced{124, 104}) {
		t.Fatalf("fourth at %+v, want past the first, the last still open", *fourth)
	}
}

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
	hook := quitHook(ws, func(*mygo.QuitEvent) { natives++ })

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
	if natives != 1 || quits != 1 {
		t.Fatalf("no windows: native %d, quit %d", natives, quits)
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
	o := composeWindowOptions()
	if o.URL != "/compose.html" || o.Width != 860 || o.Height != 720 || o.MinWidth != 640 || o.MinHeight != 480 || o.StateKey != "compose" || o.Title != "新邮件" {
		t.Fatalf("options %+v", o)
	}
}

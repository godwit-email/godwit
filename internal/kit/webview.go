package kit

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// frost is a window's: whether a frosted panel was built this frame (now)
// and the last (was), and the screenshots of its web views taken while one
// was. Each window keeps its own, so one window's dialog stills no other's
// pages.
type frost struct {
	now, was bool
	shots    map[ui.NativeWebView]*shot
}

type frostKey struct{}

func frostOf(c *ui.Context) *frost {
	return ui.Local(c.Root(), frostKey{}, func() frost { return frost{shots: map[ui.NativeWebView]*shot{}} })
}

// NewFrame starts the window's frame: WebView goes by whether its last one
// frosted a panel, as dialogs build after the pages under them.
func NewFrame(c *ui.Context) {
	f := frostOf(c)
	f.was, f.now = f.now, false
}

// pageCapturer is a web view that screenshots its page (mygo.WebView).
type pageCapturer interface {
	CapturePage() ([]byte, error)
}

// shot is a web view's screenshot, taken off the UI thread.
type shot struct {
	mu  sync.Mutex
	src ui.ImageSource
}

// WebView shows v as ui.WebView does, but while a frosted panel is up a
// screenshot of its page stands in for it: a web view is a native view under
// the window's surface, which a panel's blur cannot see. The page is modal
// behind the panel, so a still of it loses nothing.
func WebView(c *ui.Context, v ui.NativeWebView) ui.Element {
	if src := still(c, v); src != nil {
		box := ui.Box(c).AlignSelf(ui.Stretch)
		box.Children(func() { ui.Image(c, src).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover) })
		return box
	}
	return ui.WebView(c, v)
}

// still is v's screenshot while panels frost, asking for one the first
// time; nil until it arrives, and when nothing frosts (which forgets them,
// so the next panel sees the page as it is then).
func still(c *ui.Context, v ui.NativeWebView) ui.ImageSource {
	f := frostOf(c)
	if !f.now && !f.was {
		clear(f.shots)
		return nil
	}
	shots := f.shots
	cp, ok := v.(pageCapturer)
	if !ok {
		return nil
	}
	s := shots[v]
	if s == nil {
		s = &shot{}
		shots[v] = s
		go func() {
			b, err := cp.CapturePage() // waits on the main thread: never on the UI thread
			if err != nil {
				return // the page shows as it is
			}
			src, err := ui.DecodeBitmap(b)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.src = src
			s.mu.Unlock()
			c.Invalidate()
		}()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src
}

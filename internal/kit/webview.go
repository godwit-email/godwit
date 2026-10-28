package kit

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// frosted is whether a frosted panel was built this frame, wasFrosted the
// last frame's. UI thread only.
var frosted, wasFrosted bool

// NewFrame starts a frame: WebView goes by whether the last one frosted a
// panel, as dialogs build after the pages under them.
func NewFrame() { wasFrosted, frosted = frosted, false }

// pageCapturer is a web view that screenshots its page (mygo.WebView).
type pageCapturer interface {
	CapturePage() ([]byte, error)
}

// shot is a web view's screenshot, taken off the UI thread.
type shot struct {
	mu  sync.Mutex
	src ui.ImageSource
}

// shots are the screenshots taken while panels frost. UI thread only.
var shots = map[ui.NativeWebView]*shot{}

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
	if !frosted && !wasFrosted {
		clear(shots)
		return nil
	}
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

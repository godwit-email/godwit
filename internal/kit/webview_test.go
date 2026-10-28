package kit

import (
	"bytes"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// fakePage is a web view whose page screenshots to a 1×1 PNG.
type fakePage struct {
	ui.NativeWebView
	png   []byte
	shots atomic.Int32
}

func (f *fakePage) CapturePage() ([]byte, error) {
	f.shots.Add(1)
	return f.png, nil
}

// TestWebViewShotUnderGlass: while a frosted dialog is up, a screenshot of
// the page stands in for the web view, so the panel has pixels to blur; it
// is taken once, and the web view comes back when the dialog closes.
func TestWebViewShotUnderGlass(t *testing.T) {
	Glass = true
	NewFrame() // forget earlier tests' frosted frames
	NewFrame()
	t.Cleanup(func() { Glass = false; NewFrame(); NewFrame() })
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	page := &fakePage{png: buf.Bytes()}
	open := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		NewFrame()
		ui.Column(c).Fill().Children(func() { WebView(c, page).Grow(1) })
		Dialog(c, &open, "T", DialogOpts{Close: "Close"}, nil, nil)
	}), 400, 300)
	if tt.WebViewAt(200, 290) == nil {
		t.Fatal("no web view before the dialog")
	}
	open = true
	for i := 0; i < 100 && tt.WebViewAt(200, 290) != nil; i++ {
		tt.Frame() // the view reads open: build it again, as input would
		tt.Advance(theme.MotionFast)
		time.Sleep(time.Millisecond)
	}
	if tt.WebViewAt(200, 290) != nil {
		t.Fatalf("the web view still shows under the frosted dialog (%d screenshots)", page.shots.Load())
	}
	tt.Advance(2 * theme.MotionBase)
	if n := page.shots.Load(); n != 1 {
		t.Errorf("%d screenshots, want 1", n)
	}
	open = false
	tt.Frame()
	tt.Frame()
	if tt.WebViewAt(200, 290) == nil {
		t.Error("the web view did not come back after the dialog")
	}
}

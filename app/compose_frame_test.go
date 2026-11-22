package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// stubView stands in for the compose page's web view: the Tester places it
// and calls nothing on it.
type stubView struct{ ui.NativeWebView }

// A compose window's native layer: the title bar names the draft and its save
// state above the page, and the page's toasts rise 8 above its send bar, level
// with the bar's input, in the window's coordinates (under the title bar).
func TestComposeFrame(t *testing.T) {
	h := newHarness(t)
	drawn := 0
	f := NewComposeFrame(stubView{}, func() { drawn++ })
	tt := ui.NewTester(h.app.ComposeView(f), 860, 720)
	f.SetTitleBar("Re: Q4 partnership proposal", "草稿已保存 · 11:42")
	tt.Frame()
	title, ok := tt.Find("Re: Q4 partnership proposal")
	if !ok || !tt.HasText("草稿已保存 · 11:42") {
		t.Fatalf("no title bar: %q", tt.Texts())
	}
	ox, oy := f.Origin()
	if float32(oy) < title.Y+title.H || ox != 0 {
		t.Fatalf("the page at (%d, %d) is not under the title bar %v", ox, oy, title)
	}

	bar, field := ui.Rect{X: 8, Y: 600, W: 844, H: 112}, ui.Rect{X: 620, Y: 660, W: 220, H: 32}
	f.SetToastBar(bar, field)
	f.Toast(kit.ToastError, "出错了：boom")
	for range 3 {
		tt.Frame()
	}
	tt.Advance(2 * theme.MotionBase)
	msg, ok := tt.Find("出错了：boom")
	if !ok {
		t.Fatalf("no toast: %q", tt.Texts())
	}
	right, top := float32(ox)+field.X+field.W, float32(oy)+bar.Y-8 // the design's 8 above the bar
	if msg.Y+msg.H > top || msg.X+msg.W > right || msg.X < right-theme.ToastWidth {
		t.Errorf("toast text at %v, want it in the 360 wide toast ending at x %v, above y %v", msg, right, top)
	}
	if drawn < 3 {
		t.Errorf("the setters asked for %d frames, want one each", drawn)
	}
	if dir := os.Getenv("GODWIT_SHOTS"); dir != "" {
		out, err := os.Create(filepath.Join(dir, "ComposeFrame.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

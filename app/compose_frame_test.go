package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	strs "github.com/godwit-email/godwit/internal/i18n"
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
	shoot(t, tt, "ComposeFrame.png")
}

// sendLater asks f's send later at at, as the page does, and draws tt until
// the dialog shows; the answer comes on the channel.
func sendLater(t *testing.T, tt *ui.Tester, f *ComposeFrame, at time.Time) chan int64 {
	t.Helper()
	got := make(chan int64, 1)
	go func() { got <- f.SendLater(at.UnixMilli()) }()
	for range 200 {
		tt.Frame()
		if tt.HasText("定时发送") {
			tt.Advance(2 * theme.MotionBase)
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no send later dialog: %q", tt.Texts())
	return nil
}

// answer is what the page got, once the dialog closed.
func answer(t *testing.T, tt *ui.Tester, got chan int64) int64 {
	t.Helper()
	tt.Frame()
	select {
	case at := <-got:
		return at
	case <-time.After(5 * time.Second):
		t.Fatal("the page got no answer")
		return 0
	}
}

// Send later from a compose window: the page's call opens the native dialog
// at the page's time, which says only a running Godwit sends; a day and a
// time picked and 定时发送 give the page that time, 取消 gives it 0, and a
// time already past is refused in the readout, the page still waiting.
func TestComposeSendLater(t *testing.T) {
	h := newHarness(t) // its clock: 9:00 on Friday 9 October 2026
	f := NewComposeFrame(stubView{}, func() {})
	tt := ui.NewTester(h.app.ComposeView(f), 1440, 900)
	tt.SetLocale("zh-Hans")
	tt.SetRegion(i18n.RegionFor("zh-Hans"))
	tt.Frame()

	got := sendLater(t, tt, f, time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local))
	if !tt.HasText("今天 · 10月9日周五 10:00") || !tt.HasText(strs.T("compose.send_later.note")) {
		t.Fatalf("texts %q", tt.Texts())
	}
	shoot(t, tt, "SendLater-light.png")
	if err := tt.Click("2026年10月12日"); err != nil {
		t.Fatal(err)
	}
	face, ok := tt.AccessByRole(ui.RoleSlider, "时间")
	if !ok {
		t.Fatal("no clock face")
	}
	tt.ClickAt(face.Bounds.X+face.Bounds.W/2, face.Bounds.Y+face.Bounds.H*0.1) // the minute hand, at 12
	tt.Key(0, ui.KeyUp)
	if err := tt.Click("定时发送"); err != nil {
		t.Fatal(err)
	}
	if at, want := answer(t, tt, got), time.Date(2026, 10, 12, 11, 0, 0, 0, time.Local); at != want.UnixMilli() {
		t.Errorf("scheduled %v, want %v", time.UnixMilli(at), want)
	}

	got = sendLater(t, tt, f, time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local))
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if at := answer(t, tt, got); at != 0 {
		t.Errorf("cancelled, the page got %v, want 0", at)
	}

	got = sendLater(t, tt, f, time.Date(2026, 10, 9, 8, 0, 0, 0, time.Local))
	if err := tt.Click("定时发送"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("请选择一个将来的时间。") {
		t.Errorf("a past time is not refused: %q", tt.Texts())
	}
	select {
	case at := <-got:
		t.Fatalf("a past time gave the page %v", time.UnixMilli(at))
	default:
	}
	tt.Key(0, ui.KeyEscape)
	if at := answer(t, tt, got); at != 0 {
		t.Errorf("escaped, the page got %v, want 0", at)
	}

	tt.SetDark(true)
	sendLater(t, tt, f, time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local))
	shoot(t, tt, "SendLater-dark.png")
}

// Send later writes the month and the weekdays in the interface language,
// as its readout, whatever the regional formats; the week starts on the
// region's first day, as mygo's own calendar has it.
func TestComposeSendLaterLanguage(t *testing.T) {
	h := newHarness(t) // the interface in zh-Hans
	f := NewComposeFrame(stubView{}, func() {})
	tt := ui.NewTester(h.app.ComposeView(f), 1440, 900)
	tt.SetRegion(i18n.Region{Locale: "en-US", FirstWeekday: time.Sunday, HourCycle: 12})
	tt.Frame()
	sendLater(t, tt, f, time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local))
	for _, s := range []string{"今天 · 10月9日周五 10:00", "2026年10月", "周日", "周一"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	sun, _ := tt.Find("周日")
	mon, _ := tt.Find("周一")
	if sun.X >= mon.X {
		t.Errorf("周日 at %v, 周一 at %v: the week does not start on the region's Sunday", sun.X, mon.X)
	}
	if _, ok := tt.Access("2026年10月12日"); !ok {
		t.Errorf("the days are not named in the interface language: %q", tt.Texts())
	}
}

// A compose window closed with send later open answers the page's call
// with 0, and draws no dialog after.
func TestComposeSendLaterWindowClosed(t *testing.T) {
	h := newHarness(t)
	f := NewComposeFrame(stubView{}, func() {})
	tt := ui.NewTester(h.app.ComposeView(f), 1440, 900)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	got := sendLater(t, tt, f, time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local))
	f.Close()
	if at := answer(t, tt, got); at != 0 {
		t.Errorf("closed, the page got %v, want 0", at)
	}
	tt.Advance(2 * theme.MotionBase)
	if tt.HasText("定时发送") {
		t.Error("the dialog stays after the window closed")
	}
}

// shoot saves tt's image as name in $GODWIT_SHOTS, when it is set.
func shoot(t *testing.T, tt *ui.Tester, name string) {
	if dir := os.Getenv("GODWIT_SHOTS"); dir != "" {
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

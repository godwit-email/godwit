package kit

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// toastScene is a window with a content panel, a bar along its foot with
// an input of fieldWidth centered in it, and toasts; bar says whether the
// bar registers itself.
type toastScene struct {
	bar        bool
	fieldWidth float32
	toasts     []ui.Toast
	added      int
}

func (s *toastScene) view(c *ui.Context) {
	for ; s.added < len(s.toasts); s.added++ {
		c.AddToast(s.toasts[s.added])
	}
	panel := ui.Column(c).FillWidth().Height(c.Root().Bounds().H)
	panel.Children(func() {
		ui.Box(c).Grow(1)
		if !s.bar {
			return
		}
		bar := ui.Column(c).FillWidth().Height(60).AlignItems(ui.Center).Justify(ui.Center)
		bar.Children(func() {
			field := ui.Box(c).Width(s.fieldWidth).Height(36)
			ToastBar(c, bar, field)
		})
	})
	ToastAnchor(c, panel)
	ToastHost(c, "Close")
}

// toastEdges is where the toast ends: its right and bottom edges, as its
// close button is 8 in from them.
func toastEdges(t *testing.T, tt *ui.Tester) (right, bottom float32) {
	t.Helper()
	btn, ok := tt.Find("Close")
	if !ok {
		t.Fatalf("no close button in %q", tt.Texts())
	}
	return btn.X + btn.W + 8, btn.Y + btn.H + 8
}

// settle plays the frames a toast needs to come in and find its place: the
// host places it by where the page was the frame before.
func settle(tt *ui.Tester) {
	tt.Frame()
	tt.Frame()
	tt.Advance(2 * theme.MotionBase)
}

func closeable(title string) ui.Toast { return ui.Toast{ID: title, Title: title, Timeout: -1} }

func TestToastPlacement(t *testing.T) {
	cases := []struct {
		name                string
		w, h                int
		bar                 bool
		field               float32
		wantRight, wantBott float32
	}{
		{"panel corner", 800, 600, false, 0, 800 - 16, 600 - 16},
		{"bar, narrow input", 800, 600, true, 300, 400 + 150, 600 - 60 - 8},
		{"bar, input at its max width", 1600, 700, true, theme.ReaderMaxWidth, 800 + theme.ReaderMaxWidth/2, 700 - 60 - 8},
	}
	for _, tc := range cases {
		s := &toastScene{bar: tc.bar, fieldWidth: tc.field, toasts: []ui.Toast{closeable("Saved")}}
		tt := ui.NewTester(themed(s.view), tc.w, tc.h)
		settle(tt)
		right, bottom := toastEdges(t, tt)
		if !within(right, tc.wantRight) || !within(bottom, tc.wantBott) {
			t.Errorf("%s: toast ends at (%v, %v), want (%v, %v)", tc.name, right, bottom, tc.wantRight, tc.wantBott)
		}
	}
}

// TestToastPlacementBarGone: once the bar stops registering, the toast
// falls back to the panel's corner.
func TestToastPlacementBarGone(t *testing.T) {
	s := &toastScene{bar: true, fieldWidth: 300, toasts: []ui.Toast{closeable("Saved")}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	if _, bottom := toastEdges(t, tt); bottom > 600-60-8+1.5 {
		t.Fatalf("toast bottom %v, want it above the bar", bottom)
	}
	s.bar = false
	settle(tt)
	if right, bottom := toastEdges(t, tt); right < 800-16-1.5 || right > 800-16+1.5 || bottom < 600-16-1.5 || bottom > 600-16+1.5 {
		t.Errorf("toast ends at (%v, %v), want the panel's corner (784, 584)", right, bottom)
	}
}

func TestToastWidth(t *testing.T) {
	s := &toastScene{toasts: []ui.Toast{closeable("Saved")}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	title, _ := tt.Find("Saved")
	right, _ := toastEdges(t, tt)
	if w := right - (title.X - 12); w < theme.ToastWidth-1.5 || w > theme.ToastWidth+1.5 {
		t.Errorf("toast is %v wide, want %d", w, theme.ToastWidth)
	}
}

// TestToastStack: three toasts sit behind one another showing the newest
// alone; the pointer over them spreads them out, the oldest on top, and
// leaving folds them again.
func TestToastStack(t *testing.T) {
	s := &toastScene{toasts: []ui.Toast{closeable("old"), closeable("mid"), closeable("new")}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	if tt.HasText("old") || tt.HasText("mid") || !tt.HasText("new") {
		t.Fatalf("collapsed stack shows %q, want the newest alone", tt.Texts())
	}
	front, _ := tt.Find("new")
	tt.Move(front.X+front.W/2, front.Y+front.H/2)
	tt.Advance(2 * theme.MotionBase)
	old, ok1 := tt.Find("old")
	mid, ok2 := tt.Find("mid")
	newest, ok3 := tt.Find("new")
	if !ok1 || !ok2 || !ok3 || !(old.Y < mid.Y && mid.Y < newest.Y) {
		t.Fatalf("hovered stack shows %q at %v %v %v, want all three, the oldest on top", tt.Texts(), old, mid, newest)
	}
	tt.Move(old.X, (old.Y+mid.Y)/2+old.H/2) // in the gap between two toasts
	tt.Advance(2 * theme.MotionBase)
	if !tt.HasText("old") {
		t.Fatalf("stack folded with the pointer in its gap: %q", tt.Texts())
	}
	tt.Move(5, 5)
	tt.Advance(2 * theme.MotionBase)
	if tt.HasText("old") || tt.HasText("mid") {
		t.Errorf("stack still open after the pointer left: %q", tt.Texts())
	}
}

// TestToastStackKeyboard: the focus coming into the stack spreads it, Escape
// folds it (closing no toast) and Enter spreads it again.
func TestToastStackKeyboard(t *testing.T) {
	s := &toastScene{toasts: []ui.Toast{closeable("old"), closeable("new")}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	tt.Key(0, ui.KeyTab)
	tt.Advance(2 * theme.MotionBase)
	if !tt.HasText("old") || !tt.HasText("new") {
		t.Fatalf("Tab into the stack left it folded: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Advance(2 * theme.MotionBase)
	if tt.HasText("old") || !tt.HasText("new") {
		t.Errorf("Escape left %q, want the stack folded and the newest toast open", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Advance(2 * theme.MotionBase)
	if !tt.HasText("old") {
		t.Errorf("Enter left the stack folded: %q", tt.Texts())
	}
}

// TestToastKeepsPageLive: a toast is not modal, so web views under it stay
// live rather than stills (WebView).
func TestToastKeepsPageLive(t *testing.T) {
	s := &toastScene{toasts: []ui.Toast{closeable("Saved")}}
	var f *frost
	tt := ui.NewTester(themed(func(c *ui.Context) { NewFrame(c); f = frostOf(c); s.view(c) }), 800, 600)
	settle(tt)
	if !tt.HasText("Saved") {
		t.Fatalf("no toast: %q", tt.Texts())
	}
	if f.now {
		t.Error("a toast froze the page's web views")
	}
}

// TestToastActionDeadline: the action goes at its deadline with no input,
// while the toast, held by the pointer, stays past its own time.
func TestToastActionDeadline(t *testing.T) {
	acted := 0
	s := &toastScene{toasts: []ui.Toast{{ID: "gone", Title: "Archived", Action: "Undo", OnAction: func() { acted++ }}}}
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if s.added == 0 {
			s.toasts[0].Data = ToastData{Deadline: c.Now().Add(2 * time.Second)}
		}
		s.view(c)
	}), 800, 600)
	settle(tt)
	title, _ := tt.Find("Archived")
	tt.Move(title.X, title.Y)
	tt.Advance(time.Second)
	if !tt.HasText("Undo") {
		t.Fatalf("no action before its deadline: %q", tt.Texts())
	}
	tt.Advance(5 * time.Second)
	if tt.HasText("Undo") || !tt.HasText("Archived") {
		t.Errorf("after the deadline, hovered: %q, want the toast without its action", tt.Texts())
	}
	if acted != 0 {
		t.Errorf("action ran %d times unasked", acted)
	}
}

func TestToastActionRunsOnce(t *testing.T) {
	acted := 0
	s := &toastScene{toasts: []ui.Toast{{ID: "once", Title: "Archived", Action: "Undo", OnAction: func() { acted++ }}}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	if err := tt.Click("Undo"); err != nil {
		t.Fatal(err)
	}
	_ = tt.Click("Undo")
	if acted != 1 {
		t.Errorf("action ran %d times, want 1", acted)
	}
}

func TestToastCloseButton(t *testing.T) {
	s := &toastScene{toasts: []ui.Toast{{ID: "timed", Title: "Timed"}}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	tt.Frame()
	if tt.HasText("Close") {
		t.Errorf("a toast that goes by itself has a close button: %q", tt.Texts())
	}
	s2 := &toastScene{toasts: []ui.Toast{closeable("Stays")}}
	tt2 := ui.NewTester(themed(s2.view), 800, 600)
	settle(tt2)
	if !tt2.HasText("Close") {
		t.Errorf("a toast that stays has no close button: %q", tt2.Texts())
	}
}

func TestToastIcon(t *testing.T) {
	tk := theme.Light()
	for typ, want := range map[string]string{"": "", ToastSuccess: "circle-check", ToastWarning: "triangle-alert", ToastError: "circle-x"} {
		if got, _ := toastIcon(tk, typ); got != want {
			t.Errorf("toastIcon(%q) = %q, want %q", typ, got, want)
		}
	}
}

// TestToastFrontStays: the newest toast does not move as the stack spreads,
// and the older ones open above it, as wide as it, a gap apart.
func TestToastFrontStays(t *testing.T) {
	s := &toastScene{bar: true, fieldWidth: 360, toasts: []ui.Toast{
		closeable("old"), closeable("mid"), {ID: "new", Title: "new", Action: "Undo", Timeout: -1},
	}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	undo, _ := tt.Find("Undo")
	title, _ := tt.Find("new")
	tt.Move(title.X+1, title.Y+title.H/2)
	for range 4 {
		tt.Advance(2 * theme.MotionBase)
	}
	if got, _ := tt.Find("Undo"); got != undo {
		t.Errorf("the action moved from %v to %v as the stack opened", undo, got)
	}
	if got, _ := tt.Find("new"); got != title {
		t.Errorf("the title moved from %v to %v as the stack opened", title, got)
	}
	old, _ := tt.Find("old")
	mid, _ := tt.Find("mid")
	if old.X != title.X || mid.X != title.X || !(old.Y < mid.Y && mid.Y < title.Y) {
		t.Fatalf("opened stack: old %v, mid %v, new %v", old, mid, title)
	}
	if step := title.Y - mid.Y; !within(mid.Y-old.Y, step) {
		t.Errorf("toasts %v and %v apart, want the same", mid.Y-old.Y, step)
	}
	tt.Move(5, 5)
	for range 4 {
		tt.Advance(2 * theme.MotionBase)
	}
	if got, _ := tt.Find("Undo"); got != undo {
		t.Errorf("the action moved to %v as the stack folded, want %v", got, undo)
	}
}

// TestToastActionFromFolded: the click that opens the stack, since the
// pointer arrives with it, still reaches the front toast's action, once.
func TestToastActionFromFolded(t *testing.T) {
	acted := 0
	s := &toastScene{toasts: []ui.Toast{closeable("old"), {ID: "new", Title: "new", Action: "Undo", Timeout: -1, OnAction: func() { acted++ }}}}
	tt := ui.NewTester(themed(s.view), 800, 600)
	settle(tt)
	if err := tt.Click("Undo"); err != nil {
		t.Fatal(err)
	}
	if acted != 1 {
		t.Errorf("action ran %d times, want 1", acted)
	}
}

func within(got, want float32) bool { return got > want-1.5 && got < want+1.5 }

// TestToastStaysInANarrowWindow: a bar's field near the window's left (a
// 640-wide compose window with the Wit panel open, Send ending at 312) keeps
// the toast inside the window, its icon and first words shown.
func TestToastStaysInANarrowWindow(t *testing.T) {
	const text = "Couldn't send: the server closed the connection"
	added := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if !added {
			c.AddToast(ui.Toast{ID: "x", Title: text, Type: ToastError, Timeout: -1})
			added = true
		}
		ui.Column(c).Fill().Children(func() { ui.Box(c).Grow(1) })
		ToastBarRect(c, ui.Rect{X: 8, Y: 400, W: 320, H: 60}, ui.Rect{X: 200, Y: 412, W: 112, H: 32})
		ToastHost(c, "Close")
	}), 640, 480)
	settle(tt)
	tt.Frame()
	r, ok := tt.Find(text)
	if !ok || r.X < 16 {
		t.Fatalf("toast text at %+v (found %v), want it inside the window, 16 from its left or more", r, ok)
	}
}

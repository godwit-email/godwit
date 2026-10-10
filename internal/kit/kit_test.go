package kit

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

// themed runs body under the tokens the settings give, as the app does.
func themed(body func(c *ui.Context)) func(c *ui.Context) { return themedDensity("compact", body) }

func themedDensity(density string, body func(c *ui.Context)) func(c *ui.Context) {
	return func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(core.Settings{Theme: "system", Density: density, FontSize: 13, BodyFontSize: 14}, c.Theme().Dark))
		body(c)
	}
}

func TestKitButtonClick(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if Button(c, "Save", ButtonOpts{Variant: Default, Icon: "check"}).Clicked() {
			n++
		}
		if Button(c, "Delete", ButtonOpts{Variant: Destructive, Disabled: true}).Clicked() {
			n += 100
		}
	}), 300, 200)
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	_ = tt.Click("Delete")
	if n != 1 {
		t.Errorf("clicks = %d, want 1 (the disabled button must not click)", n)
	}
}

func TestKitIconButtonLabel(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if IconButton(c, "archive", "Archive", ButtonOpts{Variant: Ghost}).Clicked() {
			n++
		}
	}), 300, 200)
	if _, ok := tt.Find("Archive"); !ok {
		t.Fatalf("no element labelled Archive in %q", tt.Texts())
	}
	if err := tt.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("clicks = %d", n)
	}
}

func TestKitSwitchToggles(t *testing.T) {
	on, checked := false, false
	sel := "a"
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Switch(c, &on).Label("Notify")
		Checkbox(c, &checked, "Remember")
		Radio(c, &sel, "a", "First")
		Radio(c, &sel, "b", "Second")
	}), 300, 200)
	for _, l := range []string{"Notify", "Remember", "Second"} {
		if err := tt.Click(l); err != nil {
			t.Fatal(err)
		}
	}
	if !on || !checked || sel != "b" {
		t.Errorf("switch %v, checkbox %v, radio %q", on, checked, sel)
	}
	if err := tt.Click("Notify"); err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("the switch did not toggle back")
	}
}

func TestKitSelectChooses(t *testing.T) {
	sel := 1
	opts := []Option[int]{{1, "Alpha"}, {2, "Beta"}, {3, "Gamma"}}
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Select(c, &sel, opts).Label("Letter")
	}), 300, 300)
	if !tt.HasText("Alpha") || tt.HasText("Beta") {
		t.Fatalf("closed select shows %q", tt.Texts())
	}
	if err := tt.Click("Letter"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Beta"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	if sel != 2 {
		t.Errorf("selected %d, want 2", sel)
	}
}

func TestKitDialogEscapeCloses(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Dialog(c, &open, "Rename", func() { ui.Text(c, "Body") }, func() { Button(c, "OK", ButtonOpts{}) })
	}), 800, 600)
	if !tt.HasText("Rename") || !tt.HasText("Body") || !tt.HasText("OK") {
		t.Fatalf("open dialog shows %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	if open || tt.HasText("Rename") {
		t.Errorf("after Escape open=%v, texts %q", open, tt.Texts())
	}
}

func TestKitDarkRenders(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Background(theme.Of(c).Window).Children(func() {
			Card(c, func() { ui.Text(c, "Inside") })
		})
	}), 300, 200)
	tt.SetDark(true)
	tt.Frame()
	r, ok := tt.Find("Inside")
	if !ok {
		t.Fatal("no card text")
	}
	want := theme.Dark().Background.SRGB()
	got := tt.Image().RGBAAt(int(r.X)-8, int(r.Y+r.H/2)) // in the card's padding
	if d := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); d > 2 {
		t.Errorf("card pixel %v, want %v", got, want)
	}
	if edge := tt.Image().RGBAAt(5, 5); edge.R == got.R {
		t.Errorf("the window color %v is the card's", edge)
	}
}

func diff(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }

// heights renders body under settings st and returns the heights of the named elements.
func heights(t *testing.T, st core.Settings, body func(c *ui.Context), names ...string) []float32 {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(st, false))
		ui.Column(c).Gap(8).AlignItems(ui.Start).Children(func() { body(c) })
	}, 600, 600)
	var hs []float32
	for _, n := range names {
		r, ok := tt.Find(n)
		if !ok {
			t.Fatalf("no %q in %q", n, tt.Texts())
		}
		hs = append(hs, r.H)
	}
	return hs
}

func TestKitDensityScales(t *testing.T) {
	sel, s := 1, ""
	body := func(c *ui.Context) {
		IconButton(c, "archive", "Archive", ButtonOpts{Variant: Ghost})
		Select(c, &sel, []Option[int]{{1, "Alpha"}}).Label("Letter")
		Input(c, &s, InputOpts{}).Label("Name")
		Card(c, func() {}).Label("Box")
	}
	st := core.Settings{Theme: "light", Density: "compact", FontSize: 13, BodyFontSize: 14}
	compact := heights(t, st, body, "Archive", "Letter", "Name", "Box")
	st.Density = "comfortable"
	roomy := heights(t, st, body, "Archive", "Letter", "Name", "Box")
	for i, n := range []string{"Archive", "Letter", "Name", "Box"} {
		if want := compact[i] * 1.25; roomy[i] < want-0.5 || roomy[i] > want+0.5 {
			t.Errorf("%s: compact %v, comfortable %v, want %v", n, compact[i], roomy[i], want)
		}
	}
	if compact[0] != 32 || compact[2] != 32 {
		t.Errorf("compact button %v, input %v, want 32", compact[0], compact[2])
	}
}

func TestKitFontSizeFollows(t *testing.T) {
	body := func(c *ui.Context) {
		Badge(c, "BadgeText")
		Alert(c, Warning, "AlertText", nil)
		Field(c, "FieldLabel", "HelpText", "", func() {})
		Kbd(c, "KbdText")
	}
	names := []string{"BadgeText", "AlertText", "HelpText", "KbdText"}
	st := core.Settings{Theme: "light", Density: "compact", FontSize: 13, BodyFontSize: 14}
	small := heights(t, st, body, names...)
	st.FontSize = 18
	big := heights(t, st, body, names...)
	for i, n := range names {
		if big[i] <= small[i] {
			t.Errorf("%s: %v at 13, %v at 18", n, small[i], big[i])
		}
	}
}

func TestKitToastCloseLabel(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if c.Shortcut(0, ui.KeyT) {
			c.AddToast(ui.Toast{Title: "Saved", Timeout: -1})
		}
		ToastHost(c, "关闭")
	}), 400, 300)
	tt.Key(0, ui.KeyT)
	if !tt.HasText("Saved") {
		t.Fatalf("no toast in %q", tt.Texts())
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("Saved") {
		t.Error("the close button did not close the toast")
	}
}

func TestKitSidebarItem(t *testing.T) {
	var clicked []string
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Width(240).Children(func() {
			for _, name := range []string{"Inbox", "Sent"} {
				SidebarItem(c, SidebarItemOpts{Selected: name == "Inbox", OnClick: func() { clicked = append(clicked, name) }}).
					Children(func() { ui.Text(c, name) })
			}
		})
	}), 300, 200)
	for _, name := range []string{"Inbox", "Sent"} {
		r, ok := tt.Find(name)
		if !ok || r.X > 24 {
			t.Errorf("%s at %+v, want left-aligned", name, r)
		}
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
	}
	if len(clicked) != 1 || clicked[0] != "Sent" {
		t.Errorf("clicked %v, want [Sent] (the selected row does not click)", clicked)
	}
}

func TestKitButtonSizes(t *testing.T) {
	for _, density := range []string{"compact", "comfortable"} {
		var th *ui.Theme
		tt := ui.NewTester(themedDensity(density, func(c *ui.Context) {
			th = c.Theme()
			ui.Column(c).Gap(4).Children(func() {
				for _, k := range []struct {
					l string
					s ButtonSize
				}{{"probe-sm", SizeSm}, {"probe-def", SizeDefault}, {"probe-lg", SizeLg}} {
					ButtonFrame(c, ButtonOpts{Size: k.s}).Label(k.l).Children(func() { ui.Text(c, "x") })
				}
				IconButton(c, "check", "probe-xs", ButtonOpts{Size: SizeXs})
			})
		}), 300, 300)
		for l, want := range map[string]float32{"probe-sm": th.Space(7), "probe-def": th.Space(8), "probe-lg": th.Space(9), "probe-xs": th.Space(4)} {
			r, ok := tt.Find(l)
			if !ok {
				t.Fatalf("%s: %s not found", density, l)
			}
			if r.H != want {
				t.Errorf("%s: %s height = %v, want %v", density, l, r.H, want)
			}
			if l == "probe-xs" && r.W != want {
				t.Errorf("%s: xs width = %v, want %v", density, r.W, want)
			}
		}
	}
}

func TestKitLinkHasNoBox(t *testing.T) {
	var th *ui.Theme
	tt := ui.NewTester(themed(func(c *ui.Context) {
		th = c.Theme()
		ui.Column(c).Gap(4).Children(func() {
			ButtonFrame(c, ButtonOpts{Variant: Link}).Label("probe-link").Children(func() { ui.Text(c, "Learn more") })
			ui.Text(c, "Learn more").Label("probe-plain")
		})
	}), 300, 200)
	l, ok1 := tt.Find("probe-link")
	p, ok2 := tt.Find("probe-plain")
	if !ok1 || !ok2 {
		t.Fatal("probes not found")
	}
	if l.H >= th.Space(7) || l.H != p.H {
		t.Errorf("link height = %v, plain text = %v, want equal and < %v", l.H, p.H, th.Space(7))
	}
}

func TestKitButtonDisabledInert(t *testing.T) {
	var nb, ni, ne int
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Gap(8).Children(func() {
			if Button(c, "Off", ButtonOpts{Variant: Outline, Disabled: true}).Clicked() {
				nb++
			}
			if IconButton(c, "check", "probe-off-icon", ButtonOpts{Variant: Outline, Disabled: true}).Clicked() {
				ni++
			}
			if Button(c, "On", ButtonOpts{Variant: Outline}).Clicked() {
				ne++
			}
		})
	}), 300, 300)
	probe := func(label string) (before, after [4]uint8) {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("%s not found", label)
		}
		x, y := int(r.X)+2, int(r.Y+r.H/2)
		px := func() [4]uint8 { c := tt.Image().RGBAAt(x, y); return [4]uint8{c.R, c.G, c.B, c.A} }
		tt.Frame()
		before = px()
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		for start := time.Now(); time.Since(start) < 300*time.Millisecond; time.Sleep(10 * time.Millisecond) {
			tt.Frame()
		}
		return before, px()
	}
	for _, l := range []string{"Off", "probe-off-icon"} {
		if b, a := probe(l); b != a {
			t.Errorf("%s: pixel changed on hover %v -> %v", l, b, a)
		}
		_ = tt.Click(l)
	}
	if b, a := probe("On"); b == a {
		t.Error("enabled button pixel did not change on hover; probe is insensitive")
	}
	_ = tt.Click("On")
	if nb != 0 || ni != 0 || ne != 1 {
		t.Errorf("clicks = %d %d %d, want 0 0 1", nb, ni, ne)
	}
}

func TestKitSelectedDoesNotClick(t *testing.T) {
	var got []string
	for _, sel := range []bool{false, true} {
		sel := sel
		tag := "off"
		if sel {
			tag = "on"
		}
		tt := ui.NewTester(themed(func(c *ui.Context) {
			ui.Column(c).Children(func() {
				Pressable(c, PressableOpts{Selected: sel, OnClick: func() { got = append(got, "p-"+tag) }}).Label("p-" + tag).Height(30).Children(func() { ui.Text(c, "pp") })
				SidebarItem(c, SidebarItemOpts{Selected: sel, OnClick: func() { got = append(got, "s-"+tag) }}).Label("s-" + tag).Height(30).Children(func() { ui.Text(c, "ss") })
				RailButton(c, "check", "r-"+tag, sel, func() { got = append(got, "r-"+tag) })
			})
		}), 300, 300)
		for _, l := range []string{"p-", "s-", "r-"} {
			if err := tt.Click(l + tag); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(got) != 3 || got[0] != "p-off" || got[1] != "s-off" || got[2] != "r-off" {
		t.Errorf("clicks = %v, want only the three unselected", got)
	}
}

func TestKitTooltipOnFocus(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		b := Button(c, "Save", ButtonOpts{})
		if b.Clicked() {
			n++
		}
		Tooltip(c, b, "Saves the draft")
	}), 300, 200)
	if tt.HasText("Saves the draft") {
		t.Fatal("tip shows before focus")
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !tt.HasText("Saves the draft") {
		t.Error("tip does not show on keyboard focus")
	}
	if err := tt.Click("Save"); err != nil || n != 1 {
		t.Errorf("click err=%v n=%d, want the button to fire", err, n)
	}
}

func TestKitPressableLeftAligned(t *testing.T) {
	var gap float32
	tt := ui.NewTester(themed(func(c *ui.Context) {
		gap = c.Theme().Space(4)
		Pressable(c, PressableOpts{}).Label("pl-row").Width(240).Children(func() { ui.Text(c, "Row") })
	}), 400, 200)
	box, ok1 := tt.Find("pl-row")
	txt, ok2 := tt.Find("Row")
	if !ok1 || !ok2 {
		t.Fatal("not found")
	}
	if d := txt.X - box.X; d < 0 || d > gap {
		t.Errorf("text %v from the left edge, want near 0", d)
	}
}

func TestKitAvatarInitial(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			Avatar(c, "lena fischer")
			Avatar(c, "")
			Avatar(c, "\ufffdname")
		})
	}), 200, 200)
	tt.Frame()
	if !tt.HasText("L") || !tt.HasText("?") || !tt.HasText("\ufffd") {
		t.Errorf("texts %q", tt.Texts())
	}
	if _, ok := tt.Find("lena fischer"); !ok {
		t.Error("avatar not found by its label")
	}
}

func TestKitSeparatorSizes(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Width(200).Children(func() {
			Separator(c, false).Label("sep-h")
			Separator(c, true).Label("sep-v")
		})
		ui.Row(c).Width(200).Children(func() {
			Separator(c, false).Grow(1).Label("row-a")
			ui.Text(c, "or").Label("row-or")
			Separator(c, false).Grow(1).Label("row-b")
		})
	}), 300, 200)
	tt.Frame()
	h, _ := tt.Find("sep-h")
	v, _ := tt.Find("sep-v")
	if h.W != 200 || h.H != theme.Hairline {
		t.Errorf("horizontal %vx%v", h.W, h.H)
	}
	if v.W != theme.Hairline {
		t.Errorf("vertical width %v", v.W)
	}
	a, _ := tt.Find("row-a")
	b, _ := tt.Find("row-b")
	or, _ := tt.Find("row-or")
	if a.W <= 0 || a.W != b.W || or.W <= 0 || a.W+b.W+or.W != 200 {
		t.Errorf("row widths %v %v %v", a.W, or.W, b.W)
	}
}

func TestKitSkeletonStillUnderReduceMotion(t *testing.T) {
	for _, reduce := range []bool{true, false} {
		tt := ui.NewTester(themed(func(c *ui.Context) {
			ui.Column(c).Padding(20).Children(func() { Skeleton(c, 100, 40).Label("skel") })
		}), 200, 100)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		r, ok := tt.Find("skel")
		if !ok {
			t.Fatal("skeleton not found")
		}
		x, y := int(r.X+r.W/2), int(r.Y+r.H/2)
		px := func() [4]uint8 { c := tt.Image().RGBAAt(x, y); return [4]uint8{c.R, c.G, c.B, c.A} }
		before := px()
		time.Sleep(300 * time.Millisecond)
		tt.Frame()
		if same := before == px(); same != reduce {
			t.Errorf("reduce=%v: pixel unchanged=%v", reduce, same)
		}
	}
}

func TestKitCollapsibleToggles(t *testing.T) {
	st := &struct{ open bool }{}
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Collapsible(c, &st.open, func() { ui.Text(c, "More") }, func() { ui.Text(c, "inside") })
	}), 400, 300)
	if tt.HasText("inside") {
		t.Fatal("panel shown while closed")
	}
	if err := tt.Click("More"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("inside") {
		t.Fatalf("panel not shown after click: %q", tt.Texts())
	}
	_ = tt.Click("More")
	for start := time.Now(); tt.HasText("inside") && time.Since(start) < 2*time.Second; time.Sleep(10 * time.Millisecond) {
		tt.Frame()
	}
	if tt.HasText("inside") {
		t.Error("panel still shown after closing")
	}
}

func TestKitSheetCloses(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Sheet(c, &open, "Add", "Close", func() { ui.Text(c, "inside") })
	}), 800, 600)
	if !tt.HasText("inside") || !tt.HasText("Add") {
		t.Fatalf("open sheet shows %q", tt.Texts())
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	if open || tt.HasText("inside") {
		t.Errorf("after Close open=%v, texts %q", open, tt.Texts())
	}
}

func TestKitBareInputMatchesBase(t *testing.T) {
	a, b, d := "", "", ""
	var th theme.Tokens
	tt := ui.NewTester(themed(func(c *ui.Context) {
		th = theme.Of(c)
		ui.Column(c).FillWidth().Background(th.Sidebar).Gap(8).Padding(8).Children(func() {
			Input(c, &a, InputOpts{Bare: true}).Label("bare")
			ui.TextInputBase(c, &b).Label("base")
			Input(c, &d, InputOpts{}).Label("normal")
		})
	}), 400, 300)
	rb, ok1 := tt.Find("bare")
	rr, ok2 := tt.Find("base")
	rn, ok3 := tt.Find("normal")
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("missing inputs in %q", tt.Texts())
	}
	if rb.H != rr.H {
		t.Errorf("bare height %v, base %v", rb.H, rr.H)
	}
	px := func(r ui.Rect) [3]uint8 {
		c := tt.Image().RGBAAt(int(r.X), int(r.Y+r.H/2))
		return [3]uint8{c.R, c.G, c.B}
	}
	rgb := func(c ui.Color) [3]uint8 { return [3]uint8{c.R, c.G, c.B} }
	if px(rb) != rgb(th.Sidebar) {
		t.Errorf("bare edge pixel %v, want sidebar %v", px(rb), rgb(th.Sidebar))
	}
	if px(rn) != rgb(th.Border) {
		t.Errorf("normal edge pixel %v, want border %v", px(rn), rgb(th.Border))
	}
}

func TestGalleryRendersInBothAppearances(t *testing.T) {
	for _, dark := range []bool{false, true} {
		var g GalleryState
		tt := ui.NewTester(themed(func(c *ui.Context) { Gallery(c, &g) }), 1200, 8000)
		tt.SetDark(dark)
		tt.Frame()
		for _, s := range []string{"Button", "IconButton", "Input", "TextArea", "Field", "Select", "Checkbox", "Radio", "Switch", "Tabs", "ToggleGroup",
			"Dialog", "Sheet", "Popover", "Tooltip", "Collapsible", "Card", "Badge", "Alert", "Avatar", "Separator", "Skeleton", "Spinner", "Kbd",
			"StatusDot", "SidebarItem", "RailButton", "Pressable", "Toast"} {
			if _, ok := tt.Find("section:" + s); !ok {
				t.Errorf("dark=%v: no heading %q", dark, s)
			}
		}
		for _, o := range [][2]string{{"Open Dialog", "Dialog sample text"}, {"Open Sheet", "Sheet sample text"}, {"Open Popover", "Popover sample text"}} {
			if err := tt.Click(o[0]); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !tt.HasText(o[1]) {
				t.Errorf("dark=%v: %s shows no %q", dark, o[0], o[1])
			}
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			if tt.HasText(o[1]) {
				t.Errorf("dark=%v: Escape left %q", dark, o[1])
			}
		}
	}
}

func TestGalleryKeepsTypedText(t *testing.T) {
	var g GalleryState
	tt := ui.NewTester(themed(func(c *ui.Context) { Gallery(c, &g) }), 1200, 8000)
	tt.Frame()
	r, ok := tt.Find("With help") // the control sits under its label
	if !ok {
		t.Fatal("no With help field")
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H+20)
	tt.Type("hello")
	tt.Frame()
	if g.HelpIn != "hello" {
		t.Errorf("HelpIn = %q", g.HelpIn)
	}
}

// TestKitOverlayEnters: a dialog's backdrop fades in over MotionBase, and is
// there at once under reduced motion.
func TestKitOverlayEnters(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		open := false
		tt := ui.NewTester(themed(func(c *ui.Context) {
			Dialog(c, &open, "Title", func() { ui.Text(c, "inside") }, nil)
		}), 400, 300)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		open = true
		tt.Frame()
		first := tt.Image().RGBAAt(2, 2)
		time.Sleep(2 * theme.MotionBase)
		tt.Frame()
		last := tt.Image().RGBAAt(2, 2)
		if moved := first != last; moved == reduce {
			t.Errorf("reduce motion %v: backdrop corner %v then %v", reduce, first, last)
		}
	}
}

// TestKitToastTopCenter: toasts show at the top center, the newest first.
func TestKitToastTopCenter(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		for ; n < 2; n++ {
			c.AddToast(ui.Toast{Title: []string{"older", "newer"}[n], Type: ToastSuccess, Timeout: -1})
		}
		ToastHost(c, "Close")
	}), 800, 600)
	tt.Frame()
	older, ok1 := tt.Find("older")
	newer, ok2 := tt.Find("newer")
	if !ok1 || !ok2 {
		t.Fatalf("toasts not shown: %q", tt.Texts())
	}
	if newer.Y >= older.Y || newer.Y > 120 {
		t.Errorf("newer at %v, older at %v: want the newest on top, at the top", newer, older)
	}
	if mid := newer.X + newer.W/2; mid < 300 || mid > 500 {
		t.Errorf("newer centered at x=%v, want near 400", mid)
	}
}

func TestKitToastIcons(t *testing.T) {
	tk := theme.Light()
	for typ, want := range map[string]string{"": "", ToastSuccess: "circle-check", ToastWarning: "triangle-alert", ToastError: "circle-x"} {
		if got := toastLook(tk, typ).icon; got != want {
			t.Errorf("toastLook(%q).icon = %q, want %q", typ, got, want)
		}
	}
}

// TestKitToastTinted: a kind's toast takes its color's tint; a plain one stays white.
func TestKitToastTinted(t *testing.T) {
	tk := theme.Light()
	for _, tc := range []struct {
		typ            string
		bg, border, fg ui.Color
	}{
		{"", tk.Background, tk.Border, tk.Foreground},
		{ToastSuccess, tk.SuccessBg, tk.SuccessBorder, tk.SuccessText},
		{ToastWarning, tk.WarningBg, tk.WarningBorder, tk.WarningText},
		{ToastError, tk.DangerBg, tk.DangerBorder, tk.DangerText},
	} {
		if l := toastLook(tk, tc.typ); l.bg != tc.bg || l.border != tc.border || l.fg != tc.fg {
			t.Errorf("toastLook(%q) = %v %v %v, want %v %v %v", tc.typ, l.bg, l.border, l.fg, tc.bg, tc.border, tc.fg)
		}
	}
}

// TestKitSplitQuietDivider: a hovered divider is the ring's gray, and the
// panes keep the frame's theme (their default button is the primary color).
func TestKitSplitQuietDivider(t *testing.T) {
	size := float32(160)
	var tk theme.Tokens
	tt := ui.NewTester(themed(func(c *ui.Context) {
		tk = theme.Of(c)
		Split(c, &size, func() {
			Button(c, "Left", ButtonOpts{})
		}, func() { ui.Text(c, "right") }).Fill()
	}), 400, 200)
	tt.Frame()
	d, ok := tt.Find("Divider")
	if !ok {
		t.Fatal("no divider")
	}
	x, y := d.X+d.W/2, d.Y+d.H/2
	tt.Move(x, y)
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
	px := tt.Image().RGBAAt(int(x), int(y))
	if want := tk.DividerHover; px.R != want.R || px.G != want.G || px.B != want.B {
		t.Errorf("hovered divider %v, want DividerHover %v", px, want)
	}
	b, _ := tt.Find("Left")
	px = tt.Image().RGBAAt(int(b.X)+2, int(b.Y+b.H/2))
	if want := tk.Primary; px.R != want.R || px.G != want.G || px.B != want.B {
		t.Errorf("left pane's button %v, want the primary %v", px, want)
	}
}

// TestKitSidebarIconMuted: a SidebarItem's icon is the muted gray, and turns
// the foreground on hover.
func TestKitSidebarIconMuted(t *testing.T) {
	var th theme.Tokens
	tt := ui.NewTester(themed(func(c *ui.Context) {
		th = theme.Of(c)
		ui.Column(c).Width(240).Children(func() {
			item := SidebarItem(c, SidebarItemOpts{OnClick: func() {}}).Label("new")
			item.Children(func() { SidebarIcon(c, item, "square-pen") })
		})
	}), 300, 100)
	r, ok := tt.Find("new")
	if !ok {
		t.Fatal("no item")
	}
	// darkest is the darkest pixel's red channel inside the icon's box
	darkest := func() uint8 {
		img, m := tt.Image(), uint8(255)
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X)+40; x++ {
				m = min(m, img.RGBAAt(x, y).R)
			}
		}
		return m
	}
	if got := darkest(); got < th.Muted.R {
		t.Errorf("icon darkest %d, want no darker than muted %d", got, th.Muted.R)
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	time.Sleep(2 * theme.MotionFast)
	tt.Frame()
	if got := darkest(); got >= th.Muted.R {
		t.Errorf("hovered icon darkest %d, want darker than muted %d", got, th.Muted.R)
	}
}

// TestKitListSelectedGray: a List's selected row is the sidebar's selected
// gray, not mygo's accent (the near-black primary).
func TestKitListSelectedGray(t *testing.T) {
	var th theme.Tokens
	var s ui.ListState
	cur := 0
	s.Selected = &cur
	tt := ui.NewTester(themed(func(c *ui.Context) {
		th = theme.Of(c)
		List(c, &s, 2, func(i int) { ui.Text(c, []string{"one", "two"}[i]).Padding(8) }).Height(100)
	}), 300, 120)
	r, ok := tt.Find("one")
	if !ok {
		t.Fatal("no row")
	}
	got := tt.Image().RGBAAt(int(r.X)+4, int(r.Y+r.H/2)) // in the text's padding
	if want := th.SidebarSelected; got.R != want.R || got.G != want.G || got.B != want.B {
		t.Errorf("selected row %v, want %v", got, want)
	}
}

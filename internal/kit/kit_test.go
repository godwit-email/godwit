package kit

import (
	"image"
	"image/color"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestKitAvatarImage(t *testing.T) {
	red := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			red.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	bitmap := ui.NewBitmap(red)
	svg, err := ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="#ff0000"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, density := range []string{"compact", "comfortable"} {
		var before ui.Rect
		for _, src := range []ui.ImageSource{nil, bitmap, svg} {
			tt := ui.NewTester(themedDensity(density, func(c *ui.Context) {
				ui.Column(c).Padding(8).Children(func() { AvatarImage(c, "Alice", src) })
			}), 100, 100)
			r, ok := tt.Find("Alice")
			want := float32(32)
			if density == "comfortable" {
				want = 40
			}
			if !ok || r.W != want || r.H != want {
				t.Fatalf("avatar bounds %+v, found %v", r, ok)
			}
			if src == nil {
				before = r
			} else if r != before {
				t.Fatalf("%s: layout %v -> %v", density, before, r)
			}
			if slices.Contains(tt.Texts(), "A") != (src == nil) {
				t.Fatalf("fallback texts %q", tt.Texts())
			}
			if src != nil {
				center := tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
				corner := tt.Image().RGBAAt(int(r.X), int(r.Y))
				if center.R < 250 || center.G > 5 || center.B > 5 {
					t.Errorf("brand color %v", center)
				}
				if corner.R > 250 && corner.G < 5 && corner.B < 5 {
					t.Errorf("corner not clipped: %v", corner)
				}
			}
		}
	}
}

// themed runs body under the tokens the settings give, as the app does.
func themed(body func(c *ui.Context)) func(c *ui.Context) { return themedDensity("compact", body) }

func themedDensity(density string, body func(c *ui.Context)) func(c *ui.Context) {
	return func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(core.Settings{Theme: "system", Density: density, FontSize: 14, BodyFontSize: 14}, c.Theme().Dark))
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
		Dialog(c, &open, "Rename", DialogOpts{Close: "Close"}, func() { ui.Text(c, "Body") }, func() { Button(c, "OK", ButtonOpts{}) })
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
	st := core.Settings{Theme: "light", Density: "compact", FontSize: 14, BodyFontSize: 14}
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
	st := core.Settings{Theme: "light", Density: "compact", FontSize: 14, BodyFontSize: 14}
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
		tt.Advance(300 * time.Millisecond)
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
				NavSwitch(c, NavItem{Icon: "mail", Label: "r-" + tag, Selected: sel, OnClick: func() { got = append(got, "r-"+tag) }})
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
	if h.W != 200 || h.H != theme.Stroke {
		t.Errorf("horizontal %vx%v", h.W, h.H)
	}
	if v.W != theme.Stroke {
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
		tt.Advance(300 * time.Millisecond)
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
	tt.Advance(2 * theme.MotionBase)
	if tt.HasText("inside") {
		t.Error("panel still shown after closing")
	}
}

func TestKitDialogCloseButton(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Dialog(c, &open, "Add", DialogOpts{Desc: "desc", Close: "Close"}, func() { ui.Text(c, "inside") }, nil)
	}), 800, 600)
	if !tt.HasText("inside") || !tt.HasText("Add") || !tt.HasText("desc") {
		t.Fatalf("open dialog shows %q", tt.Texts())
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	if open || tt.HasText("inside") {
		t.Errorf("after Close open=%v, texts %q", open, tt.Texts())
	}
}

// TestDialogScrims: as macOS, a dialog dims the window with black at 20% in
// either appearance; an alert washes it with gray at 60%, light or dark.
func TestDialogScrims(t *testing.T) {
	for _, c := range []struct {
		alert, dark bool
		want        uint8 // over the near-black (#0a) column
	}{
		{false, false, 0x0a * 8 / 10}, {false, true, 0x0a * 8 / 10},
		{true, false, 107}, {true, true, 18}, // 60% of #ab or #17 over #0a
	} {
		open := true
		tt := ui.NewTester(themed(func(ctx *ui.Context) {
			ui.Row(ctx).Fill().Children(func() {
				ui.Box(ctx).Width(200).FillHeight().Background(theme.Light().Foreground) // #0a0a0a
				ui.Box(ctx).Grow(1).FillHeight().Background(theme.Light().Background)    // #fff
			})
			Dialog(ctx, &open, "T", DialogOpts{Size: DialogSm, Close: "Close", Alert: c.alert}, nil, nil)
		}), 800, 600)
		tt.SetDark(c.dark)
		tt.Advance(2 * theme.MotionBase)
		img := tt.Image()
		if p := img.RGBAAt(100, 580); diff(p.R, c.want) > 2 {
			t.Errorf("alert %v dark %v: backdrop over black %v, want %d", c.alert, c.dark, p, c.want)
		}
		if p, want := img.RGBAAt(700, 580), uint8(204); !c.alert && diff(p.R, want) > 2 {
			t.Errorf("dark %v: dialog backdrop over white %v, want %d", c.dark, p, want)
		}
	}
}

// A frosted panel's tint keeps to its rounded corners: just inside the
// corner of its box the backdrop shows, not the panel's fill.
func TestDialogGlassCorners(t *testing.T) {
	Glass = true
	t.Cleanup(func() { Glass = false })
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Box(c).Fill().Background(theme.Light().Background)
		Dialog(c, &open, "Corner", DialogOpts{Size: DialogSm, Close: "Close"}, nil, nil)
	}), 800, 600)
	tt.Advance(2 * theme.MotionBase)
	r, ok := tt.Find("Corner")
	if !ok {
		t.Fatalf("texts %q", tt.Texts())
	}
	// The title sits 16 in from the panel's top left; 2 in from that corner is outside its radius.
	if p := tt.Image().RGBAAt(int(r.X)-16+2, int(r.Y)-16+2); diff(p.R, 204) > 3 {
		t.Errorf("panel corner pixel %v, want the backdrop's 204", p)
	}
}

// An alert asks one question: Cancel, Escape or the backdrop close it, no ×.
func TestDialogAlertHasNoClose(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Dialog(c, &open, "Delete?", DialogOpts{Size: DialogSm, Close: "Close", Alert: true}, nil, func() { Button(c, "Cancel", ButtonOpts{}) })
	}), 800, 600)
	if !tt.HasText("Delete?") {
		t.Fatalf("open alert shows %q", tt.Texts())
	}
	if err := tt.Click("Close"); err == nil {
		t.Error("an alert has a close button")
	}
}

// A dialog sits a little above the middle: its centre at 40% of the window.
func TestDialogAboveCentre(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Dialog(c, &open, "Top", DialogOpts{Size: DialogSm, Close: "Close"}, func() { ui.Box(c).Height(200) }, func() { Button(c, "Bottom", ButtonOpts{}) })
	}), 800, 1000)
	tt.Advance(2 * theme.MotionBase)
	top, ok1 := tt.Find("Top")
	bottom, ok2 := tt.Find("Bottom")
	if !ok1 || !ok2 {
		t.Fatalf("texts %q", tt.Texts())
	}
	if mid := (top.Y + bottom.Y + bottom.H) / 2; mid < 380 || mid > 420 {
		t.Errorf("panel centre near %v, want about 400 of 1000", mid)
	}
}

// A dialog opens where it stays: its first frame already sits above centre,
// not centred and then moved (the window's height is known before it opens).
func TestDialogOpensInPlace(t *testing.T) {
	open := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Box(c).Fill()
		Dialog(c, &open, "Steady", DialogOpts{Size: DialogSm, Close: "Close", Alert: true}, nil, nil)
	}), 800, 1000)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true}) // no fade or rise: only the placement moves it
	tt.Frame()
	open = true
	tt.Frame()
	first, ok := tt.Find("Steady")
	if !ok {
		t.Fatalf("texts %q", tt.Texts())
	}
	tt.Frame()
	tt.Frame()
	if then, _ := tt.Find("Steady"); then.Y != first.Y {
		t.Errorf("the dialog opened at y %v and moved to %v", first.Y, then.Y)
	}
}

// A Top dialog keeps its top 64 below the window's, whatever its height: a
// form whose steps change it grows down instead of moving.
func TestDialogTop(t *testing.T) {
	for _, h := range []float32{100, 400} {
		open := true
		tt := ui.NewTester(themed(func(c *ui.Context) {
			Dialog(c, &open, "Pinned", DialogOpts{Close: "Close", Top: true}, func() { ui.Box(c).Height(h) }, nil)
		}), 800, 1000)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.Frame()
		tt.Frame()
		r, ok := tt.Find("Pinned")
		if !ok {
			t.Fatalf("texts %q", tt.Texts())
		}
		if top := r.Y - 16; top < 62 || top > 66 { // the title sits 16 inside the panel
			t.Errorf("body %v: panel top near %v, want 64", h, top)
		}
	}
}

// A selected palette row keeps the foreground for its text: its light tint
// is no system selection, whose inverted (white) text would vanish on it.
func TestPaletteRowSelectedText(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(8).Background(theme.Of(c).Background).Children(func() {
			PaletteRow(c, true).Children(func() { ui.Text(c, "Hit") })
		})
	}), 300, 100)
	tt.Frame()
	r, ok := tt.Find("Hit")
	if !ok {
		t.Fatalf("texts %q", tt.Texts())
	}
	img, darkest := tt.Image(), 255
	for x := int(r.X); x < int(r.X+r.W); x++ {
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			darkest = min(darkest, int(img.RGBAAt(x, y).R))
		}
	}
	if darkest > 80 {
		t.Errorf("the selected row's text is no darker than %d: not the foreground", darkest)
	}
}

func TestShadowX2l(t *testing.T) {
	if len(theme.ShadowX2l) != 1 || theme.ShadowX2l[0] != (theme.ShadowLayer{Y: 25, Blur: 50, Spread: -12, Alpha: 0.25}) {
		t.Errorf("ShadowX2l %v is not Tailwind's shadow-2xl", theme.ShadowX2l)
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
		tt.Advance(2 * theme.MotionFast)
		for _, s := range []string{"Button", "IconButton", "Input", "TextArea", "Field", "Select", "Checkbox", "Radio", "Switch", "Tabs", "ToggleGroup",
			"Dialog", "Confirm", "Popover", "Tooltip", "Collapsible", "Card", "Badge", "Alert", "Avatar", "Separator", "Skeleton", "Spinner", "Kbd",
			"StatusDot", "SidebarItem", "NavSwitch", "Panel", "Pressable", "Toast"} {
			if _, ok := tt.Find("section:" + s); !ok {
				t.Errorf("dark=%v: no heading %q", dark, s)
			}
		}
		for _, o := range [][2]string{{"Open Dialog", "Dialog sample text"}, {"Open Confirm", "Confirm sample text"}, {"Open Popover", "Popover sample text"}} {
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
	// One Tester frame settles up to 20 frames, which a loaded machine can
	// run past MotionBase in, so the fade runs on a clock the test moves.
	now := time.Now()
	clock = func(*ui.Context) time.Time { return now }
	t.Cleanup(func() { clock = (*ui.Context).Now })
	for _, reduce := range []bool{false, true} {
		open := false
		tt := ui.NewTester(themed(func(c *ui.Context) {
			Dialog(c, &open, "Title", DialogOpts{Close: "Close"}, func() { ui.Text(c, "inside") }, nil)
		}), 400, 300)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		open = true
		tt.Frame()
		first := tt.Image().RGBAAt(2, 2)
		now = now.Add(2 * theme.MotionBase)
		tt.Frame()
		last := tt.Image().RGBAAt(2, 2)
		if moved := first != last; moved == reduce {
			t.Errorf("reduce motion %v: backdrop corner %v then %v", reduce, first, last)
		}
	}
}

// TestKitToastTopCenter: toasts show at the top center, the oldest first.
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
	if older.Y >= newer.Y || older.Y > 120 {
		t.Errorf("newer at %v, older at %v: want the oldest on top, at the top", newer, older)
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

// TestKitResizerPill: at rest the gap is clear; hovered, its middle is the
// grip's line, the foreground at 20%; dragging it moves *size.
func TestKitResizerPill(t *testing.T) {
	size := float32(160)
	changed := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() {
			if ch, _ := Resizer(c, &size, 8, false, "Resize", nil, func() { ui.Text(c, "left") }, func() { ui.Text(c, "right") }); ch {
				changed = true
			}
		})
	}), 400, 200)
	tt.Frame()
	g, ok := tt.Find("Resize")
	if !ok {
		t.Fatal("no grip")
	}
	x, y := g.X+g.W/2, g.Y+g.H/2
	rest := tt.Image().RGBAAt(int(x), int(y))
	tt.Move(x, y)
	tt.Frame()
	if px := tt.Image().RGBAAt(int(x), int(y)); int(px.R)+int(px.G)+int(px.B) > int(rest.R)+int(rest.G)+int(rest.B)-3*20 {
		t.Errorf("hovered grip %v, at rest %v: want the darker line", px, rest)
	}
	tt.Press(x, y)
	tt.Move(x+20, y)
	tt.Move(x+40, y)
	tt.Release(x+40, y)
	tt.Frame()
	if !changed || size < 199 || size > 201 {
		t.Errorf("dragged 40: size %v (changed %v), want 200", size, changed)
	}
}

// TestKitResizerOver: over, the panels touch and the grip rides second's
// edge, 1 into each, taking the pointer there.
func TestKitResizerOver(t *testing.T) {
	size := float32(160)
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() {
			Resizer(c, &size, 3, true, "Resize", nil, func() { ui.Box(c).Fill().Label("first") }, func() { ui.Box(c).Fill().Label("second") })
		})
	}), 400, 200)
	tt.Frame()
	first, _ := tt.Find("first")
	grip, _ := tt.Find("Resize")
	second, _ := tt.Find("second")
	if first.X != 0 || second.X != 160 || grip.X != 159 {
		t.Errorf("first %v, grip %v, second %v: want them touching at 160, the grip from 159, on second's border", first, grip, second)
	}
	tt.Press(160, 100)
	tt.Move(180, 100)
	tt.Release(180, 100)
	tt.Frame()
	if size < 179 || size > 181 {
		t.Errorf("dragged 20 by first's edge: size %v, want 180", size)
	}
	if _, ok := tt.Find("Resize"); !ok {
		t.Fatal("grip gone")
	}
	tt.Press(190, 100) // second's, past the grip
	tt.Move(205, 100)
	tt.Release(205, 100)
	tt.Frame()
	if size < 179 || size > 181 {
		t.Errorf("a drag in second moved the grip: size %v", size)
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
			item.Children(func() { SidebarIcon(c, item, "square-pen", false) })
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
	tt.Advance(2 * theme.MotionFast)
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

// TestKitNavSwitchFillsSelected: the page shown in a NavSwitch has its
// icon's fill style, which inks much more of the button than the line style.
func TestKitNavSwitchFillsSelected(t *testing.T) {
	ink := func(sel bool) int {
		tt := ui.NewTester(themed(func(c *ui.Context) { NavSwitch(c, NavItem{Icon: "mail", Label: "r", Selected: sel}) }), 100, 100)
		r, ok := tt.Find("r")
		if !ok {
			t.Fatal("no button")
		}
		n, img := 0, tt.Image()
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W); x++ {
				if img.RGBAAt(x, y).R < 220 { // any ink, muted or not
					n++
				}
			}
		}
		return n
	}
	// The line icon inks 176 px on macOS and 200 on Windows (heavier antialiasing);
	// the fill icon 270 on both, a line icon in the selected colour about as few.
	if off, on := ink(false), ink(true); on < off*6/5 {
		t.Errorf("selected inks %d px, unselected %d: want the fill style", on, off)
	}
}

// TestKitSidebarIconFillsSelected: a selected row's icon is the fill style.
func TestKitSidebarIconFillsSelected(t *testing.T) {
	ink := func(sel bool) int {
		tt := ui.NewTester(themed(func(c *ui.Context) {
			ui.Column(c).Width(240).Children(func() {
				item := SidebarItem(c, SidebarItemOpts{Selected: sel}).Label("row")
				item.Children(func() { SidebarIcon(c, item, "folder", sel) })
			})
		}), 300, 100)
		r, ok := tt.Find("row")
		if !ok {
			t.Fatal("no row")
		}
		n, img := 0, tt.Image()
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X)+40; x++ {
				if img.RGBAAt(x, y).R < 220 { // any ink, muted or not
					n++
				}
			}
		}
		return n
	}
	// The line icon inks 176 px on macOS and 200 on Windows (heavier antialiasing);
	// the fill icon 270 on both, a line icon in the selected colour about as few.
	if off, on := ink(false), ink(true); on < off*6/5 {
		t.Errorf("selected inks %d px, unselected %d: want the fill style", on, off)
	}
}

func TestKitFloatDarkInnerEdge(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Background(theme.Of(c).Window).Children(func() {
			Float(c, ui.Column(c).Width(200).Height(60).Radius(8).Background(theme.Of(c).Background)).Children(func() {})
		})
	}), 300, 200)
	tt.SetDark(true)
	tt.Frame()
	img := tt.Image()
	bg := theme.Dark().Background.SRGB()
	// the box spans x 20..220, y 20..80: the edge row is brighter than the
	// fill inside it, with no border painted.
	edge, mid, inner := img.RGBAAt(120, 20), img.RGBAAt(120, 50), img.RGBAAt(120, 24)
	if d := max(diff(mid.R, bg.R), diff(mid.G, bg.G), diff(mid.B, bg.B)); d > 2 {
		t.Errorf("fill %v, want %v", mid, bg)
	}
	if edge.R < mid.R+4 {
		t.Errorf("no inner edge: edge %v, fill %v", edge, mid)
	}
	if inner.R > mid.R+1 {
		t.Errorf("the edge is wider than a hairline: %v", inner)
	}
}

func TestEmptyLayout(t *testing.T) {
	for _, dashed := range []bool{false, true} {
		var tk theme.Tokens
		tt := ui.NewTester(themed(func(c *ui.Context) {
			tk = theme.Of(c)
			Empty(c, EmptyOpts{Icon: "inbox", Title: "No mail", Desc: "Nothing here yet", Dashed: dashed, Actions: func() {
				Button(c, "Compose", ButtonOpts{})
			}}).Label("empty").Width(400)
		}), 500, 400)
		for _, s := range []string{"No mail", "Nothing here yet", "Compose"} {
			if !tt.HasText(s) {
				t.Errorf("dashed=%v: missing %q in %q", dashed, s, tt.Texts())
			}
		}
		r, ok := tt.Find("empty")
		if !ok {
			t.Fatal("no empty")
		}
		img, border := tt.Image(), 0
		for x := int(r.X + r.W/4); x < int(r.X+3*r.W/4); x++ {
			p := img.RGBAAt(x, int(r.Y))
			if p.R == tk.Border.R && p.G == tk.Border.G && p.B == tk.Border.B {
				border++
			}
		}
		half := int(r.W / 2)
		if dashed && (border == 0 || border > half*3/4) {
			t.Errorf("dashed top edge has %d/%d border pixels, want a broken line", border, half)
		}
		if !dashed && border != 0 {
			t.Errorf("undashed Empty draws a border (%d pixels)", border)
		}
		if dashed {
			_ = tt.Click("Compose")
		}
	}
}

func TestButtonGroupTooltips(t *testing.T) {
	var got []string
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ButtonGroup(c,
			GroupItem{Icon: "archive", Tip: "Archive", OnClick: func() { got = append(got, "a") }},
			GroupItem{Icon: "trash-2", Tip: "Delete", OnClick: func() { got = append(got, "d") }, Disabled: true},
		)
	}), 300, 100)
	r, ok := tt.Find("Archive")
	if !ok {
		t.Fatal("no Archive item")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(900 * time.Millisecond)
	n := 0
	for _, x := range tt.Texts() {
		if x == "Archive" {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("tip not shown after hover: %q", tt.Texts())
	}
	_ = tt.Click("Archive")
	_ = tt.Click("Delete")
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("actions ran %v, want only a", got)
	}
}

func TestTabsUnderline(t *testing.T) {
	sel := 0
	var tk theme.Tokens
	tt := ui.NewTester(themed(func(c *ui.Context) {
		tk = theme.Of(c)
		TabsUnderline(c, &sel, "One", "Two")
	}), 300, 100)
	if err := tt.Click("Two"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(2 * theme.MotionFast)
	if sel != 1 {
		t.Fatalf("sel = %d", sel)
	}
	r, _ := tt.Find("Two")
	one, _ := tt.Find("One")
	if one.H != r.H {
		t.Errorf("tab heights differ: %v vs %v", one.H, r.H)
	}
	// the 32-high tab's bottom 2 px are Primary under the selected tab: look
	// down from its label, whose box the platform's font metrics place
	img, found := tt.Image(), false
	for y := int(r.Y + r.H); y <= int(r.Y+r.H/2)+17; y++ {
		if p := img.RGBAAt(int(r.X+r.W/2), y); p.R == tk.Primary.R && p.G == tk.Primary.G {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no primary %v underline under the selected tab", tk.Primary)
	}
}

func TestToastCap(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		for ; n < 4; n++ {
			c.AddToast(ui.Toast{ID: string(rune('a' + n)), Title: []string{"first", "second", "third", "fourth"}[n], Timeout: -1})
		}
		ToastHost(c, "Close")
	}), 800, 600)
	tt.Frame()
	tt.Frame()
	if tt.HasText("first") || !tt.HasText("second") || !tt.HasText("fourth") {
		t.Errorf("toasts = %q, want the oldest dropped", tt.Texts())
	}
}

func TestDestructiveOutline(t *testing.T) {
	for _, dark := range []bool{false, true} {
		var tk theme.Tokens
		tt := ui.NewTester(themed(func(c *ui.Context) {
			tk = theme.Of(c)
			Button(c, "Delete", ButtonOpts{Variant: DestructiveOutline})
		}), 200, 100)
		tt.SetDark(dark)
		tt.Frame()
		tt.Advance(2 * theme.MotionFast)
		r, _ := tt.Find("Delete")
		same := func(p color.RGBA, c ui.Color) bool { return p.R == c.R && p.G == c.G && p.B == c.B }
		if p := tt.Image().RGBAAt(int(r.X)+3, int(r.Y+r.H/2)); !same(p, tk.Background) {
			t.Errorf("dark=%v rest bg %v, want %v", dark, p, tk.Background)
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
		tt.Advance(2 * theme.MotionFast)
		if p := tt.Image().RGBAAt(int(r.X)+3, int(r.Y+r.H/2)); !same(p, tk.DangerBg) {
			t.Errorf("dark=%v hover bg %v, want %v", dark, p, tk.DangerBg)
		}
	}
}

func TestAlertAction(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, kind := range []AlertKind{Warning, Info} {
			var tk theme.Tokens
			n := 0
			tt := ui.NewTester(themed(func(c *ui.Context) {
				tk = theme.Of(c)
				AlertAction(c, kind, "Fix", func() { n++ })
			}), 200, 100)
			tt.SetDark(dark)
			tt.Frame()
			tt.Advance(2 * theme.MotionFast)
			r, ok := tt.Find("Fix")
			if !ok {
				t.Fatal("no button")
			}
			want := tk.WarningText
			if kind == Info {
				want = tk.Primary
			}
			p := tt.Image().RGBAAt(int(r.X)+3, int(r.Y+r.H/2))
			if p.R != want.R || p.G != want.G || p.B != want.B {
				t.Errorf("dark=%v kind=%v fill %v, want %v", dark, kind, p, want)
			}
			if err := tt.Click("Fix"); err != nil || n != 1 {
				t.Errorf("click err=%v n=%d", err, n)
			}
		}
	}
}

func TestSelectWith(t *testing.T) {
	width := func(o SelectOpts, label string) float32 {
		v := 1
		tt := ui.NewTester(themed(func(c *ui.Context) {
			SelectWith(c, o, &v, []Option[int]{{1, label}}).Label("sel")
		}), 600, 100)
		r, ok := tt.Find("sel")
		if !ok {
			t.Fatal("no select")
		}
		return r.W
	}
	if w := width(SelectOpts{MinWidth: 300}, "a"); w < 300 {
		t.Errorf("min width: %v", w)
	}
	if w := width(SelectOpts{MaxWidth: 150}, "a very long option label that would be wider than the cap"); w > 150 {
		t.Errorf("max width: %v", w)
	}
	// Flat drops the shadow: the pixel under the trigger is the page's
	below := func(flat bool) uint8 {
		v := 1
		tt := ui.NewTester(themed(func(c *ui.Context) {
			ui.Column(c).Padding(20).Children(func() {
				SelectWith(c, SelectOpts{Flat: flat}, &v, []Option[int]{{1, "a"}}).Label("sel")
			})
		}), 300, 100)
		r, _ := tt.Find("sel")
		return tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H)+1).R
	}
	if f, s := below(true), below(false); f <= s {
		t.Errorf("flat pixel %d not lighter than shadowed %d", f, s)
	}
}

// TestDialogWidths: each size is its width, hanging 64 from the top.
func TestDialogWidths(t *testing.T) {
	for size, want := range map[DialogSize]int{DialogSm: theme.DialogSmWidth, DialogMd: theme.SheetWidth, DialogLg: theme.DialogWideWidth} {
		open := true
		tt := ui.NewTester(themed(func(c *ui.Context) {
			Dialog(c, &open, "T", DialogOpts{Size: size, Close: "Close"}, func() { ui.Text(c, "body") }, func() { ui.Text(c, "foot") })
		}), 900, 600)
		tt.Advance(2 * theme.MotionBase)
		title, _ := tt.Find("T")
		img, first, last := tt.Image(), -1, -1
		y := int(title.Y) - 4 // in the header's padding, above the title
		for x := 0; x < 900; x++ {
			if p := img.RGBAAt(x, y); p.R == 255 && p.G == 255 && p.B == 255 {
				if first < 0 {
					first = x
				}
				last = x
			}
		}
		if w := last - first + 1; w < want-4 || w > want {
			t.Errorf("size %d is %d wide, want %d", size, w, want)
		}
	}
}

// A disabled button paints the design's disabled colours, not mygo's
// dimming of all it covers: a Default one its primary at half opacity, so it
// stays a button on any surface (white, frosted, dark), an Outline one's
// text Idle.
func TestKitButtonDisabledColors(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Gap(8).Padding(8).Children(func() {
			Button(c, "Add", ButtonOpts{Disabled: true})
			Button(c, "Test", ButtonOpts{Variant: Outline, Disabled: true})
		})
	}), 300, 200)
	tt.Frame()
	tok := theme.Light()
	img := tt.Image()
	at := func(label string, f func(r ui.Rect) (int, int)) color.RGBA {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("%s not found", label)
		}
		x, y := f(r)
		return img.RGBAAt(x, y)
	}
	same := func(name string, got color.RGBA, want ui.Color) {
		if got.R != want.R || got.G != want.G || got.B != want.B {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if got, want := at("Add", func(r ui.Rect) (int, int) { return int(r.X) + 3, int(r.Y) + 3 }), tok.Primary.Mix(tok.Background, 0.5); diff(got.R, want.R) > 2 {
		t.Errorf("disabled Default background = %v, want primary at half over white %v", got, want)
	}
	// The darkest pixel of the Outline label is its text colour.
	r, _ := tt.Find("Test")
	dark := color.RGBA{255, 255, 255, 255}
	for x := int(r.X); x < int(r.X+r.W); x++ {
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			if c := img.RGBAAt(x, y); int(c.R)+int(c.G)+int(c.B) < int(dark.R)+int(dark.G)+int(dark.B) {
				dark = c
			}
		}
	}
	same("disabled Outline text", dark, tok.Idle)
}

func TestMain(m *testing.M) {
	BackdropBlur = false // the CPU renderer's blur is slow; TestDialogBackdropBlurs turns it on
	os.Exit(m.Run())
}

// TestDialogGlass: a dialog's backdrop only tints the window, a hard edge
// staying sharp; with Glass its panel frosts what is behind it, without it
// the panel is opaque.
func TestDialogGlass(t *testing.T) {
	BackdropBlur = true
	t.Cleanup(func() { BackdropBlur, Glass = false, false })
	for _, glass := range []bool{true, false} {
		Glass = glass
		open := true
		tt := ui.NewTester(themed(func(c *ui.Context) {
			t := theme.Of(c)
			ui.Row(c).Fill().Children(func() {
				ui.Box(c).Width(200).FillHeight().Background(t.Foreground)
				ui.Box(c).Grow(1).FillHeight().Background(t.Background)
			})
			Dialog(c, &open, "T", DialogOpts{Size: DialogSm, Close: "Close"}, func() { ui.Box(c).Height(300) }, nil)
		}), 800, 600)
		tt.Advance(2 * theme.MotionBase)
		img := tt.Image()
		// Just right of the black column, low in the window, well below the panel: tinted, not blurred.
		if p := img.RGBAAt(201, 560); p.R < 200 {
			t.Errorf("glass %v: backdrop edge pixel %v is blurred", glass, p)
		}
		// The same edge inside the panel's empty body.
		p := img.RGBAAt(201, 300)
		if glass && p.R > 240 {
			t.Errorf("glass: panel pixel %v does not frost the edge behind it", p)
		}
		if !glass && p.R < 250 {
			t.Errorf("opaque: panel pixel %v shows what is behind it", p)
		}
	}
}

func TestAvatarColorKeptPerNameNeverGray(t *testing.T) {
	tk := theme.Light()
	seen := map[theme.LabelColor]bool{}
	for _, name := range []string{"Alice", "Bob", "Carol", "TikTok", "网易邮箱账号安全", "GitHub", "Dave", "Eve"} {
		col := avatarColor(tk, name)
		if col != avatarColor(tk, name) || col == tk.Labels[0] {
			t.Fatalf("%s: %v", name, col)
		}
		seen[col] = true
	}
	if len(seen) < 3 {
		t.Errorf("%d colors for 8 names", len(seen))
	}
}

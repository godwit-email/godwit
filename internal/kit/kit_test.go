package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

// themed runs body under the tokens the settings give, as the app does.
func themed(body func(c *ui.Context)) func(c *ui.Context) {
	return func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(core.Settings{Theme: "system", Density: "compact", FontSize: 13, BodyFontSize: 14}, c.Theme().Dark))
		body(c)
	}
}

func TestKitButtonClick(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if Button(c, "Save", ButtonOpts{Variant: Primary, Icon: "check"}).Clicked() {
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

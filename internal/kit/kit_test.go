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

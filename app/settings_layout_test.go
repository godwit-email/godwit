package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

func settingRowBoxes(t *testing.T, font int, label, desc string, w int) (row, lab, ctl ui.Rect) {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(core.Settings{Theme: "light", Density: "compact", FontSize: font, BodyFontSize: font}, false))
		ui.Column(c).FillWidth().Children(func() {
			settingRow(c, label, desc, func() { kit.Button(c, "Control", kit.ButtonOpts{Variant: kit.Outline}) }).Label("the row")
		})
	}, w, 300)
	var ok [3]bool
	row, ok[0] = tt.Find("the row")
	lab, ok[1] = tt.Find(label)
	ctl, ok[2] = tt.Find("Control")
	if ok != [3]bool{true, true, true} {
		t.Fatalf("boxes found %v", ok)
	}
	return
}

// TestSettingsRowShape: a row is 48 high at font 14 and its control sits at the right edge, inside the 16 padding.
func TestSettingsRowShape(t *testing.T) {
	row, _, ctl := settingRowBoxes(t, 14, "Density", "", 600)
	if row.H != 48 {
		t.Errorf("row height %v, want 48", row.H)
	}
	if got := row.X + row.W - (ctl.X + ctl.W); got != 16 {
		t.Errorf("control %v from the right edge, want 16", got)
	}
}

// TestSettingsNoClip: a long label and description wrap inside the row and stay left of the control.
func TestSettingsNoClip(t *testing.T) {
	for _, font := range []int{13, 16} {
		label := "A rather long setting label that has to wrap in a narrow row"
		desc := "A longer description that explains the setting in more words than fit on one line."
		row, lab, ctl := settingRowBoxes(t, font, label, desc, 360)
		if lab.Y+lab.H > row.Y+row.H {
			t.Errorf("font %d: label bottom %v below row bottom %v", font, lab.Y+lab.H, row.Y+row.H)
		}
		if lab.X+lab.W > ctl.X {
			t.Errorf("font %d: label right %v past control left %v", font, lab.X+lab.W, ctl.X)
		}
		if row.H <= 48 {
			t.Errorf("font %d: row did not grow for wrapped text: %v", font, row.H)
		}
	}
}

// TestFixedHeightTextFits: text in a box of fixed height (a capability
// chip, a key cap) fits it at font 16 under the app's root line height.
func TestFixedHeightTextFits(t *testing.T) {
	var box float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(core.Settings{Theme: "light", Density: "compact", FontSize: 16, BodyFontSize: 16}, false))
		box = c.Theme().Space(5)
		ui.Column(c).Children(func() {
			capChip(c, "chip", true)
			kit.Kbd(c, "Tab")
		})
		c.Root().LineHeight(theme.Of(c).Text.Line(theme.Of(c).Text.Body))
	}, 400, 200)
	for _, s := range []string{"chip", "Tab"} {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q", s)
		}
		if r.H > box {
			t.Errorf("%q is %v high in a box of %v", s, r.H, box)
		}
	}
}

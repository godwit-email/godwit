package kit

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

func fieldRow(c *ui.Context, on *bool, disabled bool) {
	ui.Column(c).FillWidth().Padding(10).Children(func() {
		FieldWith(c, FieldOpts{Horizontal: true, Switch: on, Disabled: disabled}, "Notify", "Tell me about new mail", "", nil)
	})
}

// A horizontal Field with a switch toggles from anywhere on the row, help text included.
func TestFieldHorizontalSwitchToggles(t *testing.T) {
	on := false
	tt := ui.NewTester(themed(func(c *ui.Context) { fieldRow(c, &on, false) }), 480, 120)
	for i, name := range []string{"Notify", "Tell me about new mail"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		if want := i == 0; on != want {
			t.Errorf("after clicking %q: on = %v, want %v", name, on, want)
		}
	}
}

// The row is at least 48 high, padded 16 at the sides, with the switch at the right edge and the text left of it.
func TestFieldHorizontalShape(t *testing.T) {
	on := true
	tt := ui.NewTester(themed(func(c *ui.Context) { fieldRow(c, &on, false) }), 480, 120)
	row, ok := tt.Find("Notify")
	if !ok {
		t.Fatal("no row")
	}
	img := tt.Image()
	if row.H < 48 {
		t.Errorf("row height %v, want at least 48", row.H)
	}
	right := -1
	for x := img.Bounds().Dx() - 1; x >= 0; x-- {
		if p := img.RGBAAt(x, int(row.Y+row.H/2)); p.A > 0 && p.R < 0x80 {
			right = x
			break
		}
	}
	if want := int(row.X + row.W - 16); right < want-1 || right > want {
		t.Errorf("switch ends at %d, want %d (16 from the row's right edge)", right, want)
	}
}

// The row is the one Tab stop and its focus ring draws round it.
func TestFieldHorizontalFocusRing(t *testing.T) {
	on := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		c.DisableAnimations()
		fieldRow(c, &on, false)
	}), 480, 120)
	row, _ := tt.Find("Notify")
	before := image.NewRGBA(tt.Image().Bounds())
	copy(before.Pix, tt.Image().Pix)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	changed := 0
	for y := int(row.Y) - 4; y < int(row.Y+row.H)+4; y++ {
		for _, x := range []int{int(row.X) + 40, int(row.X + row.W/2)} {
			if before.RGBAAt(x, y) != tt.Image().RGBAAt(x, y) {
				changed++
			}
		}
	}
	if changed == 0 {
		t.Error("keyboard focus left the row unchanged")
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if on {
		t.Error("a second Tab left the row: it has more than one stop")
	}
}

// A disabled row does not toggle, and its switch is dimmed.
func TestFieldHorizontalDisabled(t *testing.T) {
	on := false
	enabled := ui.NewTester(themed(func(c *ui.Context) { fieldRow(c, new(bool), false) }), 480, 120)
	tt := ui.NewTester(themed(func(c *ui.Context) { fieldRow(c, &on, true) }), 480, 120)
	_ = tt.Click("Notify")
	if on {
		t.Error("a disabled row toggled")
	}
	row, _ := tt.Find("Notify")
	dimmed := 0
	for y := int(row.Y); y < int(row.Y+row.H); y++ {
		for x := int(row.X); x < int(row.X+row.W); x++ {
			if tt.Image().RGBAAt(x, y) != enabled.Image().RGBAAt(x, y) {
				dimmed++
			}
		}
	}
	if dimmed == 0 {
		t.Error("the disabled row looks as an enabled one")
	}
}

// An error takes the place of the help in a Field, a switch row and a LabelledRow.
func TestFieldErrorReplacesHelp(t *testing.T) {
	on := false
	rows := map[string]func(c *ui.Context){
		"field": func(c *ui.Context) { Field(c, "Name", "Help text", "Taken already", func() { ui.Text(c, "Control") }) },
		"switch": func(c *ui.Context) {
			FieldWith(c, FieldOpts{Horizontal: true, Switch: &on}, "Name", "Help text", "Taken already", nil)
		},
		"row": func(c *ui.Context) {
			LabelledRow(c, "Name", "Help text", "Taken already", func() ui.Element { return ui.Text(c, "Control") })
		},
	}
	for name, build := range rows {
		tt := ui.NewTester(themed(build), 480, 160)
		if tt.HasText("Help text") || !tt.HasText("Taken already") {
			t.Errorf("%s: texts %q", name, tt.Texts())
		}
	}
}

// The vertical Field stays a column: label, control, help.
func TestFieldVerticalOrder(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Field(c, "Name", "Help text", "", func() { ui.Text(c, "Control") })
	}), 480, 160)
	label, _ := tt.Find("Name")
	control, _ := tt.Find("Control")
	help, _ := tt.Find("Help text")
	if !(label.Y < control.Y && control.Y < help.Y) {
		t.Errorf("label %v, control %v, help %v: not in order", label.Y, control.Y, help.Y)
	}
}

// A click on a LabelledRow's label focuses its control, as a Field's label does.
func TestLabelledRowLabelFocuses(t *testing.T) {
	var s string
	tt := ui.NewTester(themed(func(c *ui.Context) {
		LabelledRow(c, "API Key", "", "", func() ui.Element { return Input(c, &s, InputOpts{}).Label("key") })
	}), 480, 120)
	if err := tt.Click("API Key"); err != nil {
		t.Fatal(err)
	}
	tt.Type("abc")
	if s != "abc" {
		t.Errorf("typed into %q, want abc", s)
	}
}

// A LabelledRow with a help text shows it under the label, and a click on the label still focuses the control.
func TestLabelledRowHelpAndFocus(t *testing.T) {
	var s string
	tt := ui.NewTester(themed(func(c *ui.Context) {
		LabelledRow(c, "API Key", "Paste the key here", "", func() ui.Element { return Input(c, &s, InputOpts{}).Label("key") })
	}), 480, 120)
	if !tt.HasText("Paste the key here") {
		t.Fatalf("help not shown: %q", tt.Texts())
	}
	if err := tt.Click("API Key"); err != nil {
		t.Fatal(err)
	}
	tt.Type("abc")
	if s != "abc" {
		t.Errorf("typed into %q, want abc", s)
	}
}

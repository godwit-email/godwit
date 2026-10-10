package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	p "github.com/godwit-email/godwit/internal/palette"
)

func TestThemeTokens(t *testing.T) {
	l := Light()
	for name, c := range map[string]struct {
		got  ui.Color
		want p.OKLCH
	}{
		"Window": {l.Window, p.Mid(p.Neutral100, p.Neutral200)}, "Sidebar": {l.Sidebar, p.Neutral50},
		"Background": {l.Background, p.White}, "Foreground": {l.Foreground, p.Neutral950},
		"Muted": {l.Muted, p.Neutral500}, "Border": {l.Border, p.Neutral200},
		"Primary": {l.Primary, p.Neutral900}, "PrimaryText": {l.PrimaryText, p.White},
		"SidebarSelected": {l.SidebarSelected, p.Mid(p.Neutral100, p.Neutral200)}, "RailSelected": {l.RailSelected, p.Neutral200},
		"Hover": {l.Hover, p.Neutral100}, "Success": {l.Success, p.Green600},
		"WarningBg": {l.WarningBg, p.Amber50}, "WarningBorder": {l.WarningBorder, p.Amber300},
		"WarningText": {l.WarningText, p.Amber800}, "Danger": {l.Danger, p.Red600},
		"DangerBg": {l.DangerBg, p.Red50}, "InsBg": {l.InsBg, p.Green100}, "InsText": {l.InsText, p.Green800},
		"DelBg": {l.DelBg, p.Red50}, "DelText": {l.DelText, p.Red700},
	} {
		if c.got != c.want.Color() {
			t.Errorf("Light().%s = %v, want %v", name, c.got, c.want.Color())
		}
	}
	d := Dark()
	if !d.Dark || d.Background != p.Neutral950.Color() || d.Window != p.Mid(p.Neutral900, p.Neutral800).Color() ||
		d.Primary != p.Neutral200.Color() || d.PrimaryText != p.Neutral900.Color() || d.RailSelected != p.Neutral700.Color() {
		t.Errorf("Dark() = %+v", d)
	}

	st := core.Settings{Theme: "system", Density: "compact", FontSize: 13, BodyFontSize: 15}
	if FromSettings(st, false).Dark || !FromSettings(st, true).Dark {
		t.Error("system theme does not follow the system")
	}
	st.Theme = "dark"
	if !FromSettings(st, false).Dark {
		t.Error("dark theme is not dark")
	}
	st.Theme = "light"
	got := FromSettings(st, true)
	if got.Dark || got.FontSize != 13 || got.BodyFontSize != 15 || got.Scale != 1 {
		t.Errorf("light compact = dark %v, sizes %v/%v, scale %v", got.Dark, got.FontSize, got.BodyFontSize, got.Scale)
	}
	st.Density = "comfortable"
	if s := FromSettings(st, true).Scale; s != 1.25 {
		t.Errorf("comfortable scale = %v, want 1.25", s)
	}
}

func TestThemeApplyOf(t *testing.T) {
	want := FromSettings(core.Settings{Theme: "dark", Density: "comfortable", FontSize: 15, BodyFontSize: 16}, false)
	var got Tokens
	var th *ui.Theme
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c, want)
		got, th = Of(c), c.Theme()
	}, 200, 100)
	tt.Frame()
	if got != want {
		t.Errorf("Of = %+v, want %+v", got, want)
	}
	if th.Font != "Geist" || th.FontSize != 15 || th.Background != want.Background || !th.Dark {
		t.Errorf("theme = font %q size %v dark %v", th.Font, th.FontSize, th.Dark)
	}
	// Without Apply, Of follows the window's appearance.
	var plain Tokens
	tt = ui.NewTester(func(c *ui.Context) { plain = Of(c) }, 200, 100)
	tt.SetDark(true)
	tt.Frame()
	if !plain.Dark {
		t.Error("Of without Apply ignores the dark window")
	}
}

func TestFontsRegister(t *testing.T) {
	if err := RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	if err := RegisterFonts(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c, Light())
		ui.Text(c, "Geist 收件箱")
		ui.Text(c, "mono").Font("Geist Mono")
	}, 200, 100)
	if !tt.HasText("Geist 收件箱") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestSpaceFollowsDensity(t *testing.T) {
	for density, want := range map[string]float32{"compact": 32, "comfortable": 40} {
		var got float32
		tok := FromSettings(core.Settings{Theme: "light", Density: density, FontSize: 13, BodyFontSize: 14}, false)
		ui.NewTester(func(c *ui.Context) {
			Apply(c, tok)
			got = c.Theme().Space(8)
		}, 200, 100).Frame()
		if got != want {
			t.Errorf("%s: Space(8) = %v, want %v", density, got, want)
		}
	}
}

func TestRadiusAndTextScale(t *testing.T) {
	tok := FromSettings(core.Settings{Theme: "light", Density: "comfortable", FontSize: 15, BodyFontSize: 16}, false)
	if r := tok.Radius; [4]float32{r.Sm, r.Md, r.Lg, r.Xl} != [4]float32{6, 8, 10, 14} {
		t.Errorf("Radius = %+v", r)
	}
	if x := tok.Text; [6]float32{x.Xs, x.Sm, x.Base, x.Lg, x.Xl, x.X2l} != [6]float32{13, 14, 15, 17, 20, 22} {
		t.Errorf("Text = %+v", x)
	}
	if Light().Text.Base != 13 {
		t.Errorf("Light().Text.Base = %v, want 13", Light().Text.Base)
	}
	var th *ui.Theme
	ui.NewTester(func(c *ui.Context) { Apply(c, tok); th = c.Theme() }, 200, 100).Frame()
	if th.Radius != 8 || th.FontSize != 15 {
		t.Errorf("theme radius %v, font size %v; want 8, 15", th.Radius, th.FontSize)
	}
}

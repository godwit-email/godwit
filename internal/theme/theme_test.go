package theme

import (
	"slices"
	"testing"
	"time"

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
		"SidebarSelected": {l.SidebarSelected, p.Neutral100},
		"Hover":           {l.Hover, p.Neutral100}, "Success": {l.Success, p.Green600},
		"SuccessBg": {l.SuccessBg, p.Green50}, "SuccessBorder": {l.SuccessBorder, p.Green200}, "SuccessText": {l.SuccessText, p.Green700},
		"DangerBorder": {l.DangerBorder, p.Red200}, "DangerText": {l.DangerText, p.Red700},
		"WarningBg": {l.WarningBg, p.Amber50}, "WarningBorder": {l.WarningBorder, p.Amber300},
		"WarningText": {l.WarningText, p.Amber800}, "Danger": {l.Danger, p.Red600},
		"DangerBg": {l.DangerBg, p.Red50}, "InsBg": {l.InsBg, p.Green100}, "InsText": {l.InsText, p.Green800},
		"DelBg": {l.DelBg, p.Red50}, "DelText": {l.DelText, p.Red700}, "Unread": {l.Unread, p.Blue500},
		"Input": {l.Input, p.Neutral200}, "Ring": {l.Ring, p.Neutral400},
	} {
		if c.got != c.want.Color() {
			t.Errorf("Light().%s = %v, want %v", name, c.got, c.want.Color())
		}
	}
	if l.Outline != p.Neutral950.Color().Alpha(0.05) || Dark().Outline != p.White.Color().Alpha(0.05) {
		t.Errorf("Outline %v / %v", l.Outline, Dark().Outline)
	}
	if l.DividerHover != p.Neutral300.Color() || Dark().DividerHover != p.Neutral700.Color() {
		t.Errorf("DividerHover %v / %v", l.DividerHover, Dark().DividerHover)
	}
	if l.HoverTint != p.Neutral950.Color().Alpha(0.04) {
		t.Errorf("Light() HoverTint %v", l.HoverTint)
	}
	d := Dark()
	if !d.Dark || d.Background != p.Neutral950.Color() || d.Window != p.Mid(p.Neutral900, p.Neutral800).Color() ||
		d.Primary != p.Neutral200.Color() || d.PrimaryText != p.Neutral900.Color() || d.HoverTint != p.White.Color().Alpha(0.08) ||
		d.Border != p.White.Color().Alpha(0.1) || d.Input != p.White.Color().Alpha(0.15) || d.Ring != p.Neutral500.Color() {
		t.Errorf("Dark() = %+v", d)
	}

	st := core.Settings{Theme: "system", Density: "compact", FontSize: 14, BodyFontSize: 15}
	if FromSettings(st, false).Dark || !FromSettings(st, true).Dark {
		t.Error("system theme does not follow the system")
	}
	st.Theme = "dark"
	if !FromSettings(st, false).Dark {
		t.Error("dark theme is not dark")
	}
	st.Theme = "light"
	got := FromSettings(st, true)
	if got.Dark || got.FontSize != 14 || got.BodyFontSize != 15 || got.Scale != 1 {
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
	if th.Font != "" || th.FontSize != 15 || th.Background != want.Background || !th.Dark {
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

// TestSystemFonts: the native UI uses the system's fonts, as the web pages do.
func TestSystemFonts(t *testing.T) {
	var font string
	ui.NewTester(func(c *ui.Context) { Apply(c, Light()); font = c.Theme().Font }, 10, 10)
	if font != "" {
		t.Errorf("Theme.Font = %q, want the system's (\"\")", font)
	}
	if Mono != "monospace" {
		t.Errorf("Mono = %q", Mono)
	}
}

func TestSpaceFollowsDensity(t *testing.T) {
	for density, want := range map[string]float32{"compact": 32, "comfortable": 40} {
		var got float32
		tok := FromSettings(core.Settings{Theme: "light", Density: density, FontSize: 14, BodyFontSize: 14}, false)
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
	// Tailwind v4's rounded-xs … rounded-4xl, whatever the density
	if r := tok.Radius; [8]float32{r.Xs, r.Sm, r.Md, r.Lg, r.Xl, r.X2l, r.X3l, r.X4l} != [8]float32{2, 4, 6, 8, 12, 16, 24, 32} {
		t.Errorf("Radius = %+v", r)
	}
	if x := tok.Text; [7]float32{x.Xs, x.Sm, x.Base, x.Body, x.Lg, x.Xl, x.X2l} != [7]float32{12, 13, 14, 15, 16, 19, 21} {
		t.Errorf("Text = %+v", x)
	}
	if Light().Text.Body != 14 {
		t.Errorf("Light().Text.Body = %v, want 14", Light().Text.Body)
	}
	var th *ui.Theme
	ui.NewTester(func(c *ui.Context) { Apply(c, tok); th = c.Theme() }, 200, 100).Frame()
	if th.Radius != 8 || th.FontSize != 15 {
		t.Errorf("theme radius %v, font size %v; want 8, 15", th.Radius, th.FontSize)
	}
}

func TestMotionTokens(t *testing.T) {
	if MotionFast != 120*time.Millisecond || MotionBase != 200*time.Millisecond {
		t.Fatal("motion tokens")
	}
}

// TestShadowsAsTailwind: the shadows are Tailwind v4's, as shadcn uses them.
func TestShadowsAsTailwind(t *testing.T) {
	for name, tc := range map[string]struct{ got, want Elevation }{
		"xs": {ShadowXs, Elevation{{1, 2, 0, 0.05}}},
		"sm": {ShadowSm, Elevation{{1, 3, 0, 0.1}, {1, 2, -1, 0.1}}},
		"md": {ShadowMd, Elevation{{4, 6, -1, 0.1}, {2, 4, -2, 0.1}}},
		"lg": {ShadowLg, Elevation{{10, 15, -3, 0.1}, {4, 6, -4, 0.1}}},
		"xl": {ShadowXl, Elevation{{20, 25, -5, 0.1}, {8, 10, -6, 0.1}}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("shadow-%s = %v, want %v", name, tc.got, tc.want)
		}
	}
	// 0 0 0 1px #0000000a, 0 2px 8px 0 #0000000a, 0 4px 80px 8px #00000006
	if want := (Elevation{{0, 0, 1, 10.0 / 255}, {2, 8, 0, 10.0 / 255}, {4, 80, 8, 6.0 / 255}}); !slices.Equal(ShadowFloat, want) {
		t.Errorf("ShadowFloat = %v, want %v", ShadowFloat, want)
	}
	if d := Dark(); d.FloatInset != p.White.Color().Alpha(0.2) {
		t.Errorf("dark FloatInset %v", d.FloatInset)
	}
}

func TestTextScaleMatchesTokens(t *testing.T) {
	tok := Light()
	x := tok.Text
	if got := [9]float32{x.Xs, x.Sm, x.Base, x.Body, x.Lg, x.Title, x.Xl, x.X2l, x.Display}; got != [9]float32{11, 12, 13, 14, 15, 16, 18, 20, 29} {
		t.Errorf("Text at 14 = %+v", x)
	}
	if got := mygoTheme(tok).FontSize; got != 14 {
		t.Errorf("mygo FontSize = %v, want 14", got)
	}
}

func TestHighlightToken(t *testing.T) {
	l, d := Light(), Dark()
	if l.Highlight != p.Yellow200.Color() || l.HighlightText != p.Neutral950.Color() || l.Highlight != (ui.Color{R: 0xff, G: 0xf0, B: 0x85, A: 255}) {
		t.Errorf("light highlight = %v on %v", l.Highlight, l.HighlightText)
	}
	if d.Highlight != p.Yellow900.Color() || d.HighlightText != p.Yellow100.Color() {
		t.Errorf("dark highlight = %v on %v", d.Highlight, d.HighlightText)
	}
}

// TestDarkStatus: the dark status tints are Tailwind's -950 fill, -800 edge, -300 text.
func TestDarkStatus(t *testing.T) {
	d := Dark()
	for name, ok := range map[string]bool{
		"success": d.SuccessBg == p.Green950.Color() && d.SuccessBorder == p.Green800.Color() && d.SuccessText == p.Green300.Color(),
		"warning": d.WarningBg == p.Amber950.Color() && d.WarningBorder == p.Amber800.Color() && d.WarningText == p.Amber300.Color(),
		"danger":  d.DangerBg == p.Red950.Color() && d.DangerBorder == p.Red800.Color() && d.DangerText == p.Red300.Color(),
		"ins/del": d.InsBg == p.Green950.Color() && d.InsText == p.Green300.Color() && d.DelBg == p.Red950.Color() && d.DelText == p.Red300.Color(),
	} {
		if !ok {
			t.Errorf("dark %s tokens wrong", name)
		}
	}
}

// TestLine: each text step has its line height at any font size, and a
// size between steps has Body's.
func TestLine(t *testing.T) {
	for _, font := range []int{13, 14, 16} {
		s := FromSettings(core.Settings{FontSize: font}, false).Text
		for size, want := range map[float32]float32{s.Xs: LineXs, s.Sm: LineSm, s.Base: LineBase, s.Body: LineBody, s.Lg: LineLg,
			s.Title: LineTitle, s.Xl: LineXl, s.X2l: LineX2l, s.Display: LineDisplay, s.Body + 0.5: LineBody} {
			if got := s.Line(size); got != want {
				t.Errorf("font %d: Line(%v) = %v, want %v", font, size, got, want)
			}
		}
	}
}

// TestLabelColors: each label color is a Tailwind step set, -100/-200/-700 in
// the light and -950/-800/-300 in the dark; gray is neutral with Primary's dot.
func TestLabelColors(t *testing.T) {
	lc := func(dot ui.Color, fill, edge, text p.OKLCH) LabelColor {
		return LabelColor{Dot: dot, Fill: fill.Color(), Edge: edge.Color(), Text: text.Color()}
	}
	l, d := Light(), Dark()
	for i, n := range LabelNames {
		if l.Labels[i] == (LabelColor{}) || d.Labels[i] == (LabelColor{}) {
			t.Errorf("label color %s unset", n)
		}
	}
	for name, c := range map[string]struct{ got, want LabelColor }{
		"light red":  {l.Label("red"), lc(p.Red500.Color(), p.Red100, p.Red200, p.Red700)},
		"dark red":   {d.Label("red"), lc(p.Red500.Color(), p.Red950, p.Red800, p.Red300)},
		"light gray": {l.Label("gray"), lc(l.Primary, p.Neutral100, p.Neutral200, p.Neutral700)},
		"dark gray":  {d.Label("gray"), lc(d.Primary, p.Neutral800, p.Neutral700, p.Neutral300)},
		"light pink": {l.Label("pink"), lc(p.Pink500.Color(), p.Pink100, p.Pink200, p.Pink700)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %+v, want %+v", name, c.got, c.want)
		}
	}
}

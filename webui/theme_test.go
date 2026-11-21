package webui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestThemeVars(t *testing.T) {
	l := theme.Light()
	v := ThemeVars(l)
	ink := func(t theme.Tokens) string { // shadows are in the appearance's ShadowInk, as kit.Elevate's
		c := t.ShadowInk()
		return fmt.Sprintf("rgb(%d %d %d / ", c.R, c.G, c.B)
	}
	lk := ink(l)
	want := map[string]string{
		"--background": css(l.Background), "--foreground": css(l.Foreground),
		"--card": css(l.Background), "--card-foreground": css(l.Foreground),
		"--popover": css(l.MaterialPopover), "--popover-foreground": css(l.Foreground),
		"--primary": css(l.Primary), "--primary-foreground": css(l.PrimaryText),
		"--secondary": css(l.Hover), "--secondary-foreground": css(l.Foreground),
		"--muted": css(l.Hover), "--muted-foreground": css(l.Muted),
		"--accent": css(l.Hover), "--accent-foreground": css(l.Foreground),
		"--destructive": css(l.Danger), "--destructive-foreground": css(l.OnSolid),
		"--border": css(l.Border), "--input": css(l.Input), "--ring": css(l.Ring),
		"--radius": "8px",
		"--ins-bg": css(l.InsBg), "--ins-text": css(l.InsText),
		"--del-bg": css(l.DelBg), "--del-text": css(l.DelText),
		"--sidebar":    css(l.Sidebar),
		"--warning-bg": css(l.WarningBg), "--warning-border": css(l.WarningBorder), "--warning-text": css(l.WarningText),
		"--info-bg": css(l.InfoBg), "--info-border": css(l.InfoBorder), "--info-text": css(l.InfoText),
		"--success-bg": css(l.SuccessBg), "--success-border": css(l.SuccessBorder), "--success-text": css(l.SuccessText),
		"--danger-bg": css(l.DangerBg), "--danger-border": css(l.DangerBorder), "--danger-text": css(l.DangerText),
		"--hover-tint": css(l.HoverTint), "--idle": css(l.Idle),
		"--shadow-float": "0 0px 0px 1px " + lk + "0.039), 0 2px 8px 0px " + lk + "0.039), 0 4px 80px 8px " + lk + "0.024)",
		"--window":       css(l.Window), "--shadow-xs": "0 1px 2px 0px " + lk + "0.050)",
		"--window-material": css(l.Window.Alpha(theme.MaterialTint)),
		"--glass-bg":        css(l.GlassBg),
		"--glass-border":    css(l.GlassBorder),
		"--glass-hover":     css(l.GlassHover),
		"--raised":          css(l.Raised),
		"--scrim":           "rgb(0 0 0 / 0.200)", "--alert-scrim": css(l.AlertScrim),
		"--shadow-dialog":    "0 25px 50px -12px " + lk + "0.250)",
		"--material-popover": css(l.MaterialPopover), "--material-dialog": css(l.MaterialDialog), "--material-bar": css(l.MaterialBar),
		"--material-blur": "25px", "--toast-width": "360px",
		"--shadow-popover": "0 8px 40px 0px " + lk + "0.160), 0 1px 3px 0px " + lk + "0.080)",
		"--dialog-sm":      "440px", "--dialog-md": "560px", "--dialog-lg": "640px",
		"--dialog-centre": "40vh", "--dialog-min-top": "32px",
	}
	for i, name := range theme.LabelNames {
		c := l.Labels[i]
		want["--label-"+name+"-dot"], want["--label-"+name+"-fill"] = css(c.Dot), css(c.Fill)
		want["--label-"+name+"-edge"], want["--label-"+name+"-text"] = css(c.Edge), css(c.Text)
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if got, want := ThemeVars(theme.Dark())["--shadow-float"], "inset 0 0 1px "+css(theme.Dark().FloatInset); got != want {
		t.Errorf("dark --shadow-float = %q, want %q", got, want)
	}
	if got, want := ThemeVars(theme.Dark())["--shadow-xs"], "0 1px 2px 0px "+ink(theme.Dark())+"0.050)"; got != want {
		t.Errorf("dark --shadow-xs = %q, want %q", got, want)
	}
	// Text on the red fill stays white in the dark, where PrimaryText is near-black.
	if got := ThemeVars(theme.Dark())["--destructive-foreground"]; got != v["--destructive-foreground"] {
		t.Errorf("dark --destructive-foreground = %q, want the light's %q", got, v["--destructive-foreground"])
	}
	// ui.RGBA takes alpha in 0..1, so the byte alpha is set directly.
	if got := css(ui.Color{R: 10, G: 20, B: 30, A: 128}); got != "rgb(10 20 30 / 0.502)" {
		t.Errorf("css = %q", got)
	}
	if got := css(ui.RGB(1, 2, 3)); got != "rgb(1 2 3 / 1)" {
		t.Errorf("css opaque = %q", got)
	}
}

func TestThemeOf(t *testing.T) {
	st := core.Settings{}
	st.Theme, st.FontSize, st.BodyFontSize = "dark", 15, 17
	got := ThemeOf(st)
	if got.Mode != "dark" || got.FontSize != 15 || got.BodyFontSize != 17 {
		t.Errorf("mode/font/body = %q/%v/%v", got.Mode, got.FontSize, got.BodyFontSize)
	}
	if got.Light["--background"] == got.Dark["--background"] {
		t.Error("light and dark backgrounds are equal")
	}
}

package webui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestThemeVars(t *testing.T) {
	l := theme.Light()
	v := ThemeVars(l)
	want := map[string]string{
		"--background": css(l.Background), "--foreground": css(l.Foreground),
		"--card": css(l.Background), "--card-foreground": css(l.Foreground),
		"--popover": css(l.Background), "--popover-foreground": css(l.Foreground),
		"--primary": css(l.Primary), "--primary-foreground": css(l.PrimaryText),
		"--secondary": css(l.Hover), "--secondary-foreground": css(l.Foreground),
		"--muted": css(l.Hover), "--muted-foreground": css(l.Muted),
		"--accent": css(l.Hover), "--accent-foreground": css(l.Foreground),
		"--destructive": css(l.Danger), "--destructive-foreground": css(l.PrimaryText),
		"--border": css(l.Border), "--input": css(l.Input), "--ring": css(l.Ring),
		"--radius": "8px",
		"--ins-bg": css(l.InsBg), "--ins-text": css(l.InsText),
		"--del-bg": css(l.DelBg), "--del-text": css(l.DelText),
		"--sidebar":   css(l.Sidebar),
		"--highlight": css(l.Highlight), "--highlight-foreground": css(l.HighlightText),
		"--warning-bg": css(l.WarningBg), "--warning-border": css(l.WarningBorder), "--warning-text": css(l.WarningText),
		"--shadow-float": "0 0px 0px 1px rgb(0 0 0 / 0.039), 0 2px 8px 0px rgb(0 0 0 / 0.039), 0 4px 80px 8px rgb(0 0 0 / 0.024)",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if got, want := ThemeVars(theme.Dark())["--shadow-float"], "inset 0 0 1px "+css(theme.Dark().FloatInset); got != want {
		t.Errorf("dark --shadow-float = %q, want %q", got, want)
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

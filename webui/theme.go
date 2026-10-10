// Package webui owns the web UI's Go side: compose windows, their bound
// service and the theme variables.
package webui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

// Theme is what a web page needs to look like the native UI: the CSS
// variables of both appearances (the page picks by Mode, following the
// system's for "system") and the interface font size.
type Theme struct {
	Mode     string            `json:"mode"` // "system", "light" or "dark"
	Light    map[string]string `json:"light"`
	Dark     map[string]string `json:"dark"`
	FontSize float32           `json:"fontSize"`
}

// ThemeOf is the theme for the user's settings.
func ThemeOf(st core.Settings) Theme {
	lt, dk := st, st
	lt.Theme, dk.Theme = "light", "dark"
	l, d := theme.FromSettings(lt, false), theme.FromSettings(dk, true)
	return Theme{Mode: st.Theme, Light: ThemeVars(l), Dark: ThemeVars(d), FontSize: l.FontSize}
}

// ThemeVars maps tokens to shadcn's CSS variables. --radius is Radius.Lg, so
// shadcn's derived sm/md/lg/xl land on the native 4/6/8/12.
func ThemeVars(t theme.Tokens) map[string]string {
	return map[string]string{
		"--background": css(t.Background), "--foreground": css(t.Foreground),
		"--card": css(t.Background), "--card-foreground": css(t.Foreground),
		"--popover": css(t.Background), "--popover-foreground": css(t.Foreground),
		"--primary": css(t.Primary), "--primary-foreground": css(t.PrimaryText),
		"--secondary": css(t.Hover), "--secondary-foreground": css(t.Foreground),
		"--muted": css(t.Hover), "--muted-foreground": css(t.Muted),
		"--accent": css(t.Hover), "--accent-foreground": css(t.Foreground),
		"--destructive": css(t.Danger), "--destructive-foreground": css(t.PrimaryText),
		"--border": css(t.Border), "--input": css(t.Input), "--ring": css(t.Ring),
		"--radius": fmt.Sprintf("%gpx", t.Radius.Lg),
	}
}

// css writes c as CSS Color 4 `rgb(r g b / a)`.
func css(c ui.Color) string {
	a := "1"
	if c.A != 255 {
		a = fmt.Sprintf("%.3f", float32(c.A)/255)
	}
	return fmt.Sprintf("rgb(%d %d %d / %s)", c.R, c.G, c.B, a)
}

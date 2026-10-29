// Package webui owns the web UI's Go side: compose windows, their bound
// service and the theme variables.
package webui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

// Theme is what a web page needs to look like the native UI: the CSS
// variables of both appearances (the page picks by Mode, following the
// system's for "system"), the interface font size and the mail body's.
type Theme struct {
	// Mode is the user's choice: "system", "light" or "dark".
	Mode string `json:"mode"`
	// Light and Dark map CSS variable names to values for each appearance.
	Light map[string]string `json:"light"`
	Dark  map[string]string `json:"dark"`
	// FontSize is the interface font size in CSS pixels.
	FontSize float32 `json:"fontSize"`
	// BodyFontSize is the mail body font size in CSS pixels (the reader's).
	BodyFontSize float32 `json:"bodyFontSize"`
}

// ThemeOf is the theme for the user's settings.
func ThemeOf(st core.Settings) Theme {
	lt, dk := st, st
	lt.Theme, dk.Theme = "light", "dark"
	l, d := theme.FromSettings(lt, false), theme.FromSettings(dk, true)
	return Theme{Mode: st.Theme, Light: ThemeVars(l), Dark: ThemeVars(d), FontSize: l.FontSize, BodyFontSize: l.BodyFontSize}
}

// ThemeVars maps tokens to shadcn's CSS variables, plus Wit's review marks
// (--ins-* and --del-*), the warning colours (--warning-*) and the floating
// input's shadow (--shadow-float: ShadowFloat in the light appearance, the
// FloatInset inner edge in the dark), the window material's colour (--window)
// and a card's shadow (--shadow-xs: ShadowXs, black in both). --radius is Radius.Lg; styles.css
// derives sm…4xl from it by fixed offsets, landing on the native 4/6/8/12/16/24/32.
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
		// Wit's review marks: inserted and deleted words.
		"--ins-bg": css(t.InsBg), "--ins-text": css(t.InsText),
		"--del-bg": css(t.DelBg), "--del-text": css(t.DelText),
		"--sidebar":       css(t.Sidebar),
		"--settings-card": css(t.Card), // a settings section's fill, under a page shown in one
		"--warning-bg":    css(t.WarningBg), "--warning-border": css(t.WarningBorder), "--warning-text": css(t.WarningText),
		"--shadow-float": floatShadow(t),
		"--window":       css(t.Window), "--shadow-xs": shadow(theme.ShadowXs),
		"--window-material": windowMaterial(t),
		"--glass-bg":        css(t.GlassBg),
		"--glass-border":    css(t.GlassBorder),
		"--glass-hover":     css(t.GlassHover),
	}
}

// windowMaterial is a page's background over the window's material: the
// light Window tinting it, as the main window's root; clear in the dark.
func windowMaterial(t theme.Tokens) string {
	if t.Dark {
		return "transparent"
	}
	return css(t.Window.Alpha(theme.MaterialTint))
}

// floatShadow is a floating input's box-shadow: ShadowFloat's layers of
// black in the light appearance; FloatInset's inner edge in the dark.
func floatShadow(t theme.Tokens) string {
	if t.Dark {
		return fmt.Sprintf("inset 0 0 %dpx %s", theme.FloatInsetBlur, css(t.FloatInset))
	}
	return shadow(theme.ShadowFloat)
}

// shadow writes e as a CSS box-shadow of black layers.
func shadow(e theme.Elevation) string {
	ls := make([]string, len(e))
	for i, l := range e {
		ls[i] = fmt.Sprintf("0 %gpx %gpx %gpx rgb(0 0 0 / %.3f)", l.Y, l.Blur, l.Spread, l.Alpha)
	}
	return strings.Join(ls, ", ")
}

// css writes c as CSS Color 4 `rgb(r g b / a)`.
func css(c ui.Color) string {
	a := "1"
	if c.A != 255 {
		a = fmt.Sprintf("%.3f", float32(c.A)/255)
	}
	return fmt.Sprintf("rgb(%d %d %d / %s)", c.R, c.G, c.B, a)
}

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
// (--ins-* and --del-*), the notice colours (--warning-*, --info-*, --success-*,
// and --danger-bg, --danger-border and --danger-text), the button's greys (--hover-tint, the
// tint a ghost or secondary button takes on hover, and --idle, a disabled
// label) and the floating
// input's shadow (--shadow-float: ShadowFloat in the light appearance, the
// FloatInset inner edge in the dark), the window material's colour (--window)
// and a card's shadow (--shadow-xs: ShadowXs, black in both), and kit.Dialog's
// look for web/src/components/dialog.tsx: the panel (--raised), the scrims
// (--scrim, --alert-scrim), ShadowX2l (--shadow-dialog), the widths
// (--dialog-sm/md/lg) and the place (--dialog-centre: the panel's centre,
// --dialog-min-top: its least distance from the top), and the design's frosted
// materials: --popover and --material-popover (MaterialPopover: popovers, menus, toasts),
// --material-dialog, --material-bar, the blur behind them (--material-blur), their drop
// (--shadow-popover: ShadowPopover) and a toast's width (--toast-width). --radius is
// Radius.Lg; styles.css derives sm…4xl from it by fixed offsets, landing on the native
// 4/6/8/12/16/24/32 (2xl, a popover card's and a dialog's, is Radius.X2l), and each mail
// label colour of theme.LabelNames as --label-<name>-dot, -fill, -edge and -text (the
// picker's dot, the fill under an avatar or chip, its edge, and the text on it).
func ThemeVars(t theme.Tokens) map[string]string {
	vars := map[string]string{
		"--background": css(t.Background), "--foreground": css(t.Foreground),
		"--card": css(t.Background), "--card-foreground": css(t.Foreground),
		"--popover": css(t.MaterialPopover), "--popover-foreground": css(t.Foreground),
		"--primary": css(t.Primary), "--primary-foreground": css(t.PrimaryText),
		"--secondary": css(t.Hover), "--secondary-foreground": css(t.Foreground),
		"--muted": css(t.Hover), "--muted-foreground": css(t.Muted),
		"--accent": css(t.Hover), "--accent-foreground": css(t.Foreground),
		"--destructive": css(t.Danger), "--destructive-foreground": css(t.OnSolid),
		"--border": css(t.Border), "--input": css(t.Input), "--ring": css(t.Ring),
		"--radius": fmt.Sprintf("%gpx", t.Radius.Lg),
		// Wit's review marks: inserted and deleted words.
		"--ins-bg": css(t.InsBg), "--ins-text": css(t.InsText),
		"--del-bg": css(t.DelBg), "--del-text": css(t.DelText),
		"--sidebar":       css(t.Sidebar),
		"--settings-card": css(t.Card), // a settings section's fill, under a page shown in one
		"--warning-bg":    css(t.WarningBg), "--warning-border": css(t.WarningBorder), "--warning-text": css(t.WarningText),
		"--info-bg": css(t.InfoBg), "--info-border": css(t.InfoBorder), "--info-text": css(t.InfoText),
		"--success-bg": css(t.SuccessBg), "--success-border": css(t.SuccessBorder), "--success-text": css(t.SuccessText),
		"--danger-bg": css(t.DangerBg), "--danger-border": css(t.DangerBorder), "--danger-text": css(t.DangerText),
		"--hover-tint": css(t.HoverTint), "--idle": css(t.Idle),
		"--shadow-float": floatShadow(t),
		"--window":       css(t.Window), "--shadow-xs": shadow(theme.ShadowXs, t),
		"--window-material": windowMaterial(t),
		"--glass-bg":        css(t.GlassBg),
		"--glass-border":    css(t.GlassBorder),
		"--glass-hover":     css(t.GlassHover),
		"--raised":          css(t.Raised),
		"--scrim":           css(t.Scrim), "--alert-scrim": css(t.AlertScrim),
		"--shadow-dialog":    shadow(theme.ShadowX2l, t),
		"--material-popover": css(t.MaterialPopover), "--material-dialog": css(t.MaterialDialog), "--material-bar": css(t.MaterialBar),
		"--material-blur":  px(theme.MaterialBlur),
		"--shadow-popover": shadow(theme.ShadowPopover, t),
		"--toast-width":    px(theme.ToastWidth),
		"--dialog-sm":      px(theme.DialogSmWidth), "--dialog-md": px(theme.SheetWidth), "--dialog-lg": px(theme.DialogWideWidth),
		"--dialog-centre":  fmt.Sprintf("%gvh", 100*(0.5-theme.DialogLift/2)),
		"--dialog-min-top": px(theme.DialogMinTop),
	}
	for i, name := range theme.LabelNames {
		c := t.Labels[i]
		vars["--label-"+name+"-dot"], vars["--label-"+name+"-fill"] = css(c.Dot), css(c.Fill)
		vars["--label-"+name+"-edge"], vars["--label-"+name+"-text"] = css(c.Edge), css(c.Text)
	}
	return vars
}

// windowMaterial is a page's background over the window's material: the
// light Window tinting it, as the main window's root; clear in the dark.
func windowMaterial(t theme.Tokens) string {
	if t.Dark {
		return "transparent"
	}
	return css(t.Window.Alpha(theme.MaterialTint))
}

// floatShadow is a floating input's box-shadow: ShadowFloat's layers in the
// light appearance; FloatInset's inner edge in the dark.
func floatShadow(t theme.Tokens) string {
	if t.Dark {
		return fmt.Sprintf("inset 0 0 %dpx %s", theme.FloatInsetBlur, css(t.FloatInset))
	}
	return shadow(theme.ShadowFloat, t)
}

// shadow writes e as a CSS box-shadow of layers in t's ShadowInk, as
// kit.Elevate draws it.
func shadow(e theme.Elevation, t theme.Tokens) string {
	ink := t.ShadowInk()
	ls := make([]string, len(e))
	for i, l := range e {
		ls[i] = fmt.Sprintf("0 %gpx %gpx %gpx rgb(%d %d %d / %.3f)", l.Y, l.Blur, l.Spread, ink.R, ink.G, ink.B, l.Alpha)
	}
	return strings.Join(ls, ", ")
}

// px writes n as CSS pixels.
func px(n float32) string { return fmt.Sprintf("%gpx", n) }

// css writes c as CSS Color 4 `rgb(r g b / a)`.
func css(c ui.Color) string {
	a := "1"
	if c.A != 255 {
		a = fmt.Sprintf("%.3f", float32(c.A)/255)
	}
	return fmt.Sprintf("rgb(%d %d %d / %s)", c.R, c.G, c.B, a)
}

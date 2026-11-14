// Package kit is Godwit's component set: shadcn/ui Nova looks (neutral) built
// on mygo's *Base widgets. Colors come only from theme.Of(c); variants are
// arguments, not method chains.
package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// ink is the color of shadows and backdrops: dark in either appearance.
func ink(t theme.Tokens) ui.Color {
	if t.Dark {
		return t.Background
	}
	return t.Foreground
}

// colors animates color changes (hover, selection) over d.
// controlRadius is the corner of a control h high: Md under the default 32
// (the 28 and 24 ones), Lg from there, so that a button, an input and a
// select of one height, side by side, round alike.
func controlRadius(c *ui.Context, h float32) float32 {
	if h < c.Theme().Space(8) {
		return theme.Of(c).Radius.Md
	}
	return theme.Of(c).Radius.Lg
}

func colors(d time.Duration) ui.ElementTransition {
	return ui.ElementTransition{Colors: true, Duration: d}
}

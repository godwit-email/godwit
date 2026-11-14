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
// tint is the fill of what stands a step off the surface it is on: n steps
// of the foreground's hover tint, which shows on a dialog's frost, on a card
// and on the window's material as it does on white, where one step is the
// hover gray. A solid gray is lighter than a frosted panel and goes unseen
// there. A badge, a tab list's track, a secondary button at rest and a
// chosen choice are one step; hovered and pressed add one each.
func tint(t theme.Tokens, n float32) ui.Color { return t.HoverTint.Alpha(n) }

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

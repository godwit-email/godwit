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
func colors(d time.Duration) ui.ElementTransition {
	return ui.ElementTransition{Colors: true, Duration: d}
}

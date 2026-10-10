package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Elevate gives e the shadow s, each layer in the ink: dark in either
// appearance, as shadcn's black shadows are.
func Elevate(c *ui.Context, e ui.Element, s theme.Elevation) ui.Element {
	ink := ink(theme.Of(c))
	for _, l := range s {
		e.Shadow(0, l.Y, l.Blur, l.Spread, ink.Alpha(l.Alpha))
	}
	return e
}

// Float makes e a floating input box: light, the ShadowFloat ring and lift
// (no border: the ring is its outline); dark, a FloatInset inner edge (an inset
// shadow, no border).
func Float(c *ui.Context, e ui.Element) ui.Element {
	if t := theme.Of(c); t.Dark {
		return e.InsetShadow(0, 0, theme.FloatInsetBlur, 0, t.FloatInset)
	}
	return Elevate(c, e, theme.ShadowFloat)
}

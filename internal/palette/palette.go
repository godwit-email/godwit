// Package palette holds the Tailwind CSS colors Godwit draws with, copied
// verbatim from tailwindcss v4.1.14 theme.css (oklch lightness as 0–1).
// Only the colors in use are here; add more from the same file.
package palette

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// OKLCH is a color as CSS's oklch(L C H): lightness 0–1, chroma, hue in degrees.
type OKLCH struct{ L, C, H float32 }

// Color is the color to draw, wide where the screen is.
func (o OKLCH) Color() ui.Color { return ui.Oklch(o.L, o.C, o.H) }

// Mid is the color halfway between a and b: each component averaged, the
// hue along the shorter arc. A gray (chroma 0) has no hue, so the mid takes
// the other's.
func Mid(a, b OKLCH) OKLCH {
	h := (a.H + b.H) / 2
	switch {
	case a.C == 0:
		h = b.H
	case b.C == 0:
		h = a.H
	case math.Abs(float64(a.H-b.H)) > 180:
		h = float32(math.Mod(float64(h)+180, 360))
	}
	return OKLCH{(a.L + b.L) / 2, (a.C + b.C) / 2, h}
}

var (
	White = OKLCH{1, 0, 0} // #fff

	Neutral50  = OKLCH{0.985, 0, 0}
	Neutral100 = OKLCH{0.97, 0, 0}
	Neutral200 = OKLCH{0.922, 0, 0}
	Neutral300 = OKLCH{0.87, 0, 0}
	Neutral400 = OKLCH{0.708, 0, 0}
	Neutral500 = OKLCH{0.556, 0, 0}
	Neutral600 = OKLCH{0.439, 0, 0}
	Neutral700 = OKLCH{0.371, 0, 0}
	Neutral800 = OKLCH{0.269, 0, 0}
	Neutral900 = OKLCH{0.205, 0, 0}
	Neutral950 = OKLCH{0.145, 0, 0}

	Green50  = OKLCH{0.982, 0.018, 155.826}
	Green100 = OKLCH{0.962, 0.044, 156.743}
	Green600 = OKLCH{0.627, 0.194, 149.214}
	Green700 = OKLCH{0.527, 0.154, 150.069}
	Green800 = OKLCH{0.448, 0.119, 151.328}

	Amber50  = OKLCH{0.987, 0.022, 95.277}
	Amber300 = OKLCH{0.879, 0.169, 91.605}
	Amber500 = OKLCH{0.769, 0.188, 70.08}
	Amber800 = OKLCH{0.473, 0.137, 46.201}

	Red50  = OKLCH{0.971, 0.013, 17.38}
	Red600 = OKLCH{0.577, 0.245, 27.325}
	Red700 = OKLCH{0.505, 0.213, 27.518}
)

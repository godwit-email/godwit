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
	Green200 = OKLCH{0.925, 0.084, 155.995}
	Green600 = OKLCH{0.627, 0.194, 149.214}
	Green700 = OKLCH{0.527, 0.154, 150.069}
	Green800 = OKLCH{0.448, 0.119, 151.328}
	Green300 = OKLCH{0.871, 0.15, 154.449}
	Green950 = OKLCH{0.266, 0.065, 152.934}

	Amber50  = OKLCH{0.987, 0.022, 95.277}
	Amber300 = OKLCH{0.879, 0.169, 91.605}
	Amber500 = OKLCH{0.769, 0.188, 70.08}
	Amber800 = OKLCH{0.473, 0.137, 46.201}
	Amber950 = OKLCH{0.279, 0.077, 45.635}

	Yellow200 = OKLCH{0.945, 0.129, 101.54}
	Yellow100 = OKLCH{0.973, 0.071, 103.193}
	Yellow900 = OKLCH{0.421, 0.095, 57.708}

	Blue500 = OKLCH{0.623, 0.214, 259.815}

	Red50  = OKLCH{0.971, 0.013, 17.38}
	Red200 = OKLCH{0.885, 0.062, 18.334}
	Red600 = OKLCH{0.577, 0.245, 27.325}
	Red700 = OKLCH{0.505, 0.213, 27.518}
	Red300 = OKLCH{0.808, 0.114, 19.571}
	Red800 = OKLCH{0.444, 0.177, 26.899}
	Red950 = OKLCH{0.258, 0.092, 26.042}

	Red100 = OKLCH{0.936, 0.032, 17.717}
	Red500 = OKLCH{0.637, 0.237, 25.331}

	Orange100 = OKLCH{0.954, 0.038, 75.164}
	Orange200 = OKLCH{0.901, 0.076, 70.697}
	Orange300 = OKLCH{0.837, 0.128, 66.29}
	Orange500 = OKLCH{0.705, 0.213, 47.604}
	Orange700 = OKLCH{0.553, 0.195, 38.402}
	Orange800 = OKLCH{0.47, 0.157, 37.304}
	Orange950 = OKLCH{0.266, 0.079, 36.259}

	Amber100 = OKLCH{0.962, 0.059, 95.617}
	Amber200 = OKLCH{0.924, 0.12, 95.746}
	Amber700 = OKLCH{0.555, 0.163, 48.998}

	Green500 = OKLCH{0.723, 0.219, 149.579}

	Blue100 = OKLCH{0.932, 0.032, 255.585}
	Blue200 = OKLCH{0.882, 0.059, 254.128}
	Blue300 = OKLCH{0.809, 0.105, 251.813}
	Blue700 = OKLCH{0.488, 0.243, 264.376}
	Blue800 = OKLCH{0.424, 0.199, 265.638}
	Blue950 = OKLCH{0.282, 0.091, 267.935}

	Violet100 = OKLCH{0.943, 0.029, 294.588}
	Violet200 = OKLCH{0.894, 0.057, 293.283}
	Violet300 = OKLCH{0.811, 0.111, 293.571}
	Violet500 = OKLCH{0.606, 0.25, 292.717}
	Violet700 = OKLCH{0.491, 0.27, 292.581}
	Violet800 = OKLCH{0.432, 0.232, 292.759}
	Violet950 = OKLCH{0.283, 0.141, 291.089}

	Pink100 = OKLCH{0.948, 0.028, 342.258}
	Pink200 = OKLCH{0.899, 0.061, 343.231}
	Pink300 = OKLCH{0.823, 0.12, 346.018}
	Pink500 = OKLCH{0.656, 0.241, 354.308}
	Pink700 = OKLCH{0.525, 0.223, 3.958}
	Pink800 = OKLCH{0.459, 0.187, 3.815}
	Pink950 = OKLCH{0.284, 0.109, 3.907}
)

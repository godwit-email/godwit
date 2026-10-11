package palette

import "testing"

func TestPaletteValues(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want OKLCH
	}{
		{"neutral-500", Neutral500, OKLCH{0.556, 0, 0}},
		{"red-600", Red600, OKLCH{0.577, 0.245, 27.325}},
		{"amber-50", Amber50, OKLCH{0.987, 0.022, 95.277}},
		{"white", White, OKLCH{1, 0, 0}},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if l := Mid(Neutral100, Neutral200).L; l != 0.946 {
		t.Errorf("Mid(neutral-100, neutral-200).L = %v, want 0.946", l)
	}
	// A gray has no hue of its own: the mid takes the other's.
	if h := Mid(Neutral500, Red600).H; h != Red600.H {
		t.Errorf("Mid(gray, red).H = %v, want %v", h, Red600.H)
	}
	// Hues meet along the shorter arc, across 0°.
	if h := Mid(OKLCH{0.5, 0.1, 350}, OKLCH{0.5, 0.1, 30}).H; h != 10 {
		t.Errorf("Mid hue across 0° = %v, want 10", h)
	}
	if Neutral500.Color() == Red600.Color() {
		t.Error("Color() gives one color for gray and red")
	}
}

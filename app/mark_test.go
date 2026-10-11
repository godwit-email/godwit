package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/internal/theme"
)

// TestMarkLogoCentred: a provider's logo sits centred on its tile, not
// shifted by the tile's outline.
func TestMarkLogoCentred(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(core.Settings{Theme: "light", Density: "compact", FontSize: 13, BodyFontSize: 14}, false))
		ui.Box(c).Padding(20).Children(func() { mark(c, auth.Provider{ID: "qq"}, theme.MarkLg) })
	}, 100, 100)
	img := tt.Image()
	x0, x1 := 100, 0
	for y := 20; y < 60; y++ {
		for x := 20; x < 60; x++ {
			if c := img.RGBAAt(x, y); c.R < 60 && c.G < 60 && c.B < 60 { // the penguin's black
				x0, x1 = min(x0, x), max(x1, x)
			}
		}
	}
	// The penguin's ink is centred in its 256 px picture; on the 40 px tile at
	// 20..60 its centre is 40 (a full-size picture, clipped by the outline,
	// puts it at 41).
	if mid := float32(x0+x1) / 2; mid < 39.5 || mid > 40.5 {
		t.Errorf("logo ink spans x %d..%d, centre %v, want 40", x0, x1, mid)
	}
}

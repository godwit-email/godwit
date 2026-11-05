package app

import (
	"bytes"
	"image/png"
	"testing"
)

func TestMenuIconPNGIsATemplateOfEveryMailAction(t *testing.T) {
	for action, name := range mailActionIcons {
		data, err := menuIconPNG(name)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
			t.Fatalf("%s: bounds %v", action, b)
		}
		ink, clear := false, false
		for y := range 32 {
			for x := range 32 {
				_, _, _, a := img.At(x, y).RGBA()
				ink = ink || a > 0
				clear = clear || a == 0
			}
		}
		if !ink || !clear {
			t.Fatalf("%s: ink %v, transparent %v", action, ink, clear)
		}
		if menuIcon(name) == nil {
			t.Fatalf("%s: no icon", action)
		}
	}
}

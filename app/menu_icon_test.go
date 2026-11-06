package app

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestMenuIconPNGIsATemplateOfEveryMailAction(t *testing.T) {
	for action, name := range mailActionIcons {
		data, err := menuIconPNG(name, theme.Light().Foreground)
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

func TestLabelMenuIconIsInTheLabelsColor(t *testing.T) {
	tk := theme.Light()
	want := tk.Label("red").Text
	data, err := menuIconPNG(labelIcon("bill"), want)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for y := range 32 {
		for x := range 32 {
			r, g, b, a := img.At(x, y).RGBA()
			found = found || a == 0xffff && uint8(r>>8) == want.R && uint8(g>>8) == want.G && uint8(b>>8) == want.B
		}
	}
	if !found {
		t.Fatalf("no pixel of %+v", want)
	}
	red, blue := labelMenuIcon(tk, core.Label{Icon: "bill", Color: "red"}), labelMenuIcon(tk, core.Label{Icon: "bill", Color: "blue"})
	if red == nil || blue == nil || red == blue {
		t.Fatalf("icons %p %p", red, blue)
	}
}

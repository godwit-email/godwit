package main

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/egoist/mygo"
)

func TestWindowOptionsPerOS(t *testing.T) {
	for _, tc := range []struct {
		goos     string
		vibrancy mygo.Vibrancy
		barH     int
	}{
		{"darwin", mygo.VibrancySidebar, 0},
		{"windows", mygo.VibrancyMica, 40},
		{"linux", mygo.VibrancyNone, 0},
	} {
		o := windowOptions(tc.goos)
		if o.Width != 1280 || o.Height != 820 || o.MinWidth != 960 || o.MinHeight != 600 || o.StateKey != "main" || o.Title != "Godwit" {
			t.Errorf("%s: size %+v", tc.goos, o)
		}
		if o.TitleBarStyle != mygo.TitleBarHiddenInset || o.Vibrancy != tc.vibrancy || o.TitleBarHeight != tc.barH {
			t.Errorf("%s: title bar %v %v %d", tc.goos, o.TitleBarStyle, o.Vibrancy, o.TitleBarHeight)
		}
	}
}

func TestTrayIcon(t *testing.T) {
	b, err := trayIcon()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != trayPx || img.Bounds().Dy() != trayPx {
		t.Fatalf("tray icon %v, %v", img.Bounds(), err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the corner is not transparent")
	}
	ink := false // the mark is black: a template image
	for y := range trayPx {
		for x := range trayPx {
			if r, g, bl, a := img.At(x, y).RGBA(); a == 0xffff && r|g|bl == 0 {
				ink = true
			}
		}
	}
	if !ink {
		t.Fatal("no black mark")
	}
}

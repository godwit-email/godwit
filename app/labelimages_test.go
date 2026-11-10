package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// An uploaded mark that brings its own background (an app-icon style tile) fills
// the badge; one drawn on a transparent canvas keeps a margin on the white tile.

func solidPNG(t *testing.T, c color.NRGBA, hole bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			// a mark in the middle only, the edges are the canvas
			if !hole || (x > 8 && x < 24 && y > 8 && y < 24) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func TestFillsCanvas(t *testing.T) {
	green := color.NRGBA{0x5e, 0xc5, 0x66, 0xff}
	for name, tc := range map[string]struct {
		ct   string
		data []byte
		want bool
	}{
		"png opaque to the edges":     {"image/png", solidPNG(t, green, false), true},
		"png mark on transparent":     {"image/png", solidPNG(t, green, true), false},
		"svg with a full square tile": {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256"><rect width="256" height="256" rx="58" fill="#5ec566"/><path d="M10 10"/></svg>`), true},
		"svg full tile, x and y":      {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect x="0" y="0" width="100" height="100" fill="red"/></svg>`), true},
		"svg mark only":               {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 215"><g fill="none" stroke="#5ec566"><path d="M94 41A79 79 0 0 0 94 199"/></g><rect x="99" y="96" width="93" height="24"/></svg>`), false},
		"svg tile of no fill":         {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect width="100" height="100" fill="none" stroke="red"/></svg>`), false},
		"not an image":                {"image/png", []byte("hello"), false},
		"empty":                       {"image/png", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, fillsCanvas(tc.ct, tc.data))
		})
	}
}

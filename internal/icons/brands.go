package icons

import (
	"bytes"
	"embed"
	"image/png"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/web"
)

//go:embed brands/*.png
var brandFiles embed.FS

var (
	brandOnce sync.Once
	brands    map[string]*ui.Bitmap
)

// BrandIcon returns the glyph of a model provider ID, as "chatgpt" (from
// web.BrandIcons; ui.Icon tints it), or nil for a provider without one.
func BrandIcon(id string) *ui.SVG {
	return glyphs[id]
}

var glyphs = func() map[string]*ui.SVG {
	m := make(map[string]*ui.SVG, len(web.BrandIcons))
	for id := range web.BrandIcons {
		b, _ := web.BrandIcon(id)
		m[id] = ui.MustParseSVG(b)
	}
	return m
}()

// Brand returns the mark of a provider ID, as "qq" (see brands/README.md),
// or nil for a provider without one.
func Brand(id string) *ui.Bitmap {
	brandOnce.Do(func() {
		brands = map[string]*ui.Bitmap{}
		entries, _ := brandFiles.ReadDir("brands")
		for _, e := range entries {
			name, ok := strings.CutSuffix(e.Name(), ".png")
			if !ok {
				continue
			}
			b, _ := brandFiles.ReadFile("brands/" + e.Name())
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				panic("icons: brand " + name + ": " + err.Error())
			}
			brands[name] = ui.NewBitmap(img)
		}
	})
	return brands[id]
}

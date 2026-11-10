package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
	_ "golang.org/x/image/webp"
)

// A label may show an uploaded image (a client's logo) instead of a glyph.

// labelImage is the decoded image of a label whose Icon is image:<n>, nil while
// it loads or when it does not decode. One per label: a new upload has a new key.
func (a *App) labelImage(l core.Label) ui.ImageSource {
	if !strings.HasPrefix(l.Icon, "image:") {
		return nil
	}
	if e, ok := a.labelImages[l.ID]; ok && e.key == l.Icon {
		return e.img
	}
	if a.labelImages == nil {
		a.labelImages = map[int64]accountImage{}
	}
	a.labelImages[l.ID] = accountImage{key: l.Icon}
	var img ui.ImageSource
	a.do(func(ctx context.Context) error {
		icon, err := a.svc.LabelIcon(ctx, l.ID)
		if err == nil {
			img = decodeIcon(icon.ContentType, icon.Data)
		}
		return err
	}, func(error) {
		if e := a.labelImages[l.ID]; e.key == l.Icon {
			a.labelImages[l.ID] = accountImage{key: l.Icon, img: img}
		}
	})
	return nil
}

// labelImageFills is whether the label's uploaded image is drawn edge to edge (the user's choice).
func (a *App) labelImageFills(l core.Label) bool { return core.LabelImageFills(l.Icon) }

// labelGlyph draws a label's image, or its glyph in its color.
func (a *App) labelGlyph(c *ui.Context, l core.Label, size float32) {
	if l.Icon == core.LabelIconMonogram || strings.HasPrefix(l.Icon, "image:") {
		if img := a.labelImage(l); img != nil || l.Icon == core.LabelIconMonogram {
			kit.LabelBadge(c, kit.LabelBadgeOpts{Name: l.Name, Size: size, Color: theme.Of(c).Label(l.Color), Image: img, Fill: a.labelImageFills(l)}).Margin(0, c.Theme().Space(0.5), 0, 0)
		} else {
			ui.Row(c).Size(size, size) // keeps its place while it loads
		}
		return
	}
	ui.Icon(c, icons.Get("label-"+l.Icon)).Size(size, size).TextColor(theme.Of(c).Label(l.Color).Text)
}

// pickLabelImage asks for an image off the UI thread and selects it in the label
// dialog, if it still shows.
func (v *labelsView) pickLabelImage() {
	a := v.a
	var data []byte
	var img ui.ImageSource
	var fill bool
	a.do(func(context.Context) error {
		path, err := a.plat.OpenImage()
		if err != nil || path == "" {
			return err
		}
		if fi, err := os.Stat(path); err != nil {
			return err
		} else if fi.Size() > core.AccountIconMax {
			return core.ErrAccountIcon
		}
		if data, err = os.ReadFile(path); err != nil {
			return err
		}
		ct := "image/png" // any bitmap: DecodeBitmap sniffs
		if strings.EqualFold(filepath.Ext(path), ".svg") {
			ct = "image/svg+xml"
		}
		if img = decodeIcon(ct, data); img == nil {
			return core.ErrAccountIcon
		}
		fill = fillsCanvas(ct, data)
		return nil
	}, func(err error) {
		if err != nil || img == nil || !v.dlg {
			a.toastErr(err)
			return
		}
		v.image, v.imagePreview, v.fill = data, img, fill // the suggestion: the user picks
		v.icon = "image:new"                              // the image is now the chosen mark (core makes its real key)
	})
}

// dialogImage is the image the label dialog shows: the one just picked, else the
// label's own, else nil (a glyph).
func (v *labelsView) dialogImage() ui.ImageSource {
	if v.imagePreview != nil {
		return v.imagePreview
	}
	if strings.HasPrefix(v.icon, "image:") {
		return v.a.labelImage(v.editLabel)
	}
	return nil
}

// fillsCanvas is whether an uploaded mark brings its own background, as an app
// icon's tile does: then the badge shows it edge to edge, where a mark drawn on a
// transparent canvas gets a margin on the white tile. A raster fills it when the
// middle of each of its four edges is opaque (not the corners, which a rounded tile
// leaves clear); an svg when it draws a rect as large as its viewBox, with a fill.
func fillsCanvas(contentType string, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if contentType == "image/svg+xml" {
		return svgFillsCanvas(data)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	for _, p := range []image.Point{
		{b.Min.X + b.Dx()/2, b.Min.Y}, {b.Min.X + b.Dx()/2, b.Max.Y - 1},
		{b.Min.X, b.Min.Y + b.Dy()/2}, {b.Max.X - 1, b.Min.Y + b.Dy()/2},
	} {
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a < opaqueEdge {
			return false
		}
	}
	return true
}

// opaqueEdge is the alpha, of 0xffff, from which an edge counts as opaque.
const opaqueEdge = 0xe000

func svgFillsCanvas(data []byte) bool {
	var minX, minY, w, h float64
	haveBox := false
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := func(name string) string {
			for _, a := range el.Attr {
				if a.Name.Local == name {
					return a.Value
				}
			}
			return ""
		}
		switch el.Name.Local {
		case "svg":
			if !haveBox {
				f := strings.Fields(strings.ReplaceAll(attr("viewBox"), ",", " "))
				if len(f) != 4 {
					return false
				}
				v := make([]float64, 4)
				for i := range v {
					if v[i], err = strconv.ParseFloat(f[i], 64); err != nil {
						return false
					}
				}
				minX, minY, w, h, haveBox = v[0], v[1], v[2], v[3], true
			}
		case "rect":
			fill := strings.ToLower(strings.TrimSpace(attr("fill")))
			if !haveBox || fill == "" || fill == "none" || fill == "transparent" {
				continue
			}
			num := func(name string, def float64) (float64, bool) {
				v := strings.TrimSuffix(strings.TrimSpace(attr(name)), "px")
				if v == "" {
					return def, true
				}
				if v == "100%" {
					return -1, true // the whole side
				}
				f, err := strconv.ParseFloat(v, 64)
				return f, err == nil
			}
			x, ok1 := num("x", minX)
			y, ok2 := num("y", minY)
			rw, ok3 := num("width", -2)
			rh, ok4 := num("height", -2)
			if !ok1 || !ok2 || !ok3 || !ok4 || x != minX || y != minY {
				continue
			}
			if (rw == w || rw == -1) && (rh == h || rh == -1) {
				return true
			}
		}
	}
}

// dialogFill is whether the label dialog draws its image edge to edge.
func (v *labelsView) dialogFill() bool { return v.fill }

// dialogIcon is the Icon to save for a kept image: its image:<n>, with the fill the user chose.
func (v *labelsView) dialogIcon() string {
	if !strings.HasPrefix(v.icon, "image:") || v.image != nil {
		return v.icon
	}
	icon := strings.TrimSuffix(v.icon, core.LabelImageFillSuffix)
	if v.fill {
		icon += core.LabelImageFillSuffix
	}
	return icon
}

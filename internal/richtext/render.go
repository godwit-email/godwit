package richtext

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
	"golang.org/x/image/colornames"
)

// RenderOptions are how Render draws a document. FontSize defaults to the
// body font size, Background (what the document is drawn on, for the
// contrast check) to the theme's. Images returns a loaded image or nil for
// a placeholder. OnLink gets the target of an activated link; the host
// shows it before opening it.
type RenderOptions struct {
	FontSize   float32
	Background ui.Color
	Images     func(src string) *ui.Bitmap
	OnLink     func(url string)
}

const mono = "Geist Mono"

// Render draws d read-only.
func Render(c *ui.Context, d Doc, o RenderOptions) ui.Element {
	t := theme.Of(c)
	if o.FontSize == 0 {
		o.FontSize = t.BodyFontSize
	}
	if o.Background.A == 0 {
		o.Background = t.Background
	}
	r := &renderer{c: c, o: o, t: t}
	return ui.Column(c).Gap(c.Theme().Space(3)).Children(func() {
		var numbers []int // the next number of each list level
		for _, b := range d.Blocks {
			if b.Kind != ListItem {
				numbers = numbers[:0]
			}
			switch b.Kind {
			case Heading:
				scale := theme.HeadingScale[min(max(b.Level, 1), 6)-1]
				r.text(b.Inlines, o.FontSize*scale, "").Bold()
			case Quote:
				r.quote(max(b.Level, 1), b.Inlines)
			case ListItem:
				lv := max(b.Level, 1)
				for len(numbers) < lv {
					numbers = append(numbers, 1)
				}
				numbers = numbers[:lv]
				r.item(b, numbers[lv-1])
				numbers[lv-1]++
			case Pre:
				ui.Box(c).Background(t.Hover).Radius(t.Radius.Md).Padding(c.Theme().Space(3)).Children(func() {
					r.text(b.Inlines, o.FontSize, mono)
				})
			case Rule:
				ui.Divider(c)
			case Table:
				r.table(b)
			case ImageBlock:
				r.image(b.Image)
			default:
				r.text(b.Inlines, o.FontSize, "")
			}
		}
	})
}

type renderer struct {
	c *ui.Context
	o RenderOptions
	t theme.Tokens
}

func (r *renderer) text(ins []Inline, size float32, font string) ui.Element {
	e := ui.RichText(r.c).FontSize(size).TextColor(r.t.Foreground)
	if font != "" {
		e = e.Font(font)
	}
	return e.Children(func() {
		for _, in := range ins {
			r.inline(in)
		}
	})
}

func (r *renderer) inline(in Inline) {
	spans := Autospace(in.Text)
	var col *ui.Color
	if in.Link == "" {
		col = textColor(in.Color, r.o.Background)
	}
	for i := range spans {
		s := &spans[i]
		s.Italic, s.Strikethrough = in.Italic, in.Strike
		s.Underline = in.Underline || in.Link != ""
		if in.Bold {
			s.Weight = 600
		}
		if in.Code {
			s.Font, s.Background = mono, r.t.Hover
		}
		if col != nil {
			s.Color = *col
		}
	}
	if in.Link == "" || !safeLink(in.Link) {
		ui.RichText(r.c, spans...)
		return
	}
	l := ui.Link(r.c, "", "").Children(func() { ui.RichText(r.c, spans...) })
	if l.Clicked() && r.o.OnLink != nil {
		r.o.OnLink(in.Link)
	}
}

// quote nests level bars around the text.
func (r *renderer) quote(level int, ins []Inline) {
	ui.Box(r.c).BorderWidth(0, 0, 0, theme.QuoteBar).BorderColor(r.t.Border).Padding(0, 0, 0, r.c.Theme().Space(3)).Children(func() {
		if level > 1 {
			r.quote(level-1, ins)
		} else {
			r.text(ins, r.o.FontSize, "").TextColor(r.t.Muted)
		}
	})
}

func (r *renderer) item(b Block, n int) {
	marker := "•"
	if b.Ordered {
		marker = strconv.Itoa(n) + "."
	}
	indent := r.c.Theme().Space(5) * float32(max(b.Level, 1)-1)
	ui.Row(r.c).AlignItems(ui.Start).Padding(0, 0, 0, indent).Children(func() {
		ui.Text(r.c, marker).FontSize(r.o.FontSize).TextColor(r.t.Muted).Width(r.c.Theme().Space(5)).Shrink(0)
		r.text(b.Inlines, r.o.FontSize, "").Grow(1)
	})
}

func (r *renderer) table(b Block) {
	cols := 0
	for _, row := range b.Rows {
		cols = max(cols, len(row))
	}
	if cols == 0 {
		return
	}
	ui.Grid(r.c).Columns(cols).Border(theme.Hairline, r.t.Border).Radius(r.t.Radius.Md).Clip().Children(func() {
		for i, row := range b.Rows {
			for j := range cols {
				cell := ui.Box(r.c).PaddingX(r.c.Theme().Space(3)).PaddingY(r.c.Theme().Space(2))
				if j >= len(row) {
					continue
				}
				cell.Children(func() {
					e := r.text(row[j].Inlines, r.o.FontSize, "")
					if i == 0 && b.Header {
						e.Bold()
					}
				})
			}
		}
	})
}

func (r *renderer) image(im *Image) {
	var bm *ui.Bitmap
	if r.o.Images != nil {
		bm = r.o.Images(im.Src)
	}
	w, h := float32(im.Width), float32(im.Height)
	if bm != nil {
		e := ui.Image(r.c, bm).Fit(ui.ScaleDown)
		if w > 0 && h > 0 {
			e = e.Size(w, h)
		}
		if im.Alt != "" {
			e.Label(im.Alt)
		}
		return
	}
	if w <= 0 || h <= 0 {
		w, h = theme.ImagePlaceholder, theme.ImagePlaceholder
	}
	e := ui.Box(r.c).Size(w, h).Border(theme.Hairline, r.t.Border).Radius(r.t.Radius.Md).Center().Children(func() {
		ui.Icon(r.c, icons.Get("image-off")).TextColor(r.t.Muted)
	})
	if im.Alt != "" {
		e.Label(im.Alt)
	}
}

// textColor parses a CSS color (hex, rgb(a), or a name) and returns it
// over bg when it contrasts with bg at least 3:1, else nil.
func textColor(css string, bg ui.Color) *ui.Color {
	col, ok := parseColor(strings.ToLower(strings.TrimSpace(css)))
	if !ok {
		return nil
	}
	col = col.Over(bg)
	col.A = 255
	if contrast(col, bg) < 3 {
		return nil
	}
	return &col
}

func parseColor(s string) (ui.Color, bool) {
	if h, ok := strings.CutPrefix(s, "#"); ok {
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if len(h) != 6 || err != nil {
			return ui.Color{}, false
		}
		return ui.RGB(uint8(v>>16), uint8(v>>8), uint8(v)), true
	}
	if body, ok := strings.CutPrefix(s, "rgb"); ok {
		body = strings.TrimPrefix(body, "a")
		body, ok = strings.CutPrefix(body, "(")
		body, ok2 := strings.CutSuffix(body, ")")
		parts := strings.Split(body, ",")
		if !ok || !ok2 || len(parts) < 3 || len(parts) > 4 {
			return ui.Color{}, false
		}
		var v [4]float64
		v[3] = 1
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return ui.Color{}, false
			}
			v[i] = f
		}
		b := func(f float64) uint8 { return uint8(min(max(f, 0), 255)) }
		return ui.RGBA(b(v[0]), b(v[1]), b(v[2]), float32(min(max(v[3], 0), 1))), true
	}
	if c, ok := colornames.Map[s]; ok {
		return ui.RGBA(c.R, c.G, c.B, float32(c.A)/255), true
	}
	return ui.Color{}, false
}

// contrast is the WCAG contrast ratio of two opaque colors.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func luminance(c ui.Color) float64 {
	ch := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

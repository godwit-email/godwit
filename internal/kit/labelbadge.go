package kit

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// LabelInitial is the letter a label shows when it has no glyph: the first
// letter of its name, or of the part after a " · " when the name has one (a
// "Client · Acme" label shows A). "?" when there is nothing to show.
func LabelInitial(name string) string {
	if i := strings.LastIndex(name, "·"); i >= 0 {
		name = name[i+len("·"):]
	}
	name = strings.TrimSpace(name)
	r, _ := utf8.DecodeRuneInString(name)
	if name == "" {
		return "?"
	}
	return string(unicode.ToUpper(r))
}

// LabelBadgeOpts is a label's mark: its Image, else its initial, in a rounded
// square like an Avatar's.
type LabelBadgeOpts struct {
	Name  string
	Size  float32
	Color theme.LabelColor
	Image ui.ImageSource
	// Fill draws Image edge to edge, for a mark that brings its own background.
	Fill bool
}

// LabelBadge draws a label's image or its initial in a rounded square. An image
// sits on a white tile with a hairline edge, centred and uncut, the way an app icon does.
func LabelBadge(c *ui.Context, o LabelBadgeOpts) ui.Element {
	t := theme.Of(c)
	radius := min(t.Radius.Lg, o.Size/3) // round, whatever the size
	font := t.Text.Xs                    // a letter in a small badge, a step up in a large one
	if o.Size >= theme.IconLg {
		font = t.Text.Sm
	}
	if o.Image != nil {
		// The mark's white tile, as a provider's logo gets in onboarding: the image
		// is contained, so a logo that is not square is not cut, and centred. The
		// outline is laid over the tile instead of being its border, because a
		// border inside the box pushes the image off centre.
		// The image keeps a margin inside the tile, so a mark that fills its canvas
		// does not touch the edge.
		in := o.Size - o.Size/4
		fit := ui.Contain
		if o.Fill {
			in, fit = o.Size, ui.Cover
		}
		return ui.Box(c).Size(o.Size, o.Size).Shrink(0).Radius(radius).Clip().Background(t.MarkTile).Center().Label(o.Name).Children(func() {
			ui.Image(c, o.Image).Size(in, in).Fit(fit)
			ui.Box(c).Absolute().Left(0).Top(0).Size(o.Size, o.Size).Radius(radius).Border(theme.Hairline, t.Outline)
		})
	}
	return ui.Box(c).Size(o.Size, o.Size).Shrink(0).Radius(radius).Background(o.Color.Fill).Center().Label(o.Name).Children(func() {
		ui.Text(c, LabelInitial(o.Name)).FontWeight(600).FontSize(font).LineHeight(theme.LineTight).TextColor(o.Color.Text)
	})
}

package kit

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// DialogSize is a dialog's width: DialogMd for a form, DialogSm for a
// confirmation, DialogLg for a larger form.
type DialogSize uint8

const (
	DialogMd DialogSize = iota
	DialogSm
	DialogLg
)

// DialogOpts are a dialog's size, the muted line under its title, the label
// of its close button, and Danger, which tints the footer for a confirmation.
type DialogOpts struct {
	Size        DialogSize
	Desc, Close string
	Danger      bool
}

// Dialog is a modal panel anchored near the top: a header with the title, the
// description and a close button, a scrolling body, and a footer bar holding
// footer's buttons at the end. body and footer may be nil. Escape, a click
// outside and the close button close it.
func Dialog(c *ui.Context, open *bool, title string, o DialogOpts, body, footer func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionBase)
	width, maxH := float32(theme.SheetWidth), float32(theme.SheetMaxHeight)
	switch o.Size {
	case DialogSm:
		width = theme.DialogSmWidth
	case DialogLg:
		width, maxH = theme.DialogWideWidth, theme.DialogWideMax
	}
	dim := float32(0.1)
	if t.Dark { // a tenth barely darkens the dark window
		dim = 0.4
	}
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogShown = true
		backdrop.Material(scrim{theme.BackdropBlur * k, ink(t).Alpha(dim * k)}).Justify(ui.Start).Padding(th.Space(16), 0, 0, 0)
		panel.Opacity(k).Margin(rise(k), 0, 0, 0).Width(width).MaxHeight(maxH).Radius(t.Radius.X2l).Clip().Background(t.Raised).Border(theme.Hairline, t.Border)
		Elevate(c, panel, theme.ShadowSm)
		ui.Row(c).Shrink(0).AlignItems(ui.Start).Gap(th.Space(4)).Padding(th.Space(4)).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
				Text(c, title, t.Text.Title).FontWeight(600).LineHeight(theme.LineFor(title))
				if o.Desc != "" {
					ui.Text(c, o.Desc).LineHeight(theme.LineFor(o.Desc)).TextColor(t.Muted)
				}
			})
			// Into the corner: the button's own padding keeps the glyph in line with the title.
			IconButton(c, "x", o.Close, ButtonOpts{Variant: Ghost, Size: SizeSm}).Margin(-th.Space(2), -th.Space(2), 0, 0).OnClick(func() { *open = false })
		})
		if body != nil {
			ui.Scroll(c).Shrink(1).Children(func() {
				ui.Column(c).Padding(0, th.Space(4), th.Space(4), th.Space(4)).Gap(th.Space(4)).Children(body)
			})
		}
		if footer != nil {
			bar := ui.Row(c).Shrink(0).Gap(th.Space(2)).AlignItems(ui.Center).Justify(ui.End).Padding(th.Space(3), th.Space(4)).
				BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Background(t.Sidebar)
			if o.Danger {
				bar.BorderColor(t.DangerBorder).Background(t.DangerBg)
			}
			bar.Children(footer)
		}
	})
}

// dialogShown is whether a Dialog was built since DialogShown last said. UI thread only.
var dialogShown bool

// DialogShown reports whether a Dialog was built since it was last called,
// and forgets it. A window on a transparent material paints itself opaque
// under one: the backdrop's blur covers only what is opaque, and sharp text
// on the material would show through it.
func DialogShown() bool {
	s := dialogShown
	dialogShown = false
	return s
}

// scrim is a dialog's backdrop: what is behind it blurred, then tinted.
type scrim struct {
	blur float32
	tint ui.Color
}

func (s scrim) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	glass.Blur{Radius: s.blur}.PaintMaterial(p, box, radii)
	p.Fill(box, s.tint, 0)
}

// Popover is a panel below anchor while *open; a press outside or Escape closes it.
func Popover(c *ui.Context, anchor ui.Element, open *bool, body func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionFast)
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.Opacity(k).Margin(theme.PopupOffset+rise(k), 0, 0, 0).Padding(th.Space(3)).Radius(t.Radius.Lg).Gap(th.Space(2)).Background(t.Background).Border(theme.Hairline, t.Border)
		Elevate(c, panel, theme.ShadowMd)
		body()
	})
}

// Tooltip shows text by anchor on keyboard focus at once and on hover after a
// pause, and describes anchor to assistive technology.
func Tooltip(c *ui.Context, anchor ui.Element, text string) { tooltip(c, anchor, text, true) }

// TooltipLabelled is Tooltip for an anchor whose Label already says text.
func TooltipLabelled(c *ui.Context, anchor ui.Element, text string) { tooltip(c, anchor, text, false) }

// tooltip is Tooltip; describe is false where anchor's Label already says text.
func tooltip(c *ui.Context, anchor ui.Element, text string, describe bool) {
	t, th := theme.Of(c), c.Theme()
	if describe {
		anchor.Description(text)
	}
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.Padding(th.Space(1), th.Space(2)).Radius(t.Radius.Md).Background(t.Primary).TextColor(t.PrimaryText).FontSize(t.Text.Xs)
		tip.Children(func() { ui.Text(c, text) })
	})
}

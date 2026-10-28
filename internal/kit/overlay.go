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
// Alert makes it one question (confirm, delete, open a link): the alert
// backdrop and no close button, Cancel, Escape or a click outside closing it.
type DialogOpts struct {
	Size          DialogSize
	Desc, Close   string
	Danger, Alert bool
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
	scrim := t.Scrim
	if o.Alert {
		scrim = t.AlertScrim
	}
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogShown = true
		backdrop.Background(scrim.Alpha(k)) // Alpha scales: the scrim fades with the panel
		aboveCentre(backdrop, panel)
		panel.Opacity(k).Margin(rise(k), 0, 0, 0).Width(width).MaxHeight(maxH).Radius(t.Radius.X2l).Clip().Border(theme.Hairline, t.Border)
		glassPanel(panel, t)
		Elevate(c, panel, theme.ShadowX2l)
		header := ui.Row(c).Shrink(0).AlignItems(ui.Start).Gap(th.Space(4)).Padding(th.Space(4))
		if body != nil {
			header.Padding(th.Space(4), th.Space(4), th.Space(4)-theme.RingWidth, th.Space(4))
		}
		header.Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
				Text(c, title, t.Text.Title).FontWeight(600).LineHeight(theme.LineFor(title))
				if o.Desc != "" {
					ui.Text(c, o.Desc).LineHeight(theme.LineFor(o.Desc)).TextColor(t.Muted)
				}
			})
			if !o.Alert {
				// Into the corner: the button's own padding keeps the glyph in line with the title.
				IconButton(c, "x", o.Close, ButtonOpts{Variant: Ghost, Size: SizeSm}).Margin(-th.Space(2), -th.Space(2), 0, 0).OnClick(func() { *open = false })
			}
		})
		if body != nil {
			ui.Scroll(c).Shrink(1).Children(func() {
				// Keep the first field's outer ring inside the viewport, without moving the content.
				ui.Column(c).Padding(theme.RingWidth, th.Space(4), th.Space(4), th.Space(4)).Gap(th.Space(4)).Children(body)
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

// aboveCentre sets panel a little above the middle of backdrop: its centre
// at 40% of the window, yet at least 32 below the top. It reads the last
// frame's sizes; the first frame, still transparent, centres the panel.
func aboveCentre(backdrop, panel ui.Element) {
	h, p := backdrop.LayoutBounds().H, panel.LayoutBounds().H
	lift := max(0, min(h*theme.DialogLift, h-p-2*theme.DialogMinTop))
	backdrop.Padding(0, 0, lift, 0)
}

// dialogShown is whether a Dialog was built since DialogShown last said. UI thread only.
var dialogShown bool

// DialogShown reports whether a Dialog was built since it was last called,
// and forgets it. A window on a transparent material paints itself opaque
// under one: the panel's frost covers only what is opaque, and sharp text
// on the material would show through it.
func DialogShown() bool {
	s := dialogShown
	dialogShown = false
	return s
}

// BackdropBlur turns the frosted panels' blur on. Tests turn it off: the
// CPU renderer the Tester draws with blurs pixel by pixel, which under -race
// runs every dialog test many times slower.
var BackdropBlur = true

// Glass frosts the panels of dialogs and the search palette: set where the
// window sits on a material (Deps.Vibrancy); elsewhere they are opaque. The
// backdrop under them only tints the window.
var Glass bool

// glassPanel fills a dialog's panel: frosted glass under Glass, else opaque.
func glassPanel(panel ui.Element, t theme.Tokens) {
	if Glass {
		panel.Material(scrim{theme.BackdropBlur * 4, t.Raised.Alpha(0.8)})
	} else {
		panel.Background(t.Raised)
	}
}

// scrim is what is behind blurred, then tinted: a frosted surface.
type scrim struct {
	blur float32
	tint ui.Color
}

func (s scrim) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	if BackdropBlur {
		glass.Blur{Radius: s.blur}.PaintMaterial(p, box, radii)
	}
	p.Fill(box, s.tint, radii[0]) // its surfaces round every corner alike
}

// Frost fills a row with fill (the surface it sits on) over what is behind it
// blurred: plain at rest, frosted glass as a drag lifts the row over others.
func Frost(fill ui.Color) ui.Material {
	return scrim{theme.BackdropBlur * 2, fill.Alpha(0.7)}
}

// PopoverAt is a panel anchored to anchor at (at, self) with topOffset while *open; a press outside or Escape closes it.
func PopoverAt(c *ui.Context, anchor ui.Element, open *bool, at, self ui.Anchor, topOffset float32, body func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionFast)
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.AttachTo(anchor, at, self).
			Opacity(k).Margin(topOffset+rise(k), 0, 0, 0).Padding(th.Space(3)).
			Radius(t.Radius.Lg).Gap(th.Space(2)).Background(t.Background).Border(theme.Hairline, t.Border)
		Elevate(c, panel, theme.ShadowMd)
		body()
	})
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

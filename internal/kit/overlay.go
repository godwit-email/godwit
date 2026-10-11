package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Dialog is a 560-wide modal panel; Escape and a click outside close it.
func Dialog(c *ui.Context, open *bool, title string, body, footer func()) {
	dialog(c, open, theme.SheetWidth, title, body, footer)
}

// DialogWide is Dialog at 640 wide.
func DialogWide(c *ui.Context, open *bool, title string, body, footer func()) {
	dialog(c, open, theme.DialogWideWidth, title, body, footer)
}

func dialog(c *ui.Context, open *bool, width float32, title string, body, footer func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionBase)
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ink(t).Alpha(0.4 * k))
		panel.Opacity(k).Margin(rise(k), 0, 0, 0).Width(width).Radius(t.Radius.X2l).Padding(th.Space(6)).Gap(th.Space(4)).Background(t.Background).Border(theme.Hairline, t.Border)
		Elevate(c, panel, theme.ShadowLg)
		Text(c, title, t.Text.Title).FontWeight(600)
		body()
		if footer != nil {
			ui.Row(c).Gap(th.Space(2)).Justify(ui.End).Children(footer)
		}
	})
}

// Sheet is a top-anchored panel, the add-account sheet's look: a title and a
// close button over a scrolling body.
func Sheet(c *ui.Context, open *bool, title, closeLabel string, body func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionBase)
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ink(t).Alpha(0.28*k)).Justify(ui.Start).Padding(th.Space(16), 0, 0, 0)
		panel.Opacity(k).Margin(rise(k), 0, 0, 0).Width(theme.SheetWidth).MaxHeight(theme.SheetMaxHeight).Radius(t.Radius.X2l).Clip().Background(t.Background).Border(theme.Hairline, t.Border)
		Elevate(c, panel, theme.ShadowLg)
		ui.Row(c).Padding(th.Space(5), th.Space(6), 0, th.Space(6)).Children(func() {
			Text(c, title, t.Text.Lg).FontWeight(600)
			ui.Spacer(c)
			IconButton(c, "x", closeLabel, ButtonOpts{Variant: Ghost, Size: SizeSm}).OnClick(func() { *open = false })
		})
		ui.Scroll(c).Shrink(1).Children(func() {
			ui.Column(c).Padding(th.Space(4), th.Space(6), th.Space(6), th.Space(6)).Gap(th.Space(5.5)).Children(body)
		})
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

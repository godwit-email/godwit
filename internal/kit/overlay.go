package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Dialog is a 560-wide modal panel; Escape and a click outside close it.
func Dialog(c *ui.Context, open *bool, title string, body, footer func()) {
	t, th := theme.Of(c), c.Theme()
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ink(t).Alpha(0.4))
		panel.Width(theme.SheetWidth).Radius(t.Radius.Xl).Padding(th.Space(6)).Gap(th.Space(4)).Background(t.Background).Border(theme.Hairline, t.Border).
			Shadow(0, theme.ShadowLg.Y, theme.ShadowLg.Blur, 0, ink(t).Alpha(0.25))
		ui.Text(c, title).FontSize(t.Text.Title).FontWeight(600)
		body()
		if footer != nil {
			ui.Row(c).Gap(th.Space(2)).Justify(ui.End).Children(footer)
		}
	})
}

// Popover is a panel below anchor while *open; a press outside or Escape closes it.
func Popover(c *ui.Context, anchor ui.Element, open *bool, body func()) {
	t, th := theme.Of(c), c.Theme()
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.Margin(theme.PopupOffset, 0, 0, 0).Padding(th.Space(3)).Radius(t.Radius.Md).Gap(th.Space(2)).Background(t.Background).Border(theme.Hairline, t.Border).
			Shadow(0, theme.ShadowMd.Y, theme.ShadowMd.Blur, 0, ink(t).Alpha(0.12))
		body()
	})
}

// Tooltip shows text by anchor on keyboard focus at once and on hover after a
// pause, and describes anchor to assistive technology.
func Tooltip(c *ui.Context, anchor ui.Element, text string) {
	t, th := theme.Of(c), c.Theme()
	anchor.Description(text)
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.Padding(th.Space(1), th.Space(2)).Radius(t.Radius.Sm).Background(t.Primary).TextColor(t.PrimaryText).FontSize(t.Text.Xs)
		tip.Children(func() { ui.Text(c, text) })
	})
}

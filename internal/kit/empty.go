package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// EmptyOpts fill an Empty. Actions builds the buttons under the text.
type EmptyOpts struct {
	Icon, Title, Desc string
	Actions           func()
	Dashed            bool
}

// Empty is a centered empty state: an icon tile, a title, a description and
// actions; Dashed draws a dashed frame around it.
func Empty(c *ui.Context, o EmptyOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	e := ui.Column(c).AlignItems(ui.Center).Justify(ui.Center).Gap(th.Space(4)).Padding(th.Space(8), th.Space(6))
	if o.Dashed {
		e.Radius(t.Radius.Xl).Border(theme.DeviceHairline, t.Border).BorderStyle(ui.BorderDashed)
	}
	return e.Children(func() {
		if o.Icon != "" {
			ui.Box(c).Size(theme.EmptyIcon, theme.EmptyIcon).Radius(t.Radius.Lg).Background(t.Hover).Center().Children(func() {
				ui.Icon(c, icons.Get(o.Icon)).Size(theme.IconLg, theme.IconLg).TextColor(t.Foreground)
			})
		}
		ui.Column(c).AlignItems(ui.Center).Gap(th.Space(1)).MaxWidth(theme.EmptyDescMax).Children(func() {
			Text(c, o.Title, t.Text.Title).LineHeight(theme.LineFor(o.Title)).FontWeight(600).TextAlign(ui.Center)
			if o.Desc != "" {
				Text(c, o.Desc, t.Text.Body).LineHeight(theme.LineProse).TextColor(t.Muted).TextAlign(ui.Center)
			}
		})
		if o.Actions != nil {
			ui.Row(c).Gap(th.Space(2)).Justify(ui.Center).Children(o.Actions)
		}
	})
}

// EmptyAction is a button for Empty's Actions: the default size, 12 to each side as the design's.
func EmptyAction(c *ui.Context, label string, o ButtonOpts) ui.Element {
	return Button(c, label, o).PaddingX(c.Theme().Space(3))
}

package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// GroupItem is one icon button of a ButtonGroup; Tip is its tooltip and label.
type GroupItem struct {
	Icon, Tip string
	OnClick   func()
	Disabled  bool
}

// ButtonGroup is ghost icon buttons side by side, unframed; GroupDivider
// parts groups. The icons are Foreground, not a lone icon button's Muted.
func ButtonGroup(c *ui.Context, items ...GroupItem) ui.Element {
	th := c.Theme()
	return ui.Row(c).Gap(th.Space(0.5)).AlignItems(ui.Center).Children(func() {
		for _, it := range items {
			b := IconButton(c, it.Icon, it.Tip, ButtonOpts{Variant: Ghost, Disabled: it.Disabled})
			if !it.Disabled {
				b.TextColor(theme.Of(c).Foreground)
			}
			if it.OnClick != nil {
				b.OnClick(it.OnClick)
			}
		}
	})
}

// GroupDivider is the hairline between ButtonGroups in a toolbar.
func GroupDivider(c *ui.Context) ui.Element {
	return ui.Box(c).Size(theme.Stroke, c.Theme().Space(5)).Shrink(0).Background(theme.Of(c).Border)
}

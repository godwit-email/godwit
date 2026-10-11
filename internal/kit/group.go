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

// ButtonGroup is bordered icon buttons side by side, split by hairlines.
func ButtonGroup(c *ui.Context, items ...GroupItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).Height(theme.GroupHeight).PaddingX(th.Space(0.5)).AlignItems(ui.Center).Radius(t.Radius.Lg).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
		for i, it := range items {
			if i > 0 {
				ui.Box(c).Size(theme.Hairline, theme.GroupSepHeight).Background(t.Border)
			}
			b := IconButton(c, it.Icon, it.Tip, ButtonOpts{Variant: Ghost, Size: SizeSm, Disabled: it.Disabled})
			if it.OnClick != nil {
				b.OnClick(it.OnClick)
			}
		}
	})
}

package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// LabelChipOpts is a mail label as a chip. Icon is a label icon's name without
// the "label-" prefix; Tip shows on hover. Auto draws the chip the model set:
// the same color, an edge instead of a fill.
type LabelChipOpts struct {
	Text, Icon, Tip string
	Color           theme.LabelColor
	Auto            bool
	OnClick         func() // set: the chip is a button
	OnRemove        func() // set: a trailing × named RemoveLabel
	RemoveLabel     string
}

// labelChipHeight is the chip's height in DIPs (the label design: 20).
const labelChipHeight = 20

// LabelChip is a mail label's chip: 20 high, its icon and name in the label's color.
func LabelChip(c *ui.Context, o LabelChipOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	var chip ui.Element
	if o.OnClick != nil {
		chip = ui.ButtonBase(c).Label(o.Text)
		if chip.Clicked() {
			o.OnClick()
		}
	} else {
		chip = ui.Row(c)
	}
	right := th.Space(1.5)
	if o.OnRemove != nil {
		right = th.Space(0.5)
	}
	chip.AlignItems(ui.Center).Gap(th.Space(1)).Height(labelChipHeight).Padding(0, right, 0, th.Space(1.5)).
		Radius(t.Radius.Sm).TextColor(o.Color.Text).FontSize(t.Text.Sm)
	if o.Auto {
		chip.Border(theme.Hairline, o.Color.Edge)
	} else {
		chip.Background(o.Color.Fill).Border(theme.Hairline, o.Color.Fill)
	}
	chip.Children(func() {
		ui.Icon(c, icons.Get("label-"+o.Icon)).Size(theme.IconXs, theme.IconXs)
		ui.Text(c, o.Text).SingleLine()
		if o.OnRemove != nil {
			x := ui.ButtonBase(c).Label(o.RemoveLabel).Size(th.Space(4), th.Space(4)).Radius(t.Radius.Xs).Justify(ui.Center).AlignItems(ui.Center)
			if x.Clicked() {
				o.OnRemove()
			}
			x.Children(func() { ui.Icon(c, icons.Get("x")).Size(10, 10) })
		}
	})
	if o.Tip != "" {
		Tooltip(c, chip, o.Tip)
	}
	return chip
}

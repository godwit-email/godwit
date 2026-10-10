package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// Variant is a button's look.
type Variant int

const (
	Primary Variant = iota
	Outline
	Ghost
	Destructive
)

// ButtonOpts style a button. Icon is a Lucide name shown before the label.
type ButtonOpts struct {
	Variant         Variant
	Small, Disabled bool
	Icon            string
}

func button(c *ui.Context, o ButtonOpts, square bool) ui.Element {
	t, th := theme.Of(c), c.Theme()
	h, padX := th.Space(8), th.Space(3.5)
	if o.Small {
		h, padX = th.Space(7), th.Space(2.5)
	}
	b := ui.ButtonBase(c).Height(h).Radius(t.Radius.Md).Gap(th.Space(1.5)).Justify(ui.Center).Disabled(o.Disabled)
	if square {
		b.Width(h).Radius(t.Radius.Sm)
	} else {
		b.PaddingX(padX)
	}
	hover := b.Hovered() && !o.Disabled
	switch o.Variant {
	case Primary:
		b.Background(t.Primary).TextColor(t.PrimaryText).FontWeight(500)
		if hover {
			b.Background(t.Primary.Mix(t.Background, 0.1))
		}
	case Outline:
		b.Background(t.Background).Border(theme.Hairline, t.Border).TextColor(t.Foreground)
		if hover {
			b.Background(t.Hover)
		}
	case Ghost:
		b.TextColor(t.Foreground)
		if square {
			b.TextColor(t.Muted)
		}
		if hover {
			b.Background(t.Hover).TextColor(t.Foreground)
		}
	case Destructive:
		b.TextColor(t.Danger)
		if hover {
			b.Background(t.DangerBg)
		}
	}
	if o.Disabled {
		b.Opacity(0.5)
	}
	return b
}

// Button is a button of a label, 32 high (28 small).
func Button(c *ui.Context, label string, o ButtonOpts) ui.Element {
	return button(c, o, false).Children(func() {
		if o.Icon != "" {
			ui.Icon(c, icons.Get(o.Icon)).Size(theme.Icon, theme.Icon)
		}
		ui.Text(c, label)
	})
}

// IconButton is a square button showing only an icon, named label for
// assistive technology and tests.
func IconButton(c *ui.Context, icon, label string, o ButtonOpts) ui.Element {
	return button(c, o, true).Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, icons.Get(icon)).Size(theme.Icon, theme.Icon)
	})
}

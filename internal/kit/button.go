package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// Variant is a button's look.
type Variant int

const (
	Default Variant = iota
	Secondary
	Outline
	Ghost
	Destructive
	Link
	DestructiveOutline
)

// ButtonSize is a button's height; the zero value is the 32 px default.
type ButtonSize int

const (
	SizeDefault ButtonSize = iota
	SizeXs                 // IconButton only, round
	SizeSm
	SizeLg
)

// ButtonOpts style a button. Icon is an icons name shown before the label.
type ButtonOpts struct {
	Variant  Variant
	Size     ButtonSize
	Disabled bool
	Icon     string
}

func button(c *ui.Context, o ButtonOpts, square bool) ui.Element {
	t, th := theme.Of(c), c.Theme()
	h, padX := th.Space(8), th.Space(3.5)
	switch o.Size {
	case SizeXs:
		if square { // icon buttons only; a labelled button keeps the default size
			h, padX = th.Space(4), th.Space(1.5)
		}
	case SizeSm:
		h, padX = th.Space(7), th.Space(2.5)
	case SizeLg:
		h, padX = th.Space(9), th.Space(4.5)
	}
	b := ui.ButtonBase(c).Gap(th.Space(1.5)).Justify(ui.Center).Disabled(o.Disabled).Transition(colors(theme.MotionFast))
	if o.Variant == Link {
		b.TextColor(t.Muted)
		if o.Size == SizeSm {
			b.FontSize(t.Text.Sm)
		}
		if b.Hovered() && !o.Disabled {
			b.TextColor(t.Foreground).Underline()
		}
		if o.Disabled {
			b.Opacity(0.5)
		}
		return b
	}
	b.Height(h).Radius(t.Radius.Lg)
	if square {
		b.Width(h).Radius(t.Radius.Md)
		if o.Size == SizeXs {
			b.Radius(t.Radius.Full)
		}
	} else {
		b.PaddingX(padX)
	}
	hover := b.Hovered() && !o.Disabled
	switch o.Variant {
	case Default:
		b.Background(t.Primary).TextColor(t.PrimaryText).FontWeight(500)
		if hover {
			b.Background(t.Primary.Mix(t.Background, 0.1))
		}
	case Secondary:
		b.Background(t.Hover).TextColor(t.Foreground)
		if hover {
			b.Background(t.SidebarSelected)
		}
	case Outline:
		b.Background(t.Background).Border(theme.Hairline, t.Input).TextColor(t.Foreground)
		Elevate(c, b, theme.ShadowXs)
		if hover {
			b.Background(t.Hover)
		}
	case Ghost:
		b.TextColor(t.Foreground)
		if square {
			b.TextColor(t.Muted)
		}
		if hover {
			b.Background(t.HoverTint).TextColor(t.Foreground)
		}
	case Destructive:
		b.TextColor(t.Danger)
		if hover {
			b.Background(t.DangerBg)
		}
	case DestructiveOutline:
		b.Background(t.Background).Border(theme.Hairline, t.DangerBorder).TextColor(t.Danger)
		if hover {
			b.Background(t.DangerBg)
		}
	}
	if o.Disabled {
		b.Opacity(0.5)
	}
	return b
}

// ButtonFrame is the styled button without children.
func ButtonFrame(c *ui.Context, o ButtonOpts) ui.Element { return button(c, o, false) }

// Button is a button of a label.
func Button(c *ui.Context, label string, o ButtonOpts) ui.Element {
	return button(c, o, false).Label(label).Children(func() {
		if o.Icon != "" {
			ui.Icon(c, icons.Get(o.Icon)).Size(theme.Icon, theme.Icon)
		}
		ui.Text(c, label)
	})
}

// IconButton is a square button showing only an icon, named label for
// assistive technology and tests. A SizeXs one, 16 round, holds a 12 icon.
func IconButton(c *ui.Context, icon, label string, o ButtonOpts) ui.Element {
	b := button(c, o, true).Label(label)
	tooltip(c, b, label, false)
	size := float32(theme.Icon)
	if o.Size == SizeXs {
		size = theme.IconXs
	}
	return b.Children(func() {
		ui.Icon(c, icons.Get(icon)).Size(size, size)
	})
}

// TextLink is an underlined Sm text button in color (a notice's action).
func TextLink(c *ui.Context, label string, color ui.Color) ui.Element {
	return ui.ButtonBase(c).Label(label).Children(func() {
		ui.Text(c, label).FontSize(theme.Of(c).Text.Sm).TextColor(color).Underline()
	})
}

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
	Flat     bool // Outline without its shadow
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
	var b ui.Element
	if o.Disabled {
		// mygo paints a Disabled element, and all inside it, at half
		// opacity; the design has disabled colours instead. So a disabled
		// button is a plain row in those colours, and cover lays the
		// Disabled button over it: that takes the clicks, the focus and
		// assistive technology.
		b = ui.Row(c).Center().Shrink(0).Role(ui.RoleNone)
	} else {
		b = ui.ButtonBase(c)
	}
	b.Gap(th.Space(1.5)).Justify(ui.Center).Transition(colors(theme.MotionFast))
	if o.Variant == Link {
		b.TextColor(t.Muted)
		if o.Disabled {
			b.TextColor(t.Idle)
		}
		if o.Size == SizeSm {
			b.FontSize(t.Text.Sm)
		}
		if b.Hovered() && !o.Disabled {
			b.TextColor(t.Foreground).Underline()
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
		if !o.Flat {
			Elevate(c, b, theme.ShadowXs)
		}
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
		b.TextColor(t.Idle)
		if o.Variant == Default {
			b.Background(t.Hover).TextColor(t.Muted)
		}
	}
	return b
}

// cover lays the Disabled button named label over a disabled button's
// look (see button); it paints nothing, so mygo's dimming shows nothing.
func cover(c *ui.Context, o ButtonOpts, label string) {
	if o.Disabled {
		ui.ButtonBase(c).Label(label).Disabled(true).Absolute().Top(0).Right(0).Bottom(0).Left(0)
	}
}

// ButtonFrame is the styled button without children; it is never
// Disabled (no label for the cover).
func ButtonFrame(c *ui.Context, o ButtonOpts) ui.Element { return button(c, o, false) }

// Button is a button of a label.
func Button(c *ui.Context, label string, o ButtonOpts) ui.Element {
	return button(c, o, false).Label(label).Children(func() {
		if o.Icon != "" {
			ui.Icon(c, icons.Get(o.Icon)).Size(theme.Icon, theme.Icon)
		}
		tx := ui.Text(c, label)
		if o.Disabled {
			tx.Role(ui.RoleNone) // the cover names the button
		}
		cover(c, o, label)
	})
}

// SwapButton is a Button whose label changes in place, as Copy turning
// into Copied: the old label fades out as the new one fades in. It is as
// wide as the widest of label and others, the labels it may show, so it
// never moves what sits beside it.
func SwapButton(c *ui.Context, label string, o ButtonOpts, others ...string) ui.Element {
	th := c.Theme()
	content := func(c *ui.Context, text string) ui.Element {
		return ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			if o.Icon != "" {
				ui.Icon(c, icons.Get(o.Icon)).Size(theme.Icon, theme.Icon)
			}
			ui.Text(c, text).Role(ui.RoleNone) // the button's Label names it
		})
	}
	return button(c, o, false).Label(label).Children(func() {
		// Every label, unseen and of no height, sets the width.
		ui.Column(c).Height(0).Opacity(0).PassThrough().Children(func() {
			for i, l := range append([]string{label}, others...) {
				content(c.Key(i), l)
			}
		})
		content(c.Key(label), label).Absolute().Top(0).Right(0).Bottom(0).Left(0).Justify(ui.Center).
			Transition(ui.ElementTransition{Duration: theme.MotionBase, Enter: &ui.Motion{}, Exit: &ui.Motion{}}) // a zero Motion is transparent: a fade
		cover(c, o, label)
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
		cover(c, o, label)
	})
}

// TextLink is an underlined text button of size in color (a notice's action).
func TextLink(c *ui.Context, label string, color ui.Color, size float32) ui.Element {
	return ui.ButtonBase(c).Label(label).Children(func() {
		Text(c, label, size).TextColor(color).Underline()
	})
}

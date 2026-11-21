package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// Variant is a button's look: Default is the primary, filled one. Danger
// and the colored tones are a Tone on an Outline or Ghost button.
type Variant int

const (
	Default Variant = iota
	Secondary
	Outline
	Ghost
	Link
)

// Size is a control's height, a step of the control scale: a button's, an
// input's or a select's. The zero value is the 32 px default.
type Size int

const (
	SizeDefault Size = iota
	SizeMini         // IconButton only: 16 round with a 12 icon, as a chip's ×
	SizeXs           // 24, in xs text with a 12 icon: an action among chips or in a dense row; buttons only
	SizeSm           // 28, in xs text with a 14 icon beside it (an icon alone stays 16)
	SizeLg           // 36
)

// ButtonOpts style a button. Icon is an icons name shown before the label.
type ButtonOpts struct {
	Variant  Variant
	Size     Size
	Disabled bool
	Flat     bool // Outline without its shadow
	Round    bool // fully rounded (pill or circle, Radius.Full)
	Icon     string
	Trailing string // an icon after the label, as a menu's chevron
	// Tone colors an Outline button (ToneWarning, ToneDanger, ToneSuccess,
	// ToneInfo: the tone's border and text on Background, its fill on hover,
	// no shadow), as the actions of a colored Alert, Toast or notice, and a
	// Ghost one (ToneDanger: Danger text, DangerBg on hover), as a delete
	// in a confirmation. Other looks and the neutral tones ignore it.
	Tone Tone
}

func button(c *ui.Context, o ButtonOpts, square bool) ui.Element {
	t, th := theme.Of(c), c.Theme()
	h, padX := th.Space(8), th.Space(2.5) // shadcn Nova's px-2.5 at every height but the smallest, as the web pages' buttons
	switch o.Size {
	case SizeMini:
		if square { // icon buttons only; a labelled button keeps the default size
			h, padX = th.Space(4), th.Space(1.5)
		}
	case SizeXs:
		h, padX = th.Space(6), th.Space(2)
	case SizeSm:
		h, padX = th.Space(7), th.Space(2.5)
	case SizeLg:
		h = th.Space(9)
	}
	// DimDisabled(false) turns off mygo's half-opacity dimming so a disabled
	// button shows the design's disabled colours instead.
	b := ui.ButtonBase(c).Disabled(o.Disabled).DimDisabled(false)
	b.Gap(th.Space(1.5)).Justify(ui.Center).Transition(colors(theme.MotionFast))
	if o.Size == SizeXs || o.Size == SizeSm { // shadcn Nova's small buttons: smaller text, a tighter gap
		b.Gap(th.Space(1)).FontSize(t.Text.Xs)
	}
	if o.Variant == Link { // inline text: no height, padding or fill, always underlined
		b.TextColor(t.Foreground).Underline()
		switch {
		case o.Disabled:
			b.TextColor(t.Idle)
		case b.Hovered():
			b.TextColor(t.Muted)
		}
		return b
	}
	b.Height(h).Radius(controlRadius(c, h)).FontWeight(500) // what acts is at 500, whatever its look
	if square {
		b.Width(h)
		if o.Size == SizeMini {
			b.Radius(t.Radius.Full)
		}
	} else {
		b.PaddingX(padX) // the same with an icon: the web pages' buttons do not mark theirs, and stay even
	}
	if o.Round {
		b.Radius(t.Radius.Full)
	}
	hover := b.Hovered() && !o.Disabled
	pressed := hover && b.Pressed() // a step past hover, as CSS's :active
	if bg, border, text, ok := toneColors(t, o.Tone); ok && (o.Variant == Outline || o.Variant == Ghost && o.Tone == ToneDanger) {
		if o.Tone == ToneDanger {
			text = t.Danger
		}
		b.TextColor(text)
		if o.Variant == Outline {
			b.Background(t.Background).Border(theme.Stroke, border)
		}
		switch {
		case pressed && o.Tone == ToneDanger:
			b.Background(t.DangerBg.Mix(t.Danger, 0.12))
		case pressed:
			b.Background(bg.Mix(border, 0.5))
		case hover:
			b.Background(bg)
		}
		if o.Disabled {
			b.TextColor(t.Idle)
		}
		return b
	}
	switch o.Variant {
	case Default:
		b.Background(t.Primary).TextColor(t.PrimaryText)
		if hover {
			b.Background(t.Primary.Mix(t.Background, 0.1))
		}
	case Secondary:
		b.Background(tint(t, 1)).TextColor(t.Foreground)
		if hover {
			b.Background(tint(t, 2))
		}
	case Outline:
		b.Background(t.Background).Border(theme.Stroke, t.Input).TextColor(t.Foreground)
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
	}
	if pressed {
		switch o.Variant {
		case Default:
			b.Background(t.Primary.Mix(t.Background, 0.2))
		case Secondary:
			b.Background(tint(t, 3))
		case Outline: // over its own opaque fill
			b.Background(t.Hover.Mix(t.Foreground, 0.08))
		case Ghost:
			b.Background(t.HoverTint.Alpha(2))
		}
	}
	if o.Disabled {
		b.TextColor(t.Idle)
		if o.Variant == Default { // itself at half, as shadcn's opacity-50: a button on any surface, frosted or not
			b.Background(t.Primary.Alpha(0.5)).TextColor(t.PrimaryText.Alpha(0.5))
		}
	}
	return b
}

// iconSize is the icon of a button of size s, shadcn Nova's: 16, 12 in a 24
// one and a 16 round one, and 14 beside a 28 one's label (alone in a 28
// square it stays 16).
func iconSize(s Size, alone bool) float32 {
	switch {
	case s == SizeMini || s == SizeXs:
		return theme.IconXs
	case s == SizeSm && !alone:
		return theme.IconSm
	}
	return theme.Icon
}

// ButtonFrame is the styled button without children.
func ButtonFrame(c *ui.Context, o ButtonOpts) ui.Element { return button(c, o, false) }

// Button is a button of a label.
func Button(c *ui.Context, label string, o ButtonOpts) ui.Element {
	return button(c, o, false).Label(label).Children(func() {
		if o.Icon != "" {
			ui.Icon(c, icons.Get(o.Icon)).Size(iconSize(o.Size, false), iconSize(o.Size, false))
		}
		ui.Text(c, label)
		if o.Trailing != "" {
			ui.Icon(c, icons.Get(o.Trailing)).Size(theme.IconSm, theme.IconSm)
		}
	})
}

// SwapButton is a Button whose label changes in place, as Copy turning
// into Copied: the old label fades out as the new one fades in. It is as
// wide as the widest of label and others, the labels it may show, and its
// content starts at its left edge, so neither it nor its icon moves.
func SwapButton(c *ui.Context, label string, o ButtonOpts, others ...string) ui.Element {
	th := c.Theme()
	content := func(c *ui.Context, text string) ui.Element {
		return ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			if o.Icon != "" {
				ui.Icon(c, icons.Get(o.Icon)).Size(iconSize(o.Size, false), iconSize(o.Size, false))
			}
			ui.Text(c, text).Role(ui.RoleNone) // the button's Label names it
		})
	}
	return button(c, o, false).Label(label).Children(func() {
		ui.Column(c).AlignItems(ui.Start).Children(func() {
			content(c.Key(label), label).
				Transition(ui.ElementTransition{Duration: theme.MotionBase, Enter: &ui.Motion{}, Exit: &ui.Motion{}}) // a zero Motion is transparent: a fade
			// Every label, unseen and of no height, widens the column to the widest.
			for i, l := range append([]string{label}, others...) {
				content(c.Key(i), l).Height(0).Opacity(0).PassThrough()
			}
		})
	})
}

// IconButton is a square button showing only an icon, named label for
// assistive technology and tests. A SizeMini one, 16 round, and a SizeXs
// one, 24, hold a 12 icon; the others a 16.
func IconButton(c *ui.Context, icon, label string, o ButtonOpts) ui.Element {
	b := button(c, o, true).Label(label)
	tooltip(c, b, label, false)
	size := iconSize(o.Size, true)
	return b.Children(func() {
		ui.Icon(c, icons.Get(icon)).Size(size, size)
	})
}

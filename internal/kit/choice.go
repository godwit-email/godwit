package kit

import (
	"cmp"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// switchTrack draws a switch: a 32×18 track and its thumb.
func switchTrack(c *ui.Context, on bool) ui.Element {
	t := theme.Of(c)
	tr := ui.Row(c).Size(theme.SwitchWidth, theme.SwitchHeight).Shrink(0).Padding(theme.SwitchInset).Radius(t.Radius.Full).Background(t.Input).Justify(ui.Start).Transition(colors(theme.MotionFast))
	thumb := t.Background
	switch {
	case on:
		tr.Background(t.Primary).Justify(ui.End)
	case t.Dark:
		thumb = t.Foreground // a dark thumb would vanish into the dark track
	}
	return tr.Children(func() {
		ui.Box(c).Size(theme.SwitchThumb, theme.SwitchThumb).Radius(t.Radius.Full).Background(thumb).Transition(ui.ElementTransition{Position: true, Duration: theme.MotionFast})
	})
}

// Switch is a 32×18 switch; give it a Label.
func Switch(c *ui.Context, on *bool) ui.Element {
	return ui.SwitchBase(c, on).Radius(theme.Of(c).Radius.Full).Children(func() { switchTrack(c, *on) })
}

// SwitchRow is a full-width switch named label in the settings row shape
// (8/16 padding, at least 48 high): label and help on the left, the track
// on the right; a click anywhere on the row toggles it.
func SwitchRow(c *ui.Context, on *bool, label, help string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.SwitchBase(c, on).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Label(label).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			Text(c, label, t.Text.Sm).FontWeight(500)
			if help != "" {
				Text(c, help, t.Text.Xs).TextColor(t.Muted)
			}
		})
		switchTrack(c, *on)
	})
}

// Checkbox is a 16×16 check box before its label.
func Checkbox(c *ui.Context, on *bool, label string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	el := ui.CheckboxBase(c, on)
	if label != "" {
		el.Gap(th.Space(2))
	}
	return el.Children(func() {
		box := ui.Box(c).Size(theme.Control, theme.Control).Shrink(0).Radius(t.Radius.Sm).Border(theme.Stroke, t.Input).Background(t.Background).Center().Transition(colors(theme.MotionFast))
		Elevate(c, box, theme.ShadowXs)
		if *on {
			box.Background(t.Primary).Border(theme.Stroke, t.Primary).TextColor(t.PrimaryText).Children(func() {
				ui.Icon(c, icons.Get("check")).Size(theme.IconXs, theme.IconXs)
			})
		}
		if label != "" {
			ui.Text(c, label)
		}
	})
}

// Radio is a radio button choosing v into *sel.
func Radio[T comparable](c *ui.Context, sel *T, v T, label string) ui.Element {
	th := c.Theme()
	return ui.RadioBase(c, sel, v).Gap(th.Space(2)).Children(func() {
		radioDot(c, *sel == v)
		ui.Text(c, label)
	})
}

// ChoiceOpts say how a Choice looks while it is not the one chosen: Dashed
// in a dashed outline, as a model on offer but not added; Card on the card
// gray with no outline, as a tile among others on a card.
type ChoiceOpts struct{ Dashed, Card bool }

// Choice makes e one of several the user picks among and returns it, drawn
// the one way a choice is: chosen, the hover gray in a primary outline;
// else the background in the border's (or as o says), half way to the hover
// gray under the pointer. e is the caller's control, a RadioBase or a
// Pressable: its role, state and corner are its own.
func Choice(c *ui.Context, e ui.Element, on bool, o ChoiceOpts) ui.Element {
	t := theme.Of(c)
	fill, edge := t.Background, t.Border
	if o.Card {
		fill, edge = t.Card, t.Border.Alpha(0)
	}
	switch {
	case on:
		fill, edge = tint(t, 1), t.Primary
	case e.Hovered():
		fill = t.Hover.Mix(fill, 0.5)
	}
	e.Background(fill).Border(theme.Stroke, edge).Transition(colors(theme.MotionFast))
	if o.Dashed && !on {
		e.Border(theme.Stroke, t.DividerHover).BorderStyle(ui.BorderDashed)
	}
	return e
}

// ToggleChipFrame is a chip that is on or off without its content: 28 high
// in xs text, muted while off. Give it its Label, Children and OnClick.
func ToggleChipFrame(c *ui.Context, on bool, o ChoiceOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Height(th.Space(7)).PaddingX(th.Space(2.5)).Gap(th.Space(1.5)).Radius(controlRadius(c, th.Space(7))).FontSize(t.Text.Xs).TextColor(t.Muted)
	if on {
		b.TextColor(t.Foreground)
	}
	return Choice(c, b, on, o)
}

// ToggleChip is a ToggleChipFrame showing label: a filter that is on or
// off, a model added or not.
func ToggleChip(c *ui.Context, label string, on bool, o ChoiceOpts) ui.Element {
	return ToggleChipFrame(c, on, o).Label(label).Children(func() { ui.Text(c, label) })
}

// RadioCard is a radio whose whole card clicks: the dot, label and help
// under it, drawn as a Choice on a card.
func RadioCard[T comparable](c *ui.Context, sel *T, v T, label, help string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	on := *sel == v
	card := ui.RadioBase(c, sel, v).FillWidth().AlignItems(ui.Start).Gap(th.Space(2)).Padding(th.Space(2)).
		Radius(t.Radius.Lg).Label(label)
	return Choice(c, card, on, ChoiceOpts{Card: true}).Children(func() {
		radioDot(c, on)
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, label)
			Text(c, help, t.Text.Xs).TextColor(t.Muted)
		})
	})
}

func radioDot(c *ui.Context, on bool) {
	t := theme.Of(c)
	dot := ui.Box(c).Size(theme.Control, theme.Control).Radius(t.Radius.Full).Border(theme.Stroke, t.Input).Background(t.Background).Center().Shrink(0).Transition(colors(theme.MotionFast))
	Elevate(c, dot, theme.ShadowXs)
	if on {
		dot.Border(theme.Stroke, t.Primary).Children(func() { ui.Box(c).Size(theme.RadioDot, theme.RadioDot).Radius(t.Radius.Full).Background(t.Primary) })
	}
}

// Option is one choice of a Select.
type Option[T comparable] struct {
	Value    T
	Label    string
	Disabled bool // listed, muted, and not chosen
}

// SelectOpts style a SelectWith: Size is its height as an Input's, zero
// MinWidth the default, zero MaxWidth unbounded; Flat drops the shadow.
type SelectOpts struct {
	Size               Size
	MinWidth, MaxWidth float32
	Flat               bool
}

// selectFace makes e a select's face showing current: the input frame with
// the text and a chevron.
func selectFace(c *ui.Context, e ui.Element, current string, o SelectOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	inputFrame(c, e, InputOpts{Flat: o.Flat, Size: o.Size})
	h, padX := inputSize(c, o.Size)
	e.Height(h).Padding(0, padX-th.Space(0.5), 0, padX).Gap(th.Space(1.5)).MinWidth(cmp.Or(o.MinWidth, theme.SelectMinWidth)).Children(func() { // the chevron's side is half a step tighter
		ui.Text(c, current).SingleLine().Grow(1).Shrink(1)
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
	})
	if o.MaxWidth > 0 {
		e.MaxWidth(o.MaxWidth)
	}
	return e
}

// SelectButton is a button with a select's face showing text, for a choice
// a Select cannot hold, as several at once: it opens the caller's popover.
// Give it a Label and an OnClick.
func SelectButton(c *ui.Context, text string, o SelectOpts) ui.Element {
	return selectFace(c, ui.ButtonBase(c).Justify(ui.Start), text, o)
}

// Select is a 32-high select of opts; give it a Label.
func Select[T comparable](c *ui.Context, sel *T, opts []Option[T]) ui.Element {
	return SelectWith(c, SelectOpts{}, sel, opts)
}

// SelectWith is Select styled by o.
func SelectWith[T comparable](c *ui.Context, o SelectOpts, sel *T, opts []Option[T]) ui.Element {
	t, th := theme.Of(c), c.Theme()
	s := ui.SelectBase(c, sel)
	current := ""
	for _, o := range opts {
		if o.Value == *sel {
			current = o.Label
		}
	}
	selectFace(c, s.Trigger, current, o)
	s.Popup(func(panel ui.Element) {
		panel.Margin(theme.PopupOffset, 0, 0, 0).Padding(th.Space(1)).
			Radius(t.Radius.Lg).Clip().
			Material(LiquidGlass(t.GlassBg)).
			Border(theme.Hairline, t.GlassBorder)
		Elevate(c, panel, theme.ShadowMd)
		for _, o := range opts {
			item := s.Item(o.Value).Height(th.Space(7)).PaddingX(th.Space(1.5)).Gap(th.Space(1.5)).Radius(t.Radius.Md) // as a MenuItem
			if o.Disabled {
				item.Disabled(true).TextColor(t.Muted)
			}
			if item.Highlighted() { // on the popup's glass, as a MenuItem
				item.Background(t.HoverTint)
			}
			item.Children(func() {
				ui.Text(c, o.Label).Grow(1)
				check := ui.Icon(c, icons.Get("check")).Size(theme.Icon, theme.Icon)
				if o.Value != *sel {
					check.Opacity(0)
				}
			})
		}
	})
	return s.Trigger
}

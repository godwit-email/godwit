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

// ChoiceLook is how a ChoiceCard looks while it is not the one chosen:
// ChoiceDefault the background in the border's outline; ChoiceOnCard the card
// gray with no outline, as a tile among others on a card; ChoiceDashed in a
// dashed outline, as a model on offer but not added.
type ChoiceLook uint8

const (
	ChoiceDefault ChoiceLook = iota
	ChoiceOnCard
	ChoiceDashed
)

// ChoiceCardOpts say how a ChoiceCard looks. Radio draws a radio dot before
// the content, filled while Selected.
type ChoiceCardOpts struct {
	Selected bool
	Look     ChoiceLook
	Radio    bool
}

// choiceFace draws e the one way a choice is: chosen, the hover gray in a
// primary outline; else the background in the border's (or as look says),
// half way to the hover gray under the pointer.
func choiceFace(c *ui.Context, e ui.Element, on bool, look ChoiceLook) ui.Element {
	t := theme.Of(c)
	fill, edge := t.Background, t.Border
	if look == ChoiceOnCard {
		fill, edge = t.Card, t.Border.Alpha(0)
	}
	switch {
	case on:
		fill, edge = tint(t, 1), t.Primary
	case e.Hovered():
		fill = t.Hover.Mix(fill, 0.5)
	}
	e.Background(fill).Border(theme.Stroke, edge).Transition(colors(theme.MotionFast))
	if look == ChoiceDashed && !on {
		e.Border(theme.Stroke, t.DividerHover).BorderStyle(ui.BorderDashed)
	}
	return e
}

// ChoiceCard makes e a card one of several is picked from, or a tile that
// acts on a click (selected is then false), around content. e is the
// caller's control, a RadioBase or a Pressable{Bare}: its role, state and
// corner are its own, and so are the padding and gap of a card that has no
// radio dot (the radio card is full width, 8 padded, 8 apart, in corners of
// Lg).
func ChoiceCard(c *ui.Context, e ui.Element, o ChoiceCardOpts, content func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	choiceFace(c, e, o.Selected, o.Look)
	if o.Radio {
		e.FillWidth().AlignItems(ui.Start).Gap(th.Space(2)).Padding(th.Space(2)).Radius(t.Radius.Lg)
	}
	return e.Children(func() {
		if o.Radio {
			radioDot(c, o.Selected)
		}
		content()
	})
}

// ChoiceText is a ChoiceCard's usual content: label, and help under it.
func ChoiceText(c *ui.Context, label, help string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
		ui.Text(c, label)
		Text(c, help, t.Text.Xs).TextColor(t.Muted)
	})
}

// ToggleChipFrame is a chip that is on or off without its content: 28 high
// in xs text, muted while off. Give it its Label, Children and OnClick.
func ToggleChipFrame(c *ui.Context, on bool, look ChoiceLook) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Height(th.Space(7)).PaddingX(th.Space(2.5)).Gap(th.Space(1.5)).Radius(controlRadius(c, th.Space(7))).FontSize(t.Text.Xs).TextColor(t.Muted)
	if on {
		b.TextColor(t.Foreground)
	}
	return choiceFace(c, b, on, look)
}

// ToggleChip is a ToggleChipFrame showing label: a filter that is on or
// off, a model added or not.
func ToggleChip(c *ui.Context, label string, on bool, look ChoiceLook) ui.Element {
	return ToggleChipFrame(c, on, look).Label(label).Children(func() { ui.Text(c, label) })
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

// SelectWith is Select styled by o. Its options open as the system's menu
// under the trigger, the current one checked; Changed reports a new choice.
func SelectWith[T comparable](c *ui.Context, o SelectOpts, sel *T, opts []Option[T]) ui.Element {
	current := ""
	for _, opt := range opts {
		if opt.Value == *sel {
			current = opt.Label
		}
	}
	trigger := selectFace(c, ui.ButtonBase(c).Justify(ui.Start), current, o)
	// a pop-up button whose value is the choice, as ui.SelectBase is: Menu keeps a role set before it
	trigger.Role(ui.RolePopUpButton).Value(current)
	trigger.Menu(func(m *ui.Menu) {
		for _, opt := range opts {
			if m.Item(opt.Label).Checked(opt.Value == *sel).Disabled(opt.Disabled).Chosen() && opt.Value != *sel {
				*sel = opt.Value
				trigger.SetChanged()
			}
		}
	})
	return trigger
}

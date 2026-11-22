package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// InputOpts style a text input. Size is its height on the control scale:
// SizeSm 28, the default 32 and SizeLg 36 (any other is the default), with
// a button's corner and 8, 10 and 12 at its sides. Leading and Trailing are
// slots before and after the text, drawn inside the frame: a search icon, a
// clear IconButton, a Kbd.
type InputOpts struct {
	Placeholder             string
	Size                    Size
	Password, Mono, Invalid bool
	Flat                    bool     // no shadow
	Bare                    bool     // unframed text field (no border, fill, shadow or ring), for a floating composer
	Glass                   bool     // frosted glass appearance with hairline border (no blur)
	Tint                    bool     // the hover tint for the fill, as on the window material (not with Glass)
	Autocomplete            bool     // an Autocomplete, framed as a slotted input: Suggest are its options, none at times
	Suggest                 []string // what an Autocomplete offers as the user types
	Leading, Trailing       func()
	Box                     *ui.Element // set to the frame an input with slots is drawn in
}

// inputSize is the height and side padding of an input of size s.
func inputSize(c *ui.Context, s Size) (h, padX float32) {
	th := c.Theme()
	switch s {
	case SizeSm:
		return th.Space(7), th.Space(2)
	case SizeLg:
		return th.Space(9), th.Space(3)
	}
	return th.Space(8), th.Space(2.5)
}

func inputFrame(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t := theme.Of(c)
	borderWidth := float32(theme.Stroke)
	h, _ := inputSize(c, o.Size)
	e.Radius(controlRadius(c, h))
	if o.Glass {
		borderWidth = theme.Hairline
		e.Clip().Background(t.GlassBg)
		Elevate(c, e, theme.ShadowXs)
	} else {
		e.Border(theme.Stroke, t.Input).Background(t.Background)
		if o.Tint {
			e.Background(t.HoverTint)
		}
		if !o.Flat {
			Elevate(c, e, theme.ShadowXs)
		}
	}
	e.FocusRing(false)
	border, ring := t.Input, c.Theme().Focus.Alpha(0)
	if o.Glass {
		border = t.GlassBorder
	}
	if e.FocusWithin() && !e.IsDisabled() {
		border, ring = t.Ring, c.Theme().Focus
	}
	if o.Invalid {
		border = t.Danger
	}
	a := ui.Animation{Duration: theme.MotionFast, Ease: ui.EaseOut}
	e.Border(borderWidth, e.AnimateColor("input-border", border, a)).
		Shadow(0, 0, 0, theme.RingWidth, e.AnimateColor("input-ring", ring, a))
	return e
}

// InputIcon is the icon an Input's Leading slot shows: 16, muted.
func InputIcon(c *ui.Context, name string) ui.Element {
	return ui.Icon(c, icons.Get(name)).Size(theme.Icon, theme.Icon).TextColor(theme.Of(c).Muted)
}

func input(c *ui.Context, e ui.Element, o InputOpts, framed bool) ui.Element {
	t := theme.Of(c)
	e.Placeholder(o.Placeholder).FocusRing(false)
	if framed {
		_, padX := inputSize(c, o.Size)
		e.PaddingX(padX)
		inputFrame(c, e, o)
	}
	if o.Mono {
		e.Font(theme.Mono).FontSize(t.Text.Xs)
	}
	if o.Password {
		e.Password()
	}
	return e
}

// InputButton is a press target that looks as an Input of o's size, for a
// field that opens something else (a palette) instead of taking text: it
// shows what the caller puts in it, left-aligned, as a placeholder and the
// keys that open it. Give it its Label, Gap and Children.
func InputButton(c *ui.Context, o InputOpts, onClick func()) ui.Element {
	h, padX := inputSize(c, o.Size)
	b := ui.ButtonBase(c).Height(h).PaddingX(padX).Justify(ui.Start).AlignItems(ui.Center).OnClick(onClick)
	return inputFrame(c, b, o)
}

// Input is a single-line text input, 32 high unless o.Size says otherwise.
// With a Leading or Trailing slot it is the text field in a frame of the same
// height, and what it returns is the text field: its gap is 8, its text is
// cut where the right padding starts (4 when there is a Trailing slot).
func Input(c *ui.Context, s *string, o InputOpts) ui.Element {
	h, padX := inputSize(c, o.Size)
	field := func() ui.Element {
		if o.Autocomplete {
			return ui.Autocomplete(c, s, o.Suggest)
		}
		return ui.TextInputBase(c, s)
	}
	if o.Bare {
		return input(c, field().Height(h).Padding(0), o, false)
	}
	if o.Leading == nil && o.Trailing == nil && !o.Autocomplete {
		return input(c, field().Height(h), o, true)
	}
	th := c.Theme()
	right := padX
	if o.Trailing != nil {
		right = th.Space(1)
	}
	box := inputFrame(c, ui.Row(c).Height(h).AlignItems(ui.Center).Gap(th.Space(2)).Padding(0, right, 0, padX), o)
	var in ui.Element
	box.Children(func() {
		if o.Leading != nil {
			o.Leading()
		}
		in = input(c, field().Grow(1).Shrink(1).MinWidth(0).AlignSelf(ui.Stretch).Padding(0), o, false)
		if o.Autocomplete {
			in.Border(0, theme.Of(c).Input).Background(ui.Transparent)
		}
		if o.Trailing != nil {
			o.Trailing()
		}
	})
	if o.Box != nil {
		*o.Box = box
	}
	return in
}

// TextArea is a multi-line text input.
func TextArea(c *ui.Context, s *string, o InputOpts) ui.Element {
	e := ui.TextAreaBase(c, s)
	e.PaddingY(c.Theme().Space(2))
	return input(c, e, o, true)
}

// FieldOpts style a FieldWith. Horizontal with Switch makes it a switch row:
// label with help or error on the left, a switch on *Switch at the right
// (the row takes no control), and the whole row its target: one Tab stop, a
// click anywhere toggles it, the focus ring runs round the row. Disabled
// disables it and dims the switch only, the label and help staying readable
// as the design's. Horizontal without Switch does nothing: LabelledRow builds
// the other settings rows.
type FieldOpts struct {
	Horizontal, Disabled bool
	Switch               *bool
}

// Field is a labelled form control with help text, or an error in its place.
func Field(c *ui.Context, label, help, errText string, control func()) ui.Element {
	return FieldWith(c, FieldOpts{}, label, help, errText, control)
}

// FieldWith is Field laid out as o says: a column, or with a Switch a row.
func FieldWith(c *ui.Context, o FieldOpts, label, help, errText string, control func()) ui.Element {
	th := c.Theme()
	message := fieldMessage(c, help, errText)
	if !o.Horizontal || o.Switch == nil {
		return ui.Column(c).Gap(th.Space(1.5)).Children(func() {
			ui.Text(c, label).FontWeight(500)
			control()
			message()
		})
	}
	return horizontalField(c, o, label, message, nil)
}

// fieldMessage draws errText in the danger color, or else help in the muted
// color: what sits under a field's label.
func fieldMessage(c *ui.Context, help, errText string) func() {
	t := theme.Of(c)
	return func() {
		switch {
		case errText != "":
			Text(c, errText, t.Text.Xs).TextColor(t.Danger)
		case help != "":
			Text(c, help, t.Text.Xs).TextColor(t.Muted)
		}
	}
}

// LabelledRow is a settings row: label with help (or errText in its place)
// on the left, and at the right the element control returns, which a click
// on the label focuses, as a Field's label does. control returns ui.Element{}
// for a row that ends in a button, a link or plain text, which keeps its own
// label.
func LabelledRow(c *ui.Context, label, help, errText string, control func() ui.Element) ui.Element {
	return horizontalField(c, FieldOpts{Horizontal: true}, label, fieldMessage(c, help, errText), control)
}

func horizontalField(c *ui.Context, o FieldOpts, label string, message func(), control func() ui.Element) ui.Element {
	t, th := theme.Of(c), c.Theme()
	row := ui.Row(c)
	if o.Switch != nil {
		row = ui.SwitchBase(c, o.Switch).Label(label).Disabled(o.Disabled).DimDisabled(false)
	}
	var name, ctrl ui.Element
	row = row.FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			name = Text(c, label, t.Text.Sm)
			message()
		})
		if o.Switch != nil {
			ui.Box(c).Shrink(0).Disabled(o.Disabled).Children(func() { switchTrack(c, *o.Switch) })
			return
		}
		ctrl = control()
	})
	ctrl.LabelledBy(name)
	return row
}

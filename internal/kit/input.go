package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// InputOpts style a text input. Size is its height on the control scale:
// SizeSm 28, the default 32 and SizeLg 36 (any other is the default), with
// a button's corner and 8, 10 and 12 at its sides.
type InputOpts struct {
	Placeholder             string
	Size                    Size
	Password, Mono, Invalid bool
	Bare                    bool // raw Base geometry: the row around it draws the box
	Flat                    bool // no shadow
	Glass                   bool // frosted glass appearance with hairline border (no blur)
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

// InputGroup is an input frame for a bare input and its icons or actions, as
// high as an Input of o.Size. It shares Input's border, shadow, error and
// focus styles; o uses Size, Invalid, Flat and Glass. The bare input in it
// stretches to its height (AlignSelf(ui.Stretch)); the group pads itself.
func InputGroup(c *ui.Context, o InputOpts) ui.Element {
	h, _ := inputSize(c, o.Size)
	return inputFrame(c, ui.Row(c).Height(h).AlignItems(ui.Center), o)
}

func input(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t := theme.Of(c)
	e.Placeholder(o.Placeholder).FocusRing(false)
	if !o.Bare {
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

// Input is a single-line text input, 32 high unless o.Size says otherwise.
func Input(c *ui.Context, s *string, o InputOpts) ui.Element {
	e := ui.TextInputBase(c, s)
	if !o.Bare {
		h, _ := inputSize(c, o.Size)
		e.Height(h)
	}
	return input(c, e, o)
}

// TextArea is a multi-line text input.
func TextArea(c *ui.Context, s *string, o InputOpts) ui.Element {
	e := ui.TextAreaBase(c, s)
	if !o.Bare {
		e.PaddingY(c.Theme().Space(2))
	}
	return input(c, e, o)
}

// Field is a labelled form control with help text, or an error in its place.
func Field(c *ui.Context, label, help, errText string, control func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		ui.Text(c, label).FontWeight(500)
		control()
		switch {
		case errText != "":
			Text(c, errText, t.Text.Xs).TextColor(t.Danger)
		case help != "":
			Text(c, help, t.Text.Xs).TextColor(t.Muted)
		}
	})
}

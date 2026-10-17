package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// InputOpts style a text input.
type InputOpts struct {
	Placeholder             string
	Password, Mono, Invalid bool
	Bare                    bool // raw Base geometry: the row around it draws the box
	Flat                    bool // no shadow
}

func inputFrame(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t := theme.Of(c)
	e.Radius(t.Radius.Md).Border(theme.Stroke, t.Input).Background(t.Background)
	if !o.Flat {
		Elevate(c, e, theme.ShadowXs)
	}
	e.FocusRing(false)
	border, ring := t.Input, c.Theme().Focus.Alpha(0)
	if e.FocusWithin() && !e.IsDisabled() {
		border, ring = t.Ring, c.Theme().Focus
	}
	if o.Invalid {
		border = t.Danger
	}
	a := ui.Animation{Duration: theme.MotionFast, Ease: ui.EaseOut}
	e.Border(theme.Stroke, e.AnimateColor("input-border", border, a)).
		Shadow(0, 0, 0, theme.RingWidth, e.AnimateColor("input-ring", ring, a))
	return e
}

// InputGroup is a 32-high input frame for a bare input and its icons or actions.
// It shares Input's border, shadow, error and focus styles; o uses Invalid and Flat.
func InputGroup(c *ui.Context, o InputOpts) ui.Element {
	return inputFrame(c, ui.Row(c).Height(c.Theme().Space(8)).AlignItems(ui.Center), o)
}

func input(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	e.Placeholder(o.Placeholder).FocusRing(false)
	if !o.Bare {
		e.PaddingX(th.Space(2.5))
		inputFrame(c, e, o)
	}
	if o.Mono {
		e.Font(theme.Mono).FontSize(t.Text.Sm)
	}
	if o.Password {
		e.Password()
	}
	return e
}

// Input is a 32-high single-line text input.
func Input(c *ui.Context, s *string, o InputOpts) ui.Element {
	e := ui.TextInputBase(c, s)
	if !o.Bare {
		e.Height(c.Theme().Space(8))
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
			Text(c, errText, t.Text.Sm).TextColor(t.Danger)
		case help != "":
			Text(c, help, t.Text.Sm).TextColor(t.Muted)
		}
	})
}

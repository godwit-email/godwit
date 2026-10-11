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
}

func input(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	e.Placeholder(o.Placeholder)
	if !o.Bare {
		e.PaddingX(th.Space(2.5)).Radius(t.Radius.Md).Border(theme.Hairline, t.Input).Background(t.Background)
		Elevate(c, e, theme.ShadowXs)
	}
	if o.Mono {
		e.Font("Geist Mono").FontSize(t.Text.Base)
	}
	if o.Password {
		e.Password()
	}
	if o.Invalid && !o.Bare {
		e.Border(theme.Hairline, t.Danger)
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
			ui.Text(c, errText).FontSize(t.Text.Sm).TextColor(t.Danger)
		case help != "":
			ui.Text(c, help).FontSize(t.Text.Sm).TextColor(t.Muted)
		}
	})
}

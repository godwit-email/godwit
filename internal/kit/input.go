package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// InputOpts style a text input.
type InputOpts struct {
	Placeholder             string
	Password, Mono, Invalid bool
}

func input(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	e.PaddingX(th.Space(2.5)).Radius(t.Radius.Sm).Border(theme.Hairline, t.Border).Background(t.Background).Placeholder(o.Placeholder)
	if o.Mono {
		e.Font("Geist Mono").FontSize(t.Text.Base)
	}
	if o.Password {
		e.Password()
	}
	if o.Invalid {
		e.Border(theme.Hairline, t.Danger)
	}
	return e
}

// Input is a 32-high single-line text input.
func Input(c *ui.Context, s *string, o InputOpts) ui.Element {
	return input(c, ui.TextInputBase(c, s).Height(c.Theme().Space(8)), o)
}

// TextArea is a multi-line text input.
func TextArea(c *ui.Context, s *string, o InputOpts) ui.Element {
	return input(c, ui.TextAreaBase(c, s).PaddingY(c.Theme().Space(2)), o)
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

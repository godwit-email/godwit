package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// Collapsible is a muted, small trigger row with a turning chevron, and panel
// below it while *open. *open must outlive the frame. It returns the trigger.
func Collapsible(c *ui.Context, open *bool, trigger, panel func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	parts := ui.CollapsibleBase(c, open)
	tr := parts.Trigger.MinHeight(th.Space(7)).PaddingX(th.Space(2)).Gap(th.Space(1.5)).Radius(controlRadius(c, th.Space(7))).FontSize(t.Text.Xs).TextColor(t.Muted).
		Justify(ui.Start).Transition(colors(theme.MotionFast))
	if tr.Hovered() {
		tr.Background(tint(t, 1))
	}
	tr.Children(func() {
		Chevron(c, *open, theme.IconSm)
		trigger()
	})
	parts.Panel(panel)
	return tr
}

// Chevron is the arrow of what opens: muted, a quarter turn clockwise while
// open, over MotionBase. size is theme.IconXs for a sidebar's rows and
// theme.IconSm for a Collapsible's.
func Chevron(c *ui.Context, open bool, size float32) ui.Element {
	e := ui.Icon(c, icons.Get("chevron-right")).Size(size, size).Shrink(0).TextColor(theme.Of(c).Muted)
	turn := float32(0)
	if open {
		turn = 1
	}
	return e.Rotate(90 * e.AnimateTo("turn", turn, ui.Animation{Duration: theme.MotionBase}))
}

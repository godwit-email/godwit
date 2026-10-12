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
	tr := parts.Trigger.MinHeight(th.Space(7.5)).PaddingX(th.Space(2)).Gap(th.Space(1.5)).Radius(t.Radius.Lg).FontSize(t.Text.Sm).TextColor(t.Muted).
		Justify(ui.Start).Transition(colors(theme.MotionFast))
	if tr.Hovered() {
		tr.Background(t.Hover)
	}
	tr.Children(func() {
		ui.Icon(c, icons.Get("chevron-right")).Size(theme.IconSm, theme.IconSm).Rotate(90 * parts.Progress())
		trigger()
	})
	parts.Panel(panel)
	return tr
}

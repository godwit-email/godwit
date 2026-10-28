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

// SidebarCollapsible is a sidebar row (SidebarItem's size and padding) that
// folds the rows below it: trigger's children, then a 12 chevron at its
// right, turned a quarter while open. Muted is a group's title: small muted
// text. *open must outlive the frame. It returns the trigger.
func SidebarCollapsible(c *ui.Context, open *bool, muted bool, trigger, panel func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	parts := ui.CollapsibleBase(c, open)
	tr := parts.Trigger.Height(th.Space(8)).PaddingX(th.Space(2)).Gap(th.Space(2)).Radius(t.Radius.Lg).
		Justify(ui.Start).Transition(colors(theme.MotionFast))
	if muted {
		tr.Gap(th.Space(1)).FontSize(t.Text.Sm).TextColor(t.Muted)
	}
	if tr.Hovered() {
		tr.Background(t.HoverTint)
	}
	tr.Children(func() {
		trigger()
		SidebarChevron(c, parts.Progress())
	})
	parts.Panel(func() { // the sidebar's own gap, above and between the rows
		ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(0.5), 0, 0, 0).Children(panel)
	})
	return tr
}

// SidebarChevron is a sidebar row's 12 muted chevron, turned a quarter at open 1.
func SidebarChevron(c *ui.Context, open float32) ui.Element {
	return ui.Icon(c, icons.Get("chevron-right")).Size(theme.IconXs, theme.IconXs).Shrink(0).TextColor(theme.Of(c).Muted).Rotate(90 * open)
}

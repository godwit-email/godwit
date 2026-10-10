package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// ToastHost draws the window's toasts (c.Toast, c.AddToast) at the bottom
// right; call it once in the view. closeLabel names the close buttons (pass
// the translated "Close"). Type "error" shows the error icon.
func ToastHost(c *ui.Context, closeLabel string) {
	t, th := theme.Of(c), c.Theme()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(th.Space(4)).AlignItems(ui.End).Justify(ui.End).Gap(th.Space(2))
		for _, tst := range toasts {
			toast := ui.ToastBase(c, tst)
			toast.Root.Row().Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3.5)).Radius(t.Radius.Md).Background(t.Background).Border(theme.Hairline, t.Border).
				Shadow(0, theme.ShadowMd.Y, theme.ShadowMd.Blur, 0, ink(t).Alpha(0.12)).MaxWidth(theme.ToastMaxWidth).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 8}, Exit: &ui.Motion{}})
			toast.Root.Children(func() {
				if tst.Type == "error" {
					ui.Icon(c, icons.Get("circle-x")).Size(theme.Icon, theme.Icon).TextColor(t.Danger)
				}
				ui.Column(c).Grow(1).Gap(th.Space(0.5)).Children(func() {
					ui.Text(c, tst.Title).FontWeight(500)
					if tst.Description != "" {
						ui.Text(c, tst.Description).FontSize(t.Text.Sm).TextColor(t.Muted)
					}
				})
				if tst.Action != "" {
					toast.ActionButton().Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Sm).Border(theme.Hairline, t.Border).Children(func() {
						ui.Text(c, tst.Action)
					})
				}
				toast.CloseButton().Label(closeLabel).Size(theme.CloseButton, theme.CloseButton).Radius(t.Radius.Xs).Center().TextColor(t.Muted).Children(func() {
					ui.Icon(c, icons.Get("x")).Size(theme.IconSm, theme.IconSm)
				})
			})
		}
	})
}

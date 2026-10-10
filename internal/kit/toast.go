package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// The kinds of toast, for ui.Toast's Type; "" is a plain one.
const (
	ToastSuccess = "success"
	ToastWarning = "warning"
	ToastError   = "error"
)

// ToastHost draws the window's toasts (c.Toast, c.AddToast) at the top
// center, the newest first; call it once in the view. closeLabel names the
// close buttons (pass the translated "Close"). A success, warning or error
// toast leads with its icon in its color; a plain one has none.
func ToastHost(c *ui.Context, closeLabel string) {
	t, th := theme.Of(c), c.Theme()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(th.Space(4)).AlignItems(ui.Center).Justify(ui.Start).Gap(th.Space(2))
		for i := range toasts {
			tst := toasts[len(toasts)-1-i]
			toast := ui.ToastBase(c, tst)
			toast.Root.Row().Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3.5)).Radius(t.Radius.Md).Background(t.Background).Border(theme.Hairline, t.Border).
				Shadow(0, theme.ShadowMd.Y, theme.ShadowMd.Blur, 0, ink(t).Alpha(0.12)).MaxWidth(theme.ToastMaxWidth).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: -2 * theme.MotionOffset}, Exit: &ui.Motion{Y: -2 * theme.MotionOffset}})
			toast.Root.Children(func() {
				if icon, color, ok := toastIcon(t, tst.Type); ok {
					ui.Icon(c, icons.Get(icon)).Size(theme.Icon, theme.Icon).TextColor(color)
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

// toastIcon is the icon and color a toast of typ leads with, if any.
func toastIcon(t theme.Tokens, typ string) (string, ui.Color, bool) {
	switch typ {
	case ToastSuccess:
		return "circle-check", t.Success, true
	case ToastWarning:
		return "triangle-alert", t.Attention, true
	case ToastError:
		return "circle-x", t.Danger, true
	}
	return "", ui.Transparent, false
}

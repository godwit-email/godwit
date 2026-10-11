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

// ToastHost draws the window's toasts (c.Toast, c.AddToast) centered under
// the top bar, the newest first and at most theme.ToastMax; call it once in
// the view. closeLabel names the close buttons (pass the translated
// "Close"). A success, warning or error toast is tinted: its color's 50 behind, 200 around, 700 for the
// text and its leading icon; a plain one is white, without an icon.
func ToastHost(c *ui.Context, closeLabel string) {
	t, th := theme.Of(c), c.Theme()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(theme.TopBarHeight+th.Space(2), th.Space(4), th.Space(4), th.Space(4)).AlignItems(ui.Center).Justify(ui.Start).Gap(th.Space(2))
		for len(toasts) > theme.ToastMax { // the oldest makes room
			c.CloseToast(toasts[0].ID)
			toasts = toasts[1:]
		}
		for i := range toasts {
			tst := toasts[len(toasts)-1-i]
			toast := ui.ToastBase(c, tst)
			look := toastLook(t, tst.Type)
			toast.Root.Row().Gap(th.Space(2.5)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.X2l).Background(look.bg).Border(theme.Hairline, look.border).
				TextColor(look.fg).MaxWidth(theme.ToastMaxWidth).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: -2 * theme.MotionOffset}, Exit: &ui.Motion{Y: -2 * theme.MotionOffset}})
			Elevate(c, toast.Root, theme.ShadowLg)
			toast.Root.Children(func() {
				if look.icon != "" {
					ui.Icon(c, icons.Get(look.icon)).Size(theme.Icon, theme.Icon)
				}
				ui.Column(c).Grow(1).Gap(th.Space(0.5)).Children(func() {
					ui.Text(c, tst.Title).FontWeight(500)
					if tst.Description != "" {
						Text(c, tst.Description, t.Text.Sm).TextColor(look.muted)
					}
				})
				if tst.Action != "" {
					toast.ActionButton().Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).Border(theme.Hairline, look.border).Children(func() {
						ui.Text(c, tst.Action)
					})
				}
				toast.CloseButton().Label(closeLabel).Size(theme.CloseButton, theme.CloseButton).Radius(t.Radius.Sm).Center().TextColor(look.muted).Children(func() {
					ui.Icon(c, icons.Get("x")).Size(theme.IconSm, theme.IconSm)
				})
			})
		}
	})
}

// toastStyle is how a toast of a kind looks: its leading icon ("" for
// none), its colors, and the color of its secondary text and close button.
type toastStyle struct {
	icon                  string
	bg, border, fg, muted ui.Color
}

// toastLook is the style of a toast of typ: tinted for success, warning and
// error, plain otherwise.
func toastLook(t theme.Tokens, typ string) toastStyle {
	switch typ {
	case ToastSuccess:
		return toastStyle{"circle-check", t.SuccessBg, t.SuccessBorder, t.SuccessText, t.SuccessText}
	case ToastWarning:
		return toastStyle{"triangle-alert", t.WarningBg, t.WarningBorder, t.WarningText, t.WarningText}
	case ToastError:
		return toastStyle{"circle-x", t.DangerBg, t.DangerBorder, t.DangerText, t.DangerText}
	}
	return toastStyle{"", t.Background, t.Border, t.Foreground, t.Muted}
}

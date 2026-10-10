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
// the top bar, the newest first; call it once in the view. closeLabel names
// the close buttons (pass the translated "Close"). A success, warning or
// error toast is tinted: its color's 50 behind, 200 around, 700 for the
// text and its leading icon; a plain one is white, without an icon.
func ToastHost(c *ui.Context, closeLabel string) {
	th := c.Theme()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(theme.TopBarHeight+th.Space(2), th.Space(4), th.Space(4), th.Space(4)).AlignItems(ui.Center).Justify(ui.Start).Gap(th.Space(2))
		for i := range toasts {
			tst := toasts[len(toasts)-1-i]
			toast := ui.ToastBase(c, tst)
			toast.Root.Transition(ui.ElementTransition{Enter: &ui.Motion{Y: -2 * theme.MotionOffset}, Exit: &ui.Motion{Y: -2 * theme.MotionOffset}})
			drawToast(c, toast.Root, tst, toast.ActionButton, func() ui.Element { return toast.CloseButton().Label(closeLabel) })
		}
	})
}

// drawToast styles root as a toast of tst's kind and builds its content;
// action and closer make its buttons (action only when tst has one).
func drawToast(c *ui.Context, root ui.Element, tst ui.Toast, action, closer func() ui.Element) {
	t, th := theme.Of(c), c.Theme()
	look := toastLook(t, tst.Type)
	root.Row().Gap(th.Space(2.5)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Xl).Background(look.bg).Border(theme.Hairline, look.border).
		TextColor(look.fg).Shadow(0, theme.ShadowMd.Y, theme.ShadowMd.Blur, 0, ink(t).Alpha(0.08)).MaxWidth(theme.ToastMaxWidth)
	root.Children(func() {
		if look.icon != "" {
			ui.Icon(c, icons.Get(look.icon)).Size(theme.Icon, theme.Icon)
		}
		ui.Column(c).Grow(1).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, tst.Title).FontWeight(500)
			if tst.Description != "" {
				ui.Text(c, tst.Description).FontSize(t.Text.Sm).TextColor(look.muted)
			}
		})
		if tst.Action != "" && action != nil {
			action().Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Sm).Border(theme.Hairline, look.border).Children(func() {
				ui.Text(c, tst.Action)
			})
		}
		closer().Size(theme.CloseButton, theme.CloseButton).Radius(t.Radius.Xs).Center().TextColor(look.muted).Children(func() {
			ui.Icon(c, icons.Get("x")).Size(theme.IconSm, theme.IconSm)
		})
	})
}

// toastPreview draws a toast of tst's kind in place, as the gallery shows
// it: the same look, nothing that closes.
func toastPreview(c *ui.Context, tst ui.Toast) ui.Element {
	root := ui.Row(c)
	drawToast(c, root, tst, nil, func() ui.Element { return ui.Box(c) })
	return root
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

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
// the top bar, the oldest first and at most theme.ToastMax; call it once in
// the view. A toast is a control high (36) with a menu's corners; its action
// is a small filled button at its end. One that goes by itself has no close
// button (Escape closes it); one that stays (Timeout < 0) has, named
// closeLabel (pass the translated "Close"). A success, warning or error toast
// is tinted: its color's 50 behind, 200 around and under its action, 700 for
// the text and its leading icon; a plain one is white, without an icon.
func ToastHost(c *ui.Context, closeLabel string) {
	t, th := theme.Of(c), c.Theme()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(theme.TopBarHeight+th.Space(2), th.Space(4), th.Space(4), th.Space(4)).AlignItems(ui.Center).Justify(ui.Start).Gap(th.Space(2))
		for len(toasts) > theme.ToastMax { // the oldest makes room
			c.CloseToast(toasts[0].ID)
			toasts = toasts[1:]
		}
		for _, tst := range toasts {
			toast := ui.ToastBase(c, tst)
			look := toastLook(t, tst.Type)
			// Space(1) around, so a 28 action ends Space(1) from the edge; the text makes it Space(3).
			toast.Root.Row().AlignItems(ui.Center).Gap(th.Space(1)).MinHeight(th.Space(9)).Padding(th.Space(1), th.Space(1), th.Space(1), th.Space(3)).
				Radius(t.Radius.Lg).Background(look.bg).Border(theme.Hairline, look.border).
				TextColor(look.fg).FontSize(t.Text.Sm).MaxWidth(theme.ToastMaxWidth).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: -2 * theme.MotionOffset}, Exit: &ui.Motion{Y: -2 * theme.MotionOffset}})
			Elevate(c, toast.Root, theme.ShadowLg)
			toast.Root.Children(func() {
				if look.icon != "" {
					ui.Icon(c, icons.Get(look.icon)).Size(theme.Icon, theme.Icon).Margin(0, th.Space(1), 0, 0)
				}
				ui.Column(c).Grow(1).Gap(th.Space(0.5)).Padding(th.Space(1), th.Space(2), th.Space(1), 0).Children(func() {
					title := ui.Text(c, tst.Title)
					if tst.Description != "" { // a heading over its line
						title.FontWeight(500)
						Text(c, tst.Description, t.Text.Xs).TextColor(look.muted)
					}
				})
				if tst.Action != "" {
					b := toast.ActionButton().Height(th.Space(7)).PaddingX(th.Space(2)).Radius(t.Radius.Md).FontWeight(500).Transition(colors(theme.MotionFast))
					fill := look.action
					switch { // as Button: a step on hover, another pressed
					case b.Pressed():
						fill = fill.Mix(look.fg, 0.12)
					case b.Hovered():
						fill = fill.Mix(look.fg, 0.06)
					}
					b.Background(fill).Children(func() { ui.Text(c, tst.Action) })
				}
				if tst.Timeout < 0 {
					toast.CloseButton().Label(closeLabel).Size(th.Space(7), th.Space(7)).Radius(t.Radius.Md).Center().TextColor(look.muted).Children(func() {
						ui.Icon(c, icons.Get("x")).Size(theme.Icon, theme.Icon)
					})
				}
			})
		}
	})
}

// toastStyle is how a toast of a kind looks: its leading icon ("" for
// none), its colors, and the color of its secondary text and close button.
type toastStyle struct {
	icon                          string
	bg, border, fg, muted, action ui.Color // action is the fill under its action button
}

// toastLook is the style of a toast of typ: tinted for success, warning and
// error, plain otherwise.
func toastLook(t theme.Tokens, typ string) toastStyle {
	switch typ {
	case ToastSuccess:
		return toastStyle{"circle-check", t.SuccessBg, t.SuccessBorder, t.SuccessText, t.SuccessText, t.SuccessBorder}
	case ToastWarning:
		return toastStyle{"triangle-alert", t.WarningBg, t.WarningBorder, t.WarningText, t.WarningText, t.WarningBorder}
	case ToastError:
		return toastStyle{"circle-x", t.DangerBg, t.DangerBorder, t.DangerText, t.DangerText, t.DangerBorder}
	}
	return toastStyle{"", t.Raised, t.Border, t.Foreground, t.Muted, t.Hover}
}

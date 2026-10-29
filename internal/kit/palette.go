package kit

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// PaletteOpts name a Palette and its search box. OnKey sees the keys the
// box types no text with (the arrows, Enter, Tab, those with Cmd or Ctrl)
// before it does, as ui's OnTextKey: true takes the key. Chips, when set,
// builds the PaletteChips before the box. Keys, when set, holds the box:
// it types no text, and Keys sees every key pressed first (a menu's
// letters). Send, when set, makes the box a message to send instead of a
// search: a framed text area and a button named Send, and no head or body.
// Its panel is a dialog's (Glass).
type PaletteOpts struct {
	Label, Placeholder, Close string
	OnKey                     func(k ui.TextKey) bool
	Keys                      func(mods ui.Modifiers, key ui.Key) bool
	Send                      string
	OnSend                    func()
	Hints                     []PaletteHint
	Chips                     func()
}

// PaletteHint is a shortcut the palette's bottom bar names: Key, then Label.
// With OnClick it is a button too, at the bar's end: give it last.
type PaletteHint struct {
	Label, Key string
	OnClick    func()
}

// paletteSel is the selected PaletteRow built this frame, which the search
// box names to assistive technology. UI thread only.
var paletteSel ui.Element

// paletteHeld is the key Keys took, which holds the box until it is up: its
// text comes after the frame the key's item may have closed Keys in. UI
// thread only.
var paletteHeld ui.Key

// Palette is a modal search panel near the top of the window: a search box
// bound to query with a close button, head (the kind tabs) under it, body
// (the result list, of PaletteRows) scrolling under that, and a bar of
// o.Hints. The box has the focus; Escape, a click outside and the close
// button close it.
func Palette(c *ui.Context, open *bool, query *string, o PaletteOpts, head, body func()) {
	t, th := theme.Of(c), c.Theme()
	if !*open {
		paletteHeld = 0 // its key may come up elsewhere (an item that closed the palette): no box to hold
	}
	k := enter(c, open, theme.MotionBase)
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogShown = true
		// a dialog's scrim, but fixed near the top: as hits come and go the panel only grows down
		backdrop.Background(t.Scrim.Alpha(k)).Justify(ui.Start).Padding(th.Space(16), 0, 0, 0)
		panel.Label(o.Label).Opacity(k).Margin(rise(k), 0, 0, 0).Width(theme.DialogWideWidth).MaxHeight(theme.SheetMaxHeight).
			Radius(t.Radius.X2l).Clip().Border(theme.Hairline, t.Border)
		glassPanel(panel, t)
		Elevate(c, panel, theme.ShadowX2l)
		var in ui.Element
		if o.Send != "" {
			paletteSel = ui.Element{}
			ui.Column(c.Key("send")).Shrink(0).Padding(th.Space(3)).Children(func() {
				ui.Column(c).Gap(th.Space(2)).Padding(th.Space(3)).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
					in = ui.TextAreaBase(c, query).Placeholder(o.Placeholder).Label(o.Label).FocusRing(false).AutoFocus().
						MinHeight(th.Space(14)).MaxHeight(th.Space(60)).FontSize(t.Text.Base)
					if o.OnKey != nil {
						in.OnTextKey(o.OnKey)
					}
					ui.Row(c).Justify(ui.End).Children(func() {
						IconButton(c, "arrow-up", o.Send, ButtonOpts{Size: SizeSm, Disabled: strings.TrimSpace(*query) == ""}).OnClick(o.OnSend)
					})
				})
			})
			head, body = nil, nil
		} else {
			ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(3), th.Space(3), th.Space(2), th.Space(4)).Children(func() {
				ui.Icon(c, icons.Get("search")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
				if o.Chips != nil {
					o.Chips()
				}
				in = ui.TextInputBase(c, query).Placeholder(o.Placeholder).Label(o.Label).FocusRing(false).AutoFocus().Grow(1).FontSize(t.Text.Base)
				if o.OnKey != nil {
					in.OnTextKey(o.OnKey)
				}
				if o.Keys != nil || paletteHeld != 0 {
					in.ReadOnly(true).HandleInput(func(ev ui.InputEvent) bool {
						switch {
						case ev.Kind == ui.InputKeyUp && ev.Key == paletteHeld:
							paletteHeld = 0
						case ev.Kind == ui.InputKeyDown && o.Keys != nil && o.Keys(ev.Mods, ev.Key):
							paletteHeld = ev.Key
							return true
						}
						return false
					})
				}
				IconButton(c, "x", o.Close, ButtonOpts{Variant: Ghost, Size: SizeSm}).OnClick(func() { *open = false })
			})
		}
		if head != nil {
			ui.Row(c).Role(ui.RoleTabList).KeepFocus().Shrink(0).Gap(th.Space(1)).Padding(0, th.Space(3), th.Space(2), th.Space(3)).Children(head)
		}
		if body != nil {
			paletteSel = ui.Element{}
			ui.Scroll(c).Shrink(1).Grow(1).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
				ui.Column(c).Role(ui.RoleList).KeepFocus().Label(o.Label).Padding(th.Space(2)).Gap(th.Space(0.5)).Children(body)
			})
			if paletteSel != (ui.Element{}) {
				in.ActiveDescendant(paletteSel)
			}
		}
		if len(o.Hints) > 0 {
			ui.Row(c).Shrink(0).Gap(th.Space(4)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(4)).
				BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
				for _, h := range o.Hints {
					hint := func() {
						Kbd(c, h.Key)
						Text(c, h.Label, t.Text.Xs).TextColor(t.Muted)
					}
					if h.OnClick != nil {
						ui.Spacer(c)
						Pressable(c, PressableOpts{OnClick: h.OnClick}).Label(h.Label).Gap(th.Space(1.5)).AlignItems(ui.Center).
							Padding(th.Space(0.5), th.Space(1)).Margin(0, -th.Space(1)).Radius(t.Radius.Md).Children(hint)
					} else {
						ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(hint)
					}
				}
			})
		}
	})
}

// PaletteTab is a kind tab of a Palette's head, tinted while chosen.
func PaletteTab(c *ui.Context, label string, chosen bool) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Role(ui.RoleTab).Selected(chosen).Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).
		FontSize(t.Text.Sm).TextColor(t.Muted).Transition(colors(theme.MotionFast))
	if chosen {
		b.Background(t.HoverTint).TextColor(t.Foreground)
	}
	return b.Children(func() { ui.Text(c, label) })
}

// PalettePreview is a card 8px off a Palette's right edge, level with the
// selected PaletteRow, which it follows as the list scrolls; body builds
// its content. Build it after the Palette in the same frame, while open.
func PalettePreview(c *ui.Context, body func()) {
	row := paletteSel
	if row == (ui.Element{}) {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Overlay(c, func() {
		// The row stops the list's 8px padding short of the panel's edge.
		card := ui.Column(c).AttachTo(row, ui.AnchorTopEnd, ui.AnchorTopStart).MarginStart(th.Space(4)).
			Width(th.Space(72)).Gap(th.Space(2)).Padding(th.Space(4)).Radius(t.Radius.Xl).Clip().
			Material(LiquidGlass(t.GlassBg)).
			Border(theme.Hairline, t.GlassBorder)
		Elevate(c, card, theme.ShadowLg)
		card.Children(body)
	})
}

// PaletteRow is a one-line result of a Palette for content the caller
// builds: an option of its list, tinted while selected. Give it its Label,
// Gap and Children.
func PaletteRow(c *ui.Context, selected bool) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Role(ui.RoleListItem).Selected(selected).Justify(ui.Start).AlignItems(ui.Center).
		Height(th.Space(9)).PaddingX(th.Space(2.5)).Radius(t.Radius.Lg).
		TextColor(t.Foreground) // not a Selected button's inverted text: the tint is light
	if selected {
		b.Background(t.HoverTint)
		paletteSel = b
	}
	return b
}

// PaletteChip is a filter set in a Palette's search row: its label and a
// button, named remove, which it returns for the caller's OnClick.
func PaletteChip(c *ui.Context, label, remove string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	var x ui.Element
	ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(th.Space(0.5)).Height(th.Space(6)).Padding(0, th.Space(0.5), 0, th.Space(2)).
		Radius(t.Radius.Md).Background(t.HoverTint).Children(func() {
		Text(c, label, t.Text.Sm).SingleLine()
		x = IconButton(c, "x", remove, ButtonOpts{Variant: Ghost, Size: SizeXs})
	})
	return x
}

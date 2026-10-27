package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// PaletteOpts name a Palette and its search box. OnKey sees the keys the
// box types no text with (the arrows, Enter, Tab, those with Cmd or Ctrl)
// before it does, as ui's OnTextKey: true takes the key. Glass draws the
// panel as frosted glass, for a window on a material; else it is opaque.
type PaletteOpts struct {
	Label, Placeholder, Close string
	OnKey                     func(k ui.TextKey) bool
	Hints                     []PaletteHint
	Glass                     bool
}

// PaletteHint is a shortcut the palette's bottom bar names: Key, then Label.
type PaletteHint struct{ Label, Key string }

// paletteSel is the selected PaletteRow built this frame, which the search
// box names to assistive technology. UI thread only.
var paletteSel ui.Element

// Palette is a modal search panel near the top of the window: a search box
// bound to query with a close button, head (the kind tabs) under it, body
// (the result list, of PaletteRows) scrolling under that, and a bar of
// o.Hints. The box has the focus; Escape, a click outside and the close
// button close it.
func Palette(c *ui.Context, open *bool, query *string, o PaletteOpts, head, body func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionBase)
	dim := float32(0.1)
	if t.Dark {
		dim = 0.4
	}
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogShown = true
		backdrop.Material(scrim{theme.BackdropBlur * k, ink(t).Alpha(dim * k)}).Justify(ui.Start).Padding(th.Space(16), 0, 0, 0)
		panel.Label(o.Label).Opacity(k).Margin(rise(k), 0, 0, 0).Width(theme.DialogWideWidth).MaxHeight(theme.SheetMaxHeight).
			Radius(t.Radius.X2l).Clip().Border(theme.Hairline, t.Border)
		if o.Glass {
			panel.Material(scrim{theme.BackdropBlur * 4, t.Background.Alpha(0.8)})
		} else {
			panel.Background(t.Background)
		}
		Elevate(c, panel, theme.ShadowXl)
		var in ui.Element
		ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(3), th.Space(3), th.Space(2), th.Space(4)).Children(func() {
			ui.Icon(c, icons.Get("search")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
			in = ui.TextInputBase(c, query).Placeholder(o.Placeholder).Label(o.Label).FocusRing(false).AutoFocus().Grow(1).FontSize(t.Text.Base)
			if o.OnKey != nil {
				in.OnTextKey(o.OnKey)
			}
			IconButton(c, "x", o.Close, ButtonOpts{Variant: Ghost, Size: SizeSm}).OnClick(func() { *open = false })
		})
		if head != nil {
			ui.Row(c).Role(ui.RoleTabList).KeepFocus().Shrink(0).Gap(th.Space(1)).Padding(0, th.Space(3), th.Space(2), th.Space(3)).Children(head)
		}
		paletteSel = ui.Element{}
		ui.Scroll(c).Shrink(1).Grow(1).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Column(c).Role(ui.RoleList).KeepFocus().Label(o.Label).Padding(th.Space(2)).Gap(th.Space(0.5)).Children(body)
		})
		if paletteSel != (ui.Element{}) {
			in.ActiveDescendant(paletteSel)
		}
		if len(o.Hints) > 0 {
			ui.Row(c).Shrink(0).Gap(th.Space(4)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(4)).
				BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
				for _, h := range o.Hints {
					ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
						Kbd(c, h.Key)
						Text(c, h.Label, t.Text.Xs).TextColor(t.Muted)
					})
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
			Width(th.Space(72)).Gap(th.Space(2)).Padding(th.Space(4)).Radius(t.Radius.Xl).
			Background(t.Background).Border(theme.Hairline, t.Border)
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
		Height(th.Space(9)).PaddingX(th.Space(2.5)).Radius(t.Radius.Lg)
	if selected {
		b.Background(t.HoverTint)
		paletteSel = b
	}
	return b
}

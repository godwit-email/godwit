package kit

import (
	"sync"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
	"github.com/godwit-email/godwit/internal/theme"
)

// DialogSize is a dialog's width: DialogMd for a form, DialogSm for a
// confirmation, DialogLg for a larger form.
type DialogSize uint8

const (
	DialogMd DialogSize = iota
	DialogSm
	DialogLg
)

// DialogOpts are a dialog's size, the muted line under its title, the label
// of its close button, and Danger, which tints the footer for a confirmation.
// Alert makes it one question (confirm, delete, open a link): the alert
// backdrop and no close button, Cancel, Escape or a click outside closing it.
// Top keeps it near the window's top instead of above centre, for a form
// whose height changes as it is used (steps, results): it grows down.
type DialogOpts struct {
	Size               DialogSize
	Desc, Close        string
	Danger, Alert, Top bool
}

// Dialog is a modal panel anchored near the top: a header with the title, the
// description and a close button, a scrolling body, and a footer bar holding
// footer's buttons at the end. body and footer may be nil. Escape, a click
// outside and the close button close it.
func Dialog(c *ui.Context, open *bool, title string, o DialogOpts, body, footer func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionBase)
	width, maxH := float32(theme.SheetWidth), float32(theme.SheetMaxHeight)
	switch o.Size {
	case DialogSm:
		width = theme.DialogSmWidth
	case DialogLg:
		width, maxH = theme.DialogWideWidth, theme.DialogWideMax
	}
	scrim := t.Scrim
	if o.Alert {
		scrim = t.AlertScrim
	}
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogShown = true
		backdrop.Background(scrim.Alpha(k)).Transition(leave(c)) // Alpha scales: the scrim fades with the panel
		if o.Top {
			backdrop.Justify(ui.Start).Padding(theme.DialogTop, 0, 0, 0)
		} else {
			aboveCentre(c, backdrop, panel)
		}
		panel.Opacity(k).Margin(rise(k), 0, 0, 0).Width(width).MaxHeight(maxH).Radius(t.Radius.X2l).Clip().Border(theme.Hairline, t.GlassBorder)
		glassPanel(c, panel)
		Elevate(c, panel, theme.ShadowX2l)
		header := ui.Row(c).Shrink(0).AlignItems(ui.Start).Gap(th.Space(4)).Padding(th.Space(4))
		if body != nil {
			header.Padding(th.Space(4), th.Space(4), th.Space(4)-theme.RingWidth, th.Space(4))
		}
		header.Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
				Text(c, title, t.Text.Base).FontWeight(600)
				if o.Desc != "" {
					ui.Text(c, o.Desc).TextColor(t.Muted)
				}
			})
			if !o.Alert {
				// Into the corner: the button's own padding keeps the glyph in line with the title.
				IconButton(c, "x", o.Close, ButtonOpts{Variant: Ghost, Size: SizeSm}).Margin(-th.Space(2), -th.Space(2), 0, 0).OnClick(func() { *open = false })
			}
		})
		if body != nil {
			ui.Scroll(c).Shrink(1).Children(func() {
				// Keep the first field's outer ring inside the viewport, without moving the content.
				ui.Column(c).Padding(theme.RingWidth, th.Space(4), th.Space(4), th.Space(4)).Gap(th.Space(4)).Children(body)
			})
		}
		if footer != nil {
			bar := ui.Row(c).Shrink(0).Gap(th.Space(2)).AlignItems(ui.Center).Justify(ui.End).Padding(th.Space(3), th.Space(4)).
				BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Background(t.Sidebar)
			if o.Danger {
				bar.BorderColor(t.DangerBorder).Background(t.DangerBg)
			}
			bar.Children(footer)
		}
	})
}

// aboveCentre sets panel a little above the middle of backdrop: its centre
// at 40% of the window, yet at least 32 below the top. The window's height
// is the root's last frame, known before the dialog opens, so its first
// frame is already in place; a panel taller than the room for that is
// measured a frame late.
func aboveCentre(c *ui.Context, backdrop, panel ui.Element) {
	h, p := c.Root().LayoutBounds().H, panel.LayoutBounds().H
	lift := max(0, min(h*theme.DialogLift, h-p-2*theme.DialogMinTop))
	backdrop.Padding(0, 0, lift, 0)
}

// dialogShown is whether a Dialog was built since DialogShown last said. UI thread only.
var dialogShown bool

// DialogShown reports whether a Dialog was built since it was last called,
// and forgets it. A window on a transparent material paints itself opaque
// under one: the panel's frost covers only what is opaque, and sharp text
// on the material would show through it.
func DialogShown() bool {
	s := dialogShown
	dialogShown = false
	return s
}

// BackdropBlur turns the frosted panels' blur on. Tests turn it off: the
// CPU renderer the Tester draws with blurs pixel by pixel, which under -race
// runs every dialog test many times slower.
var BackdropBlur = true

// Glass frosts the panels of dialogs and the search palette: set where the
// window sits on a material (Deps.Vibrancy); elsewhere they are opaque. The
// backdrop under them only tints the window.
var Glass bool

// glassPanel fills a dialog's panel: the dialog material under Glass, else opaque.
func glassPanel(c *ui.Context, panel ui.Element) {
	t := theme.Of(c)
	if Glass {
		panel.Material(frostedMaterial(c, t.MaterialDialog))
	} else {
		panel.Background(t.Raised)
	}
}

// scrim is what is behind blurred, then tinted: a frosted surface.
type scrim struct {
	blur   float32
	tint   ui.Color
	native bool // back with the system's glass under the surface (macOS 26+) so live web views blur in sync
}

// NativeGlass backs a translucent Bar with AppKit's NSGlassEffectView under
// MyGo's surface (above any embedded WKWebView) on macOS 26+. Native UI stays
// opaque over it and keeps glass.Blur; a web view's hole shows the system's
// live-blurred glass through the bar's translucent tint on the same
// CoreAnimation frame, eliminating scroll-lag ghosting.
func (s scrim) NativeGlass() (ui.NativeGlassSpec, bool) {
	if !s.native || s.tint.A >= 255 {
		return ui.NativeGlassSpec{}, false
	}
	return ui.NativeGlassSpec{}, true
}

var (
	lightWindow = theme.Light().Window
	darkWindow  = theme.Dark().Window
)

type frostProg struct {
	sh            *shader.Shader
	backing, tint shader.Uniform
}

// frostShader composites the blurred backdrop over Backing before tinting and
// outputs opaque alpha: over a translucent vibrancy surface (the sidebar), plain
// glass.Blur keeps the backdrop's partial alpha so SrcOver leaves sharp text
// underneath showing through the blur.
var frostShader = sync.OnceValue(func() frostProg {
	sh := shader.MustCompile(`package main

var Backing vec4
var Tint vec4

func Fragment(dst vec4, pos vec2) vec4 {
	b := backdropSample(pos)
	c := b.rgb + Backing.rgb*(1-b.a)
	return vec4(mix(c, Tint.rgb, Tint.a), 1)
}
`)
	return frostProg{sh: sh, backing: sh.Uniform("Backing"), tint: sh.Uniform("Tint")}
})

func (s scrim) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	if box.W <= 0 || box.H <= 0 {
		return
	}
	if !BackdropBlur || s.tint.A >= 255 {
		fillRounded(p, box, s.tint, radii)
		return
	}
	if s.native {
		glass.Blur{Radius: s.blur}.PaintMaterial(p, box, radii)
		fillRounded(p, box, s.tint, radii)
		return
	}
	backing := lightWindow
	if p.Theme().Dark {
		backing = darkWindow
	}
	if p.Preferences().ReduceTransparency {
		fillRounded(p, box, s.tint.Over(backing), radii) // Over keeps it opaque, as Reduce Transparency asks
		return
	}
	fp := frostShader()
	vals := fp.sh.NewValues().Color(fp.backing, backing).Color(fp.tint, s.tint)
	drawRounded(p, box, radii, func(r float32) {
		fp.sh.Draw(p, box, shader.Options{Values: vals, Radius: r, BackdropBlur: s.blur})
	})
}

// drawRounded draws into box rounded by radii (top-left, top-right, bottom-right,
// bottom-left), as a settings card's first or last row is. Painter rounds
// every corner alike, so a box round on one side only is drawn inside a
// clip that reaches past its other side by the radius.
// shortcut: takes the top pair and the bottom pair alike; a box with four
// different corners needs a path.
func drawRounded(p *ui.Painter, box ui.Rect, radii [4]float32, draw func(radius float32)) {
	top, bottom := radii[0], radii[2]
	switch {
	case top == bottom:
		draw(top)
	case bottom == 0:
		p.Clip(ui.Rect{X: box.X, Y: box.Y, W: box.W, H: box.H + top}, top, func() { draw(0) })
	case top == 0:
		p.Clip(ui.Rect{X: box.X, Y: box.Y - bottom, W: box.W, H: box.H + bottom}, bottom, func() { draw(0) })
	default:
		draw(min(top, bottom))
	}
}

func fillRounded(p *ui.Painter, box ui.Rect, c ui.Color, radii [4]float32) {
	drawRounded(p, box, radii, func(r float32) { p.Fill(box, c, r) })
}

// frostedMaterial is one of the design's materials: what is behind blurred
// by theme.MaterialBlur, then tinted with tint (a theme.Tokens Material*).
// Call it while building the panel.
func frostedMaterial(c *ui.Context, tint ui.Color) ui.Material {
	frostOf(c).now = true // the window's web views under it show a screenshot it can blur (WebView)
	return scrim{blur: theme.MaterialBlur, tint: tint}
}

// popupLook gives a popover, menu or select popup the popover material, its
// edge and drop, rounded by radius.
func popupLook(c *ui.Context, panel ui.Element, radius float32) {
	popupFace(c, panel, radius, frostedMaterial(c, theme.Of(c).MaterialPopover))
}

// toastLook is popupLook for a toast, which is not modal: the page under it
// stays live, so its material does not turn web views into stills (WebView).
func toastLook(c *ui.Context, panel ui.Element, radius float32) {
	popupFace(c, panel, radius, scrim{blur: theme.MaterialBlur, tint: theme.Of(c).MaterialPopover})
}

func popupFace(c *ui.Context, panel ui.Element, radius float32, m ui.Material) {
	t := theme.Of(c)
	panel.Radius(radius).Clip().Material(m).Border(theme.Hairline, t.GlassBorder)
	Elevate(c, panel, theme.ShadowPopover)
}

// popoverCard is popupLook for a card of content: 16 round, 16 of padding,
// its children 10 apart.
func popoverCard(c *ui.Context, panel ui.Element) {
	th := c.Theme()
	popupLook(c, panel, theme.Of(c).Radius.X2l)
	panel.Padding(th.Space(4)).Gap(th.Space(2.5))
}

// Frost fills a row with fill (the surface it sits on) over what is behind it
// blurred: plain at rest, frosted glass as a drag lifts the row over others.
// shortcut: its 0.7 tint has no design token, as it is a drag effect and not a material; give it one if the design names it.
func Frost(fill ui.Color) ui.Material {
	return scrim{blur: theme.BackdropBlur * 2, tint: fill.Alpha(0.7)}
}

// Bar fills a bar the content scrolls under (the reader's sticky header and
// floating reply card) with tint, theme.Tokens.MaterialBar, over what is
// behind it blurred. On macOS 26+, it also backs the bar with AppKit's
// NSGlassEffectView under the surface so live web views blur in sync with
// CoreAnimation; elsewhere the web view blurs its covered strips itself
// (the reader's BodyGlass).
func Bar(tint ui.Color) ui.Material {
	return scrim{blur: theme.MaterialBlur, tint: tint, native: true}
}

// PopoverAt is a panel anchored to anchor at (at, self) with topOffset while *open; a press outside or Escape closes it.
func PopoverAt(c *ui.Context, anchor ui.Element, open *bool, at, self ui.Anchor, topOffset float32, body func()) {
	k := enter(c, open, theme.MotionFast)
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.AttachTo(anchor, at, self).
			Opacity(k).Margin(topOffset+rise(k), 0, 0, 0).Transition(leave(c))
		popoverCard(c, panel)
		body()
	})
}

// MenuAt is a menu anchored as PopoverAt: a tight panel of MenuItems,
// perhaps under a MenuLabel, labelled label.
func MenuAt(c *ui.Context, anchor ui.Element, open *bool, at, self ui.Anchor, topOffset float32, label string, body func()) {
	t, th := theme.Of(c), c.Theme()
	k := enter(c, open, theme.MotionFast)
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.AttachTo(anchor, at, self).Role(ui.RoleMenu).Label(label).
			Opacity(k).Margin(topOffset+rise(k), 0, 0, 0).Padding(th.Space(1)).Transition(leave(c))
		popupLook(c, panel, t.Radius.Lg)
		body()
	})
}

// MenuLabel is a muted heading over a menu's items.
func MenuLabel(c *ui.Context, text string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return Text(c, text, t.Text.Xs).FontWeight(500).TextColor(t.Muted).SingleLine().Padding(th.Space(1), th.Space(1.5))
}

// MenuItem is an item of a menu, tinted while selected (the keys' choice);
// give it its Label and Children.
func MenuItem(c *ui.Context, selected bool) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Role(ui.RoleMenuItem).Selected(selected).Justify(ui.Start).AlignItems(ui.Center).Gap(th.Space(1.5)).
		Height(th.Space(7)).PaddingX(th.Space(1.5)).Radius(t.Radius.Md).TextColor(t.Foreground) // a tint, not the inverted text
	if selected || b.Hovered() {
		b.Background(t.HoverTint)
	}
	return b
}

// Popover is a panel below anchor while *open; a press outside or Escape closes it.
func Popover(c *ui.Context, anchor ui.Element, open *bool, body func()) {
	PopoverAt(c, anchor, open, ui.AnchorBottomStart, ui.AnchorTopStart, theme.PopupOffset, body)
}

// Tooltip shows text by anchor on keyboard focus at once and on hover after a
// pause, and describes anchor to assistive technology.
func Tooltip(c *ui.Context, anchor ui.Element, text string) { tooltip(c, anchor, text, true) }

// TooltipLabelled is Tooltip for an anchor whose Label already says text.
func TooltipLabelled(c *ui.Context, anchor ui.Element, text string) { tooltip(c, anchor, text, false) }

// tooltip is Tooltip; describe is false where anchor's Label already says text.
func tooltip(c *ui.Context, anchor ui.Element, text string, describe bool) {
	t, th := theme.Of(c), c.Theme()
	if describe {
		anchor.Description(text)
	}
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.Padding(th.Space(1.5), th.Space(3)).Radius(t.Radius.Md).Background(t.Primary).TextColor(t.PrimaryText).FontSize(t.Text.Xs)
		tip.Children(func() { ui.Text(c, text) })
	})
}

// HoverCard shows a read-only popover card w wide below anchor, at its start,
// as Tooltip shows its text (after 600 ms of hover, at once on keyboard focus,
// the pointer passing through it): body fills it.
func HoverCard(c *ui.Context, anchor ui.Element, w float32, body func()) {
	t, th := theme.Of(c), c.Theme()
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.AttachTo(anchor, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(theme.PopupOffset, 0, 0, 0).Width(w).TextColor(t.Foreground)
		// popoverCard's padding on toastLook: not modal, so the page's web views stay live
		toastLook(c, tip, t.Radius.X2l)
		tip.Padding(th.Space(4)).Gap(th.Space(2.5))
		tip.Children(body)
	})
}

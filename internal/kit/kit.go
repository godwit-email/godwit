// Package kit is Godwit's component set: shadcn/ui Nova looks (neutral) built
// on mygo's *Base widgets. Colors come only from theme.Of(c); variants are
// arguments, not method chains.
package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// Variant is a button's look.
type Variant int

const (
	Primary Variant = iota
	Outline
	Ghost
	Destructive
)

// ButtonOpts style a button. Icon is a Lucide name shown before the label.
type ButtonOpts struct {
	Variant         Variant
	Small, Disabled bool
	Icon            string
}

// px scales a size by the density: comfortable rows and paddings are 1.25×.
func px(t theme.Tokens, v float32) float32 { return v * t.Scale }

// small and xsmall are text sizes below the interface font size.
func small(t theme.Tokens) float32  { return t.FontSize - 1 }
func xsmall(t theme.Tokens) float32 { return t.FontSize - 2 }

// ink is the color of shadows and backdrops: dark in either appearance.
func ink(t theme.Tokens) ui.Color {
	if t.Dark {
		return t.Background
	}
	return t.Foreground
}

func button(c *ui.Context, o ButtonOpts, square bool) ui.Element {
	t := theme.Of(c)
	h, padX := px(t, 32), px(t, 14)
	if o.Small {
		h, padX = px(t, 28), px(t, 10)
	}
	b := ui.ButtonBase(c).Height(h).Radius(8).Gap(px(t, 6)).Justify(ui.Center).Disabled(o.Disabled)
	if square {
		b.Width(h).Radius(6)
	} else {
		b.PaddingX(padX)
	}
	hover := b.Hovered() && !o.Disabled
	switch o.Variant {
	case Primary:
		b.Background(t.Primary).TextColor(t.PrimaryText).FontWeight(500)
		if hover {
			b.Background(t.Primary.Mix(t.Background, 0.1))
		}
	case Outline:
		b.Background(t.Background).Border(1, t.Border).TextColor(t.Foreground)
		if hover {
			b.Background(t.Hover)
		}
	case Ghost:
		b.TextColor(t.Foreground)
		if square {
			b.TextColor(t.Muted)
		}
		if hover {
			b.Background(t.Hover).TextColor(t.Foreground)
		}
	case Destructive:
		b.TextColor(t.Danger)
		if hover {
			b.Background(t.DangerBg)
		}
	}
	if o.Disabled {
		b.Opacity(0.5)
	}
	return b
}

// Button is a button of a label, 32 high (28 small).
func Button(c *ui.Context, label string, o ButtonOpts) ui.Element {
	return button(c, o, false).Children(func() {
		if o.Icon != "" {
			ui.Icon(c, icons.Get(o.Icon)).Size(16, 16)
		}
		ui.Text(c, label)
	})
}

// IconButton is a square button showing only an icon, named label for
// assistive technology and tests.
func IconButton(c *ui.Context, icon, label string, o ButtonOpts) ui.Element {
	return button(c, o, true).Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, icons.Get(icon)).Size(16, 16)
	})
}

// Switch is a 32×18 switch; give it a Label.
func Switch(c *ui.Context, on *bool) ui.Element {
	t := theme.Of(c)
	s := ui.SwitchBase(c, on).Size(32, 18).Padding(2).Radius(9).Background(t.Border).Justify(ui.Start)
	if *on {
		s.Background(t.Primary).Justify(ui.End)
	}
	thumb := t.Background
	if t.Dark && !*on {
		thumb = t.Foreground // a dark thumb would vanish into the dark track
	}
	return s.Children(func() {
		ui.Box(c).Size(14, 14).Radius(7).Background(thumb).Shadow(0, 1, 2, 0, ink(t).Alpha(0.2))
	})
}

// Checkbox is a 16×16 check box before its label.
func Checkbox(c *ui.Context, on *bool, label string) ui.Element {
	t := theme.Of(c)
	return ui.CheckboxBase(c, on).Gap(px(t, 8)).Children(func() {
		box := ui.Box(c).Size(16, 16).Radius(4).Border(1, t.Border).Background(t.Background).Center()
		if *on {
			box.Background(t.Primary).Border(1, t.Primary).TextColor(t.PrimaryText).Children(func() {
				ui.Icon(c, icons.Get("check")).Size(12, 12)
			})
		}
		ui.Text(c, label)
	})
}

// Radio is a radio button choosing v into *sel.
func Radio[T comparable](c *ui.Context, sel *T, v T, label string) ui.Element {
	t := theme.Of(c)
	return ui.RadioBase(c, sel, v).Gap(px(t, 8)).Children(func() {
		dot := ui.Box(c).Size(16, 16).Radius(8).Border(1, t.Border).Background(t.Background).Center()
		if *sel == v {
			dot.Border(1, t.Primary).Children(func() { ui.Box(c).Size(8, 8).Radius(4).Background(t.Primary) })
		}
		ui.Text(c, label)
	})
}

// Option is one choice of a Select.
type Option[T comparable] struct {
	Value T
	Label string
}

// Select is a 32-high select of opts; give it a Label.
func Select[T comparable](c *ui.Context, sel *T, opts []Option[T]) ui.Element {
	t := theme.Of(c)
	s := ui.SelectBase(c, sel)
	current := ""
	for _, o := range opts {
		if o.Value == *sel {
			current = o.Label
		}
	}
	s.Trigger.Height(px(t, 32)).PaddingX(px(t, 10)).Gap(px(t, 8)).Radius(6).Border(1, t.Border).Background(t.Background).MinWidth(120).Children(func() {
		ui.Text(c, current).Grow(1)
		ui.Icon(c, icons.Get("chevron-down")).Size(16, 16).TextColor(t.Muted)
	})
	s.Popup(func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Padding(px(t, 4)).Radius(8).Background(t.Background).Border(1, t.Border).
			Shadow(0, 4, 16, 0, ink(t).Alpha(0.12))
		for _, o := range opts {
			item := s.Item(o.Value).Height(px(t, 32)).PaddingX(px(t, 8)).Gap(px(t, 8)).Radius(6)
			if item.Highlighted() {
				item.Background(t.Hover)
			}
			item.Children(func() {
				ui.Text(c, o.Label).Grow(1)
				check := ui.Icon(c, icons.Get("check")).Size(16, 16)
				if o.Value != *sel {
					check.Opacity(0)
				}
			})
		}
	})
	return s.Trigger
}

// InputOpts style a text input.
type InputOpts struct {
	Placeholder             string
	Password, Mono, Invalid bool
}

func input(c *ui.Context, e ui.Element, o InputOpts) ui.Element {
	t := theme.Of(c)
	e.PaddingX(px(t, 10)).Radius(6).Border(1, t.Border).Background(t.Background).Placeholder(o.Placeholder)
	if o.Mono {
		e.Font("Geist Mono").FontSize(t.FontSize)
	}
	if o.Password {
		e.Password()
	}
	if o.Invalid {
		e.Border(1, t.Danger)
	}
	return e
}

// Input is a 32-high single-line text input.
func Input(c *ui.Context, s *string, o InputOpts) ui.Element {
	return input(c, ui.TextInputBase(c, s).Height(px(theme.Of(c), 32)), o)
}

// TextArea is a multi-line text input.
func TextArea(c *ui.Context, s *string, o InputOpts) ui.Element {
	return input(c, ui.TextAreaBase(c, s).PaddingY(px(theme.Of(c), 8)), o)
}

// Field is a labelled form control with help text, or an error in its place.
func Field(c *ui.Context, label, help, errText string, control func()) {
	t := theme.Of(c)
	ui.Column(c).Gap(px(t, 6)).Children(func() {
		ui.Text(c, label).FontWeight(500)
		control()
		switch {
		case errText != "":
			ui.Text(c, errText).FontSize(small(t)).TextColor(t.Danger)
		case help != "":
			ui.Text(c, help).FontSize(small(t)).TextColor(t.Muted)
		}
	})
}

// Tabs is a segmented tab list choosing *sel.
func Tabs(c *ui.Context, sel *int, labels ...string) ui.Element {
	t := theme.Of(c)
	tabs := ui.TabsBase(c, sel, len(labels))
	return tabs.List.Padding(px(t, 3)).Radius(8).Background(t.Hover).Gap(px(t, 2)).Children(func() {
		for i, l := range labels {
			tab := tabs.Tab(i).Height(px(t, 26)).PaddingX(px(t, 10)).Radius(6).FontWeight(500).TextColor(t.Muted)
			if i == *sel {
				tab.Background(t.Background).TextColor(t.Foreground).Shadow(0, 1, 2, 0, ink(t).Alpha(0.1))
			}
			tab.Children(func() { ui.Text(c, l) })
		}
	})
}

// Dialog is a 560-wide modal panel; Escape and a click outside close it.
func Dialog(c *ui.Context, open *bool, title string, body, footer func()) {
	t := theme.Of(c)
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ink(t).Alpha(0.4))
		panel.Width(560).Radius(14).Padding(px(t, 24)).Gap(px(t, 16)).Background(t.Background).Border(1, t.Border).
			Shadow(0, 16, 48, 0, ink(t).Alpha(0.25))
		ui.Text(c, title).FontSize(t.FontSize + 3).FontWeight(600)
		body()
		if footer != nil {
			ui.Row(c).Gap(px(t, 8)).Justify(ui.End).Children(footer)
		}
	})
}

// Popover is a panel below anchor while *open; a press outside or Escape closes it.
func Popover(c *ui.Context, anchor ui.Element, open *bool, body func()) {
	t := theme.Of(c)
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Padding(px(t, 12)).Radius(8).Gap(px(t, 8)).Background(t.Background).Border(1, t.Border).
			Shadow(0, 4, 16, 0, ink(t).Alpha(0.12))
		body()
	})
}

// Badge is a small gray label.
func Badge(c *ui.Context, text string) ui.Element {
	t := theme.Of(c)
	return ui.Box(c).Padding(px(t, 1), px(t, 6)).Radius(4).Background(t.Hover).Children(func() {
		ui.Text(c, text).FontSize(small(t)).TextColor(t.Foreground)
	})
}

// AlertKind is an Alert's look.
type AlertKind int

const (
	Warning AlertKind = iota
	Danger
	Info
)

// Alert is a bordered message bar with optional actions after the text.
func Alert(c *ui.Context, kind AlertKind, text string, actions func()) ui.Element {
	t := theme.Of(c)
	bg, border, fg := t.Background, t.Border, t.Foreground
	switch kind {
	case Warning:
		bg, border, fg = t.WarningBg, t.WarningBorder, t.WarningText
	case Danger:
		bg, border, fg = t.DangerBg, t.Danger.Alpha(0.3), t.Danger
	}
	return ui.Row(c).Wrap().Gap(px(t, 12)).Padding(px(t, 8), px(t, 12)).Radius(8).Background(bg).Border(1, border).TextColor(fg).
		FontSize(t.FontSize).Children(func() {
		ui.Text(c, text)
		if actions != nil {
			actions()
		}
	})
}

// Status is what a StatusDot tells.
type Status int

const (
	OK Status = iota
	Attention
	Error
)

// StatusDot is a 6×6 dot: green OK, amber Attention, red Error.
func StatusDot(c *ui.Context, s Status) ui.Element {
	t := theme.Of(c)
	color := t.Success
	switch s {
	case Attention:
		color = t.Attention
	case Error:
		color = t.Danger
	}
	return ui.Box(c).Size(6, 6).Radius(3).Background(color)
}

// Card is a bordered panel.
func Card(c *ui.Context, body func()) ui.Element {
	t := theme.Of(c)
	return ui.Column(c).Gap(px(t, 12)).Padding(px(t, 16)).Radius(12).Background(t.Background).Border(1, t.Border).Children(body)
}

// Spinner shows that something is under way.
func Spinner(c *ui.Context) ui.Element {
	return ui.Spinner(c).TextColor(theme.Of(c).Muted)
}

// Kbd shows a key or shortcut.
func Kbd(c *ui.Context, text string) ui.Element {
	t := theme.Of(c)
	return ui.Box(c).Height(px(t, 20)).PaddingX(px(t, 5)).Radius(4).Border(1, t.Border).Background(t.Sidebar).Center().Children(func() {
		ui.Text(c, text).Font("Geist Mono").FontSize(xsmall(t)).TextColor(t.Muted)
	})
}

// ToastHost draws the window's toasts (c.Toast, c.AddToast) at the bottom
// right; call it once in the view. closeLabel names the close buttons (pass
// the translated "Close"). Type "error" shows the error icon.
func ToastHost(c *ui.Context, closeLabel string) {
	t := theme.Of(c)
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(px(t, 16)).AlignItems(ui.End).Justify(ui.End).Gap(px(t, 8))
		for _, tst := range toasts {
			toast := ui.ToastBase(c, tst)
			toast.Root.Row().Gap(px(t, 10)).Padding(px(t, 10), px(t, 14)).Radius(8).Background(t.Background).Border(1, t.Border).
				Shadow(0, 4, 16, 0, ink(t).Alpha(0.12)).MaxWidth(420).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 8}, Exit: &ui.Motion{}})
			toast.Root.Children(func() {
				if tst.Type == "error" {
					ui.Icon(c, icons.Get("circle-x")).Size(16, 16).TextColor(t.Danger)
				}
				ui.Column(c).Grow(1).Gap(px(t, 2)).Children(func() {
					ui.Text(c, tst.Title).FontWeight(500)
					if tst.Description != "" {
						ui.Text(c, tst.Description).FontSize(small(t)).TextColor(t.Muted)
					}
				})
				if tst.Action != "" {
					toast.ActionButton().Height(px(t, 28)).PaddingX(px(t, 10)).Radius(6).Border(1, t.Border).Children(func() {
						ui.Text(c, tst.Action)
					})
				}
				toast.CloseButton().Label(closeLabel).Size(20, 20).Radius(4).Center().TextColor(t.Muted).Children(func() {
					ui.Icon(c, icons.Get("x")).Size(14, 14)
				})
			})
		}
	})
}

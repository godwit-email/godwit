package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// switchTrack draws a switch: a 32×18 track and its thumb.
func switchTrack(c *ui.Context, on bool) ui.Element {
	t := theme.Of(c)
	tr := ui.Row(c).Size(theme.SwitchWidth, theme.SwitchHeight).Shrink(0).Padding(theme.SwitchInset).Radius(t.Radius.Full).Background(t.Input).Justify(ui.Start).Transition(colors(theme.MotionFast))
	thumb := t.Background
	switch {
	case on:
		tr.Background(t.Primary).Justify(ui.End)
	case t.Dark:
		thumb = t.Foreground // a dark thumb would vanish into the dark track
	}
	return tr.Children(func() {
		ui.Box(c).Size(theme.SwitchThumb, theme.SwitchThumb).Radius(t.Radius.Full).Background(thumb).Transition(ui.ElementTransition{Position: true, Duration: theme.MotionFast})
	})
}

// Switch is a 32×18 switch; give it a Label.
func Switch(c *ui.Context, on *bool) ui.Element {
	return ui.SwitchBase(c, on).Radius(theme.Of(c).Radius.Full).Children(func() { switchTrack(c, *on) })
}

// SwitchRow is a full-width switch named label in the settings row shape
// (8/16 padding, at least 48 high): label and help on the left, the track
// on the right; a click anywhere on the row toggles it.
func SwitchRow(c *ui.Context, on *bool, label, help string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.SwitchBase(c, on).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Label(label).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, label).FontWeight(500)
			if help != "" {
				Text(c, help, t.Text.Sm).TextColor(t.Muted)
			}
		})
		switchTrack(c, *on)
	})
}

// Checkbox is a 16×16 check box before its label.
func Checkbox(c *ui.Context, on *bool, label string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.CheckboxBase(c, on).Gap(th.Space(2)).Children(func() {
		box := ui.Box(c).Size(theme.Control, theme.Control).Radius(t.Radius.Sm).Border(theme.Hairline, t.Input).Background(t.Background).Center()
		Elevate(c, box, theme.ShadowXs)
		if *on {
			box.Background(t.Primary).Border(theme.Hairline, t.Primary).TextColor(t.PrimaryText).Children(func() {
				ui.Icon(c, icons.Get("check")).Size(theme.IconXs, theme.IconXs)
			})
		}
		ui.Text(c, label)
	})
}

// Radio is a radio button choosing v into *sel.
func Radio[T comparable](c *ui.Context, sel *T, v T, label string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.RadioBase(c, sel, v).Gap(th.Space(2)).Children(func() {
		dot := ui.Box(c).Size(theme.Control, theme.Control).Radius(t.Radius.Full).Border(theme.Hairline, t.Input).Background(t.Background).Center()
		Elevate(c, dot, theme.ShadowXs)
		if *sel == v {
			dot.Border(theme.Hairline, t.Primary).Children(func() { ui.Box(c).Size(theme.RadioDot, theme.RadioDot).Radius(t.Radius.Full).Background(t.Primary) })
		}
		ui.Text(c, label)
	})
}

// Option is one choice of a Select.
type Option[T comparable] struct {
	Value T
	Label string
}

// SelectOpts style a SelectWith: zero Radius is Md, zero MinWidth the
// default, zero MaxWidth unbounded; Flat drops the shadow.
type SelectOpts struct {
	Radius, MinWidth, MaxWidth float32
	Flat                       bool
}

// Select is a 32-high select of opts; give it a Label.
func Select[T comparable](c *ui.Context, sel *T, opts []Option[T]) ui.Element {
	return SelectWith(c, SelectOpts{}, sel, opts)
}

// SelectWith is Select styled by o.
func SelectWith[T comparable](c *ui.Context, o SelectOpts, sel *T, opts []Option[T]) ui.Element {
	t, th := theme.Of(c), c.Theme()
	s := ui.SelectBase(c, sel)
	current := ""
	for _, o := range opts {
		if o.Value == *sel {
			current = o.Label
		}
	}
	if !o.Flat {
		Elevate(c, s.Trigger, theme.ShadowXs)
	}
	radius, minW := o.Radius, o.MinWidth
	if radius == 0 {
		radius = t.Radius.Md
	}
	if minW == 0 {
		minW = theme.SelectMinWidth
	}
	s.Trigger.Height(th.Space(8)).PaddingX(th.Space(2.5)).Gap(th.Space(2)).Radius(radius).Border(theme.Hairline, t.Input).Background(t.Background).MinWidth(minW).Children(func() {
		ui.Text(c, current).Grow(1)
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
	})
	if o.MaxWidth > 0 {
		s.Trigger.MaxWidth(o.MaxWidth)
	}
	s.Popup(func(panel ui.Element) {
		panel.Margin(theme.PopupOffset, 0, 0, 0).Padding(th.Space(1)).Radius(t.Radius.Lg).Background(t.Background).Border(theme.Hairline, t.Border)
		Elevate(c, panel, theme.ShadowMd)
		for _, o := range opts {
			item := s.Item(o.Value).Height(th.Space(8)).PaddingX(th.Space(2)).Gap(th.Space(2)).Radius(t.Radius.Md)
			if item.Highlighted() {
				item.Background(t.Hover)
			}
			item.Children(func() {
				ui.Text(c, o.Label).Grow(1)
				check := ui.Icon(c, icons.Get("check")).Size(theme.Icon, theme.Icon)
				if o.Value != *sel {
					check.Opacity(0)
				}
			})
		}
	})
	return s.Trigger
}

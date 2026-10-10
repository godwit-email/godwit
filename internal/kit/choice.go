package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// switchTrack draws a switch: a 32×18 track and its thumb.
func switchTrack(c *ui.Context, on bool) ui.Element {
	t := theme.Of(c)
	tr := ui.Row(c).Size(theme.SwitchWidth, theme.SwitchHeight).Shrink(0).Padding(theme.SwitchInset).Radius(t.Radius.Full).Background(t.Border).Justify(ui.Start)
	thumb := t.Background
	switch {
	case on:
		tr.Background(t.Primary).Justify(ui.End)
	case t.Dark:
		thumb = t.Foreground // a dark thumb would vanish into the dark track
	}
	return tr.Children(func() {
		ui.Box(c).Size(theme.SwitchThumb, theme.SwitchThumb).Radius(t.Radius.Full).Background(thumb).Shadow(0, theme.ShadowSm.Y, theme.ShadowSm.Blur, 0, ink(t).Alpha(0.2))
	})
}

// Switch is a 32×18 switch; give it a Label.
func Switch(c *ui.Context, on *bool) ui.Element {
	return ui.SwitchBase(c, on).Radius(theme.Of(c).Radius.Full).Children(func() { switchTrack(c, *on) })
}

// SwitchRow is a full-width switch named label: label and help on the left,
// the track on the right; a click anywhere on the row toggles it.
func SwitchRow(c *ui.Context, on *bool, label, help string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.SwitchBase(c, on).FillWidth().Gap(th.Space(4)).Label(label).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, label)
			if help != "" {
				ui.Text(c, help).FontSize(t.Text.Sm).TextColor(t.Muted)
			}
		})
		switchTrack(c, *on)
	})
}

// Checkbox is a 16×16 check box before its label.
func Checkbox(c *ui.Context, on *bool, label string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.CheckboxBase(c, on).Gap(th.Space(2)).Children(func() {
		box := ui.Box(c).Size(theme.Control, theme.Control).Radius(t.Radius.Xs).Border(theme.Hairline, t.Border).Background(t.Background).Center()
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
		dot := ui.Box(c).Size(theme.Control, theme.Control).Radius(t.Radius.Full).Border(theme.Hairline, t.Border).Background(t.Background).Center()
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

// Select is a 32-high select of opts; give it a Label.
func Select[T comparable](c *ui.Context, sel *T, opts []Option[T]) ui.Element {
	t, th := theme.Of(c), c.Theme()
	s := ui.SelectBase(c, sel)
	current := ""
	for _, o := range opts {
		if o.Value == *sel {
			current = o.Label
		}
	}
	s.Trigger.Height(th.Space(8)).PaddingX(th.Space(2.5)).Gap(th.Space(2)).Radius(t.Radius.Sm).Border(theme.Hairline, t.Border).Background(t.Background).MinWidth(theme.SelectMinWidth).Children(func() {
		ui.Text(c, current).Grow(1)
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
	})
	s.Popup(func(panel ui.Element) {
		panel.Margin(theme.PopupOffset, 0, 0, 0).Padding(th.Space(1)).Radius(t.Radius.Md).Background(t.Background).Border(theme.Hairline, t.Border).
			Shadow(0, theme.ShadowMd.Y, theme.ShadowMd.Blur, 0, ink(t).Alpha(0.12))
		for _, o := range opts {
			item := s.Item(o.Value).Height(th.Space(8)).PaddingX(th.Space(2)).Gap(th.Space(2)).Radius(t.Radius.Sm)
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

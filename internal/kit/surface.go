package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// SidebarItemOpts says how a sidebar row looks and what clicking it does.
type SidebarItemOpts struct {
	Selected bool    // the page shown: highlighted, and it does not click
	Muted    bool    // a secondary action, as Add account
	Small    bool    // 30 high in the small text, as an account's header
	Indent   float32 // more left padding, in spacing steps, as a folder under its account
	OnClick  func()
}

// SidebarItem is a row of a page's sidebar, its children left-aligned: 32
// high, highlighted while selected or hovered. Give it its Children.
func SidebarItem(c *ui.Context, o SidebarItemOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Height(th.Space(8)).Padding(0, th.Space(2), 0, th.Space(2+o.Indent)).Gap(th.Space(2)).Radius(t.Radius.Lg).Justify(ui.Start).Transition(colors(theme.MotionFast))
	if o.Small {
		b.Height(th.Space(7.5)).Gap(th.Space(1.5)).FontSize(t.Text.Sm)
	}
	if o.Muted {
		b.TextColor(t.Muted)
	}
	switch {
	case o.Selected && b.Hovered():
		b.Background(t.HoverTint.Alpha(2.25))
	case o.Selected:
		b.Background(t.HoverTint.Alpha(1.75))
	case b.Hovered():
		b.Background(t.HoverTint)
	}
	if o.OnClick != nil && !o.Selected {
		b.OnClick(o.OnClick)
	}
	return b
}

// SidebarIcon is a SidebarItem's leading icon: muted line style until item is
// hovered; selected, the foreground fill style.
func SidebarIcon(c *ui.Context, item ui.Element, icon string, selected bool) {
	t := theme.Of(c)
	svg, color := icons.Get(icon), t.Muted
	if selected {
		svg, color = icons.Fill(icon), t.Foreground
	} else if item.Hovered() {
		color = t.Foreground
	}
	ui.Icon(c, svg).Size(theme.Icon, theme.Icon).TextColor(color)
}

// Card is a bordered panel.
func Card(c *ui.Context, body func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	card := ui.Column(c).Gap(th.Space(3)).Padding(th.Space(4)).Radius(t.Radius.Xl).Background(t.Background).Border(theme.Hairline, t.Border)
	return Elevate(c, card, theme.ShadowSm).Children(body)
}

// PressableOpts say how a Pressable looks and what clicking it does.
type PressableOpts struct {
	Selected, Bordered bool
	Glass              bool // frosted glass appearance with hairline border and GlassHover (no blur)
	Bare               bool // no hover or selected background: the caller draws its own
	OnClick            func()
}

// Pressable is a clickable surface for content the caller builds, left-aligned.
// Give it its Label, Padding, Gap and Children. A selected one does not click.
func Pressable(c *ui.Context, o PressableOpts) ui.Element {
	t := theme.Of(c)
	b := ui.ButtonBase(c).Justify(ui.Start).Radius(t.Radius.Lg).Transition(colors(theme.MotionFast))
	if o.Glass {
		tint := t.GlassBg
		if b.Hovered() {
			tint = t.GlassHover
		}
		b.Radius(t.Radius.Lg).Clip().
			Background(tint).
			Border(theme.Hairline, t.GlassBorder)
		Elevate(c, b, theme.ShadowXs)
	} else if o.Bordered {
		b.Border(theme.Stroke, t.Border).Background(t.Background)
	}
	if !o.Bare && !o.Glass {
		switch {
		case o.Selected:
			b.Background(t.SidebarSelected)
		case b.Hovered():
			b.Background(t.Hover)
		}
	}
	if o.OnClick != nil && !o.Selected {
		b.OnClick(o.OnClick)
	}
	return b
}

// NavItem is a page a NavSwitch goes to.
type NavItem struct {
	Icon, Label string
	Selected    bool // the page shown: raised, and it does not click
	OnClick     func()
}

// navPill is the raised background of the page a NavSwitch shows: one
// identity, so it slides from the page left to the page picked.
type navPill struct{}

// NavSwitch is a segmented row of page links on the window material: the
// page shown raised on the background in the fill icon with its name, the
// others muted icons named by a tooltip. Picking another slides the raised
// background over and widens it to the name as it comes in.
func NavSwitch(c *ui.Context, items ...NavItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	move := ui.ElementTransition{Duration: theme.MotionBase}
	return ui.Row(c).Padding(th.Space(0.5)).Gap(th.Space(0.5)).Radius(t.Radius.Lg).Background(t.HoverTint).Clip().Children(func() { // the pill stays in the track as both resize
		for _, it := range items {
			b := ui.ButtonBase(c).Height(th.Space(7)).Radius(t.Radius.Md).Center().
				FontSize(t.Text.Base).TextColor(t.Muted).Label(it.Label).Transition(move) // its width and place, as names come and go
			svg := icons.Get(it.Icon)
			switch {
			case it.Selected:
				b.TextColor(t.Foreground).Padding(0, th.Space(2.5), 0, th.Space(1.5))
				svg = icons.Fill(it.Icon)
			default:
				b.Width(th.Space(7))
				if b.Hovered() {
					b.TextColor(t.Foreground)
				}
				Tooltip(c, b, it.Label)
			}
			if it.OnClick != nil && !it.Selected {
				b.OnClick(it.OnClick)
			}
			content := func(e ui.Element) {
				e.Children(func() {
					ui.Icon(c, svg).Size(theme.Icon, theme.Icon)
					if it.Selected {
						ui.Text(c, it.Label).SingleLine().Transition(ui.ElementTransition{Duration: theme.MotionBase, Enter: &ui.Motion{}}) // fades in
					}
				})
			}
			b.Children(func() {
				// Absolute elements paint over their siblings, the later over the earlier: the
				// button is sized by its content unseen, then the pill, then the content over it.
				content(ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Opacity(0))
				if it.Selected {
					pill := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Radius(t.Radius.Md).Background(t.Background).PassThrough()
					Elevate(c, pill, theme.ShadowXs).Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: navPill{}})
				}
				content(ui.Row(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Gap(th.Space(1.5)).Clip().PassThrough()) // clipped while the button widens
			})
		}
	})
}

// Panel is a region of a page on the window material, as the mail list or
// the reading pane: a white card in the border's hairline with ShadowXs,
// corners of radius. Give it its size and Children.
func Panel(c *ui.Context, radius float32) ui.Element {
	t := theme.Of(c)
	return Elevate(c, ui.Column(c).Radius(radius).Clip().Background(t.Background).Border(theme.Hairline, t.Border), theme.ShadowXs)
}

// CardHeader is a Card's title with an optional description under it.
func CardHeader(c *ui.Context, title, description string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1)).Children(func() {
		Text(c, title, t.Text.Lg).FontWeight(600)
		if description != "" {
			Text(c, description, t.Text.Sm).TextColor(t.Muted)
		}
	})
}

// CardFooter is a right-aligned row for a Card's actions.
func CardFooter(c *ui.Context, children func()) {
	ui.Row(c).Justify(ui.End).Gap(c.Theme().Space(2)).Children(children)
}

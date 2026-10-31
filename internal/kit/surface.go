package kit

import (
	"slices"

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

// navContent is the icon and name of a NavSwitch's i-th page, which slide
// from where they were as the pill does.
type navContent int

// NavSwitch is a segmented row of page links on the window material: the
// page shown raised on the background in the fill icon with its name, the
// others muted icons named by a tooltip. Picking another slides the raised
// background over and widens it to the name, which it uncovers.
//
// The buttons take their new widths at once, so that what slides heads for
// where it ends: a pill chasing a button still on its way overshoots it. The
// icons and names are a row of their own over the buttons, as an absolute
// element paints over its siblings and the pill would cover those before it.
func NavSwitch(c *ui.Context, items ...NavItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	shown := slices.IndexFunc(items, func(it NavItem) bool { return it.Selected })
	hovered := make([]bool, len(items))
	// segment sizes a page's button and its icon and name alike, the icon where
	// it is with or without the name.
	segment := func(e ui.Element, it NavItem) ui.Element {
		e.Height(th.Space(7)).AlignItems(ui.Center).Gap(th.Space(1.5)).Padding(0, 0, 0, th.Space(1.5))
		if it.Selected {
			return e.Padding(0, th.Space(2.5), 0, th.Space(1.5))
		}
		return e.Width(th.Space(7))
	}
	content := func(e ui.Element, it NavItem) {
		svg := icons.Get(it.Icon)
		if it.Selected {
			svg = icons.Fill(it.Icon)
		}
		e.Children(func() {
			ui.Icon(c, svg).Size(theme.Icon, theme.Icon).PassThrough() // the buttons under them take the pointer
			if it.Selected {
				ui.Text(c, it.Label).SingleLine().Shrink(0).PassThrough()
			}
		})
	}
	track := func(e ui.Element) ui.Element { return e.Padding(th.Space(0.5)).Gap(th.Space(0.5)) }
	return ui.Box(c).Radius(t.Radius.Lg).Background(t.HoverTint).Clip().
		Transition(ui.ElementTransition{Duration: theme.MotionBase, Size: true}).Children(func() {
		track(ui.Row(c)).Children(func() {
			for i, it := range items {
				b := segment(ui.ButtonBase(c).Radius(t.Radius.Md).Label(it.Label), it)
				hovered[i] = b.Hovered()
				if !it.Selected {
					Tooltip(c, b, it.Label)
					if it.OnClick != nil {
						b.OnClick(it.OnClick)
					}
				}
				b.Children(func() {
					content(ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Opacity(0), it) // sizes the button unseen
					if it.Selected {
						pill := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Radius(t.Radius.Md).Background(t.Background).PassThrough()
						Elevate(c, pill, theme.ShadowXs).Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: navPill{}})
					}
				})
			}
		})
		track(ui.Row(c).Absolute().Left(0).Top(0).Bottom(0)).PassThrough().Children(func() {
			for i, it := range items {
				// Keyed by the page shown, so that a new one appears and slides from where its
				// Match was, clipping the name as it widens.
				e := segment(ui.Row(c).Key([2]int{shown, i}), it).FontSize(t.Text.Base).TextColor(t.Muted).Clip().PassThrough().
					Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: navContent(i)})
				if it.Selected || hovered[i] {
					e.TextColor(t.Foreground)
				}
				content(e, it)
			}
		})
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

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
	b := ui.ButtonBase(c).Height(th.Space(8)).Padding(0, th.Space(2), 0, th.Space(2+o.Indent)).Gap(th.Space(2)).Radius(t.Radius.Md).Justify(ui.Start).Transition(colors(theme.MotionFast))
	if o.Small {
		b.Height(th.Space(7.5)).Gap(th.Space(1.5)).FontSize(t.Text.Sm)
	}
	if o.Muted {
		b.TextColor(t.Muted)
	}
	switch {
	case o.Selected:
		b.Background(t.SidebarSelected).FontWeight(500)
	case b.Hovered():
		b.Background(t.Hover)
	}
	if o.OnClick != nil && !o.Selected {
		b.OnClick(o.OnClick)
	}
	return b
}

// Card is a bordered panel.
func Card(c *ui.Context, body func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Column(c).Gap(th.Space(3)).Padding(th.Space(4)).Radius(t.Radius.Panel).Background(t.Background).Border(theme.Hairline, t.Border).Children(body)
}

// PressableOpts say how a Pressable looks and what clicking it does.
type PressableOpts struct {
	Selected, Bordered bool
	Bare               bool // no hover or selected background: the caller draws its own
	OnClick            func()
}

// Pressable is a clickable surface for content the caller builds, left-aligned.
// Give it its Label, Padding, Gap and Children. A selected one does not click.
func Pressable(c *ui.Context, o PressableOpts) ui.Element {
	t := theme.Of(c)
	b := ui.ButtonBase(c).Justify(ui.Start).Radius(t.Radius.Md).Transition(colors(theme.MotionFast))
	if o.Bordered {
		b.Border(theme.Hairline, t.Border).Background(t.Background)
	}
	if !o.Bare {
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

// RailButton is a square icon button of the icon rail; selected, it does not click.
func RailButton(c *ui.Context, icon, label string, selected bool, onClick func()) ui.Element {
	t := theme.Of(c)
	b := ui.ButtonBase(c).Size(theme.RailButton, theme.RailButton).Radius(t.Radius.Md).Center().Label(label).Transition(colors(theme.MotionFast))
	tooltip(c, b, label, false)
	b.TextColor(t.Muted)
	switch {
	case selected:
		b.Background(t.RailSelected).TextColor(t.Foreground)
	case b.Hovered():
		b.Background(t.HoverTint).TextColor(t.Foreground)
	}
	if onClick != nil && !selected {
		b.OnClick(onClick)
	}
	return b.Children(func() { ui.Icon(c, icons.Get(icon)).Size(theme.IconLg, theme.IconLg) })
}

// CardHeader is a Card's title with an optional description under it.
func CardHeader(c *ui.Context, title, description string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1)).Children(func() {
		ui.Text(c, title).FontSize(t.Text.Lg).FontWeight(600)
		if description != "" {
			ui.Text(c, description).FontSize(t.Text.Sm).TextColor(t.Muted)
		}
	})
}

// CardFooter is a right-aligned row for a Card's actions.
func CardFooter(c *ui.Context, children func()) {
	ui.Row(c).Justify(ui.End).Gap(c.Theme().Space(2)).Children(children)
}

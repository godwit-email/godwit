package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// SidebarItemOpts says how a sidebar row looks and what clicking it does.
type SidebarItemOpts struct {
	Selected bool    // the page shown: highlighted, and it does not click
	Covered  bool    // selected, but something covers the page: it clicks, to show it
	Muted    bool    // a secondary action, as Add account; a group's title, in small muted text
	Small    bool    // 28 high, as a nested row or compact footer action (keeps default text size)
	Indent   float32 // more left padding, in spacing steps, as a folder under its account
	Open     *bool   // a group's fold, which must outlive the frame
	Title    func()  // a group's title: its content, before its Chevron and Count
	Group    func()  // the rows a group folds: set, the row is the group's title, and Title and Open are too
	Count    string  // at a group title's end, in the column of its rows' counts
	OnClick  func()
}

// SidebarItem is a row of a page's sidebar, its children left-aligned: 32
// high, highlighted while selected or hovered. Give it its Children, unless
// it is a group's title: it folds the rows of Group by *Open, shows Title, a
// Chevron and Count, and is the row to give a ContextMenu.
func SidebarItem(c *ui.Context, o SidebarItemOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	var group ui.CollapsibleParts
	var b ui.Element
	if o.Group != nil {
		group = ui.CollapsibleBase(c, o.Open)
		b = group.Trigger
	} else {
		b = ui.ButtonBase(c)
	}
	b.Height(th.Space(8)).Padding(0, th.Space(2), 0, th.Space(2+o.Indent)).Gap(th.Space(2)).Radius(t.Radius.Lg).Justify(ui.Start).Transition(colors(theme.MotionFast))
	if o.Small {
		b.Height(th.Space(7))
	}
	if o.Muted {
		b.TextColor(t.Muted)
		if o.Group != nil {
			b.Height(th.Space(6)).Gap(th.Space(1)).FontSize(t.Text.Xs)
		}
	}
	switch {
	case o.Selected && b.Hovered():
		b.Background(tint(t, 2.25))
	case o.Selected:
		b.Background(tint(t, 1.75))
	case b.Hovered():
		b.Background(tint(t, 1))
	}
	if o.OnClick != nil && (!o.Selected || o.Covered) {
		b.OnClick(o.OnClick)
	}
	if o.Group != nil {
		b.Children(func() {
			o.Title()
			Chevron(c, *o.Open, theme.IconXs)
			if o.Count != "" {
				ui.Spacer(c)
				Text(c, o.Count, t.Text.Xs).TextColor(t.Muted)
			}
		})
		group.Panel(func() { // the sidebar's own gap, above and between the rows
			ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(0.5), 0, 0, 0).Children(o.Group)
		})
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

// Tone is the fill of a Block or a Card.
type Tone uint8

const (
	ToneCard    Tone = iota // the card gray: a region apart from the page around it
	ToneTint                // the hover tint: one that stands out more, as a note or a group being edited
	ToneOutline             // the background in a hairline: a region on the page's own white
	ToneRaised              // the background, bare: a white block on a card
	ToneWarning             // the status tints, each its -bg in a hairline of its -border under its -text: a note that warns,
	ToneDanger              // one that says what failed,
	ToneSuccess             // and one that says what went well
	ToneInfo                // blue, a note to read before acting (a draft a program wrote): the blue label's fill halfway to the background in a hairline of its edge under its text
)

// Block makes e a region inside a page or a card and returns it: tone's
// fill at radius-lg. Give it its Padding, Gap and Children; a page rounds
// and fills no box of its own.
func Block(c *ui.Context, e ui.Element, tone Tone) ui.Element {
	t := theme.Of(c)
	return toned(e.Radius(t.Radius.Lg), t, tone, false)
}

// Card makes e a section of a page and returns it: tone's fill at
// radius-xl, the card gray in a hairline too.
func Card(c *ui.Context, e ui.Element, tone Tone) ui.Element {
	t := theme.Of(c)
	return toned(e.Radius(t.Radius.Xl), t, tone, true)
}

// toned fills e as tone; edged also outlines the card gray.
func toned(e ui.Element, t theme.Tokens, tone Tone, edged bool) ui.Element {
	switch tone {
	case ToneCard:
		e.Background(t.Card)
		if edged {
			e.Border(theme.Hairline, t.Border)
		}
	case ToneTint:
		e.Background(tint(t, 1))
	case ToneOutline:
		e.Background(t.Background).Border(theme.Hairline, t.Border)
	case ToneRaised:
		e.Background(t.Background)
	default:
		if bg, border, text, ok := toneColors(t, tone); ok {
			e.Background(bg).Border(theme.Hairline, border).TextColor(text)
		}
	}
	return e
}

// toneColors are a colored tone's fill, outline and text: what a Block of it
// draws, and what a Button on it (ButtonOpts.Tone) follows. ok is false for
// the neutral tones.
func toneColors(t theme.Tokens, tone Tone) (bg, border, text ui.Color, ok bool) {
	switch tone {
	case ToneWarning:
		return t.WarningBg, t.WarningBorder, t.WarningText, true
	case ToneDanger:
		return t.DangerBg, t.DangerBorder, t.DangerText, true
	case ToneSuccess:
		return t.SuccessBg, t.SuccessBorder, t.SuccessText, true
	case ToneInfo:
		return t.InfoBg, t.InfoBorder, t.InfoText, true
	}
	return // neutral: no colors of its own
}

// PressableOpts say how a Pressable looks and what clicking it does.
type PressableOpts struct {
	Selected bool
	Glass    bool // frosted glass appearance with hairline border and GlassHover (no blur)
	Bare     bool // no hover or selected background: the caller draws its own
	OnClick  func()
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
	}
	if !o.Bare && !o.Glass {
		switch {
		case o.Selected: // tints, as a SidebarItem's: they show on a card too
			b.Background(tint(t, 1.75))
		case b.Hovered():
			b.Background(tint(t, 1))
		}
	}
	if o.OnClick != nil && !o.Selected {
		b.OnClick(o.OnClick)
	}
	return b
}

// Panel is a region of a page on the window material, as the mail list or
// the reading pane: a white card in the border's hairline with ShadowXs,
// corners of radius. Give it its size and Children.
func Panel(c *ui.Context, radius float32) ui.Element {
	t := theme.Of(c)
	return Elevate(c, ui.Column(c).Radius(radius).Clip().Background(t.Background).Border(theme.Hairline, t.Border), theme.ShadowXs)
}

// FloatCard gives e the floating composer surface: 16 round translucent frosted
// glass (MaterialBar over blurred content), ShadowFloat in either appearance
// plus FloatInset's inner edge in the dark.
func FloatCard(c *ui.Context, e ui.Element) ui.Element {
	t := theme.Of(c)
	e.Radius(t.Radius.X2l).Material(Bar(t.MaterialBar))
	Elevate(c, e, theme.ShadowFloat)
	if t.Dark {
		e.InsetShadow(0, 0, theme.FloatInsetBlur, 0, t.FloatInset)
	}
	return e
}

// CardHeader is a Card's title with an optional description under it.
func CardHeader(c *ui.Context, title, description string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1)).Children(func() {
		Text(c, title, t.Text.Base).FontWeight(600)
		if description != "" {
			Text(c, description, t.Text.Xs).TextColor(t.Muted)
		}
	})
}

// CardFooter is a right-aligned row for a Card's actions.
func CardFooter(c *ui.Context, children func()) {
	ui.Row(c).Justify(ui.End).Gap(c.Theme().Space(2)).Children(children)
}

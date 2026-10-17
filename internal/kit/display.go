package kit

import (
	"hash/fnv"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Text is text at a step of the text scale with that step's line height.
// Leaves only: a line height passes down as a multiplier, so on a container
// it would reach texts of other sizes.
func Text(c *ui.Context, s string, size float32) ui.Element {
	return ui.Text(c, s).FontSize(size).LineHeight(theme.Of(c).Text.Line(size))
}

// Badge is a small gray label.
func Badge(c *ui.Context, text string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Box(c).Padding(th.Space(0.5), th.Space(2)).Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
		Text(c, text, t.Text.Sm).TextColor(t.Foreground)
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
	t, th := theme.Of(c), c.Theme()
	bg, border, fg := alertLook(t, kind)
	return ui.Row(c).AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Xl).Background(bg).Border(theme.Hairline, border).TextColor(fg).
		FontSize(t.Text.Body).Children(func() {
		ui.Text(c, text).Grow(1).Shrink(1) // the text wraps; the actions stay on its right
		if actions != nil {
			actions()
		}
	})
}

func alertLook(t theme.Tokens, kind AlertKind) (bg, border, fg ui.Color) {
	switch kind {
	case Warning:
		return t.WarningBg, t.WarningBorder, t.WarningText
	case Danger:
		return t.DangerBg, t.Danger.Alpha(0.3), t.Danger
	}
	return t.Background, t.Border, t.Foreground
}

// AlertAction is a solid button for an Alert's actions: filled with the
// kind's text color and white text (the kind's background in dark) (Info: Primary with PrimaryText).
func AlertAction(c *ui.Context, kind AlertKind, label string, onClick func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	bg, _, fill := alertLook(t, kind)
	text := t.OnSolid
	if t.Dark { // the dark fill is the light -300 text colour: the dark -950 background reads on it
		text = bg
	}
	if kind == Info { // Foreground flips with the appearance; Primary pairs with PrimaryText
		fill, text = t.Primary, t.PrimaryText
	}
	b := ui.ButtonBase(c).Label(label).Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).Background(fill).TextColor(text).FontWeight(500).Center()
	if b.Hovered() {
		b.Background(fill.Mix(t.Background, 0.1))
	}
	if onClick != nil {
		b.OnClick(onClick)
	}
	return b.Children(func() { ui.Text(c, label) })
}

// Status is what a StatusDot tells.
type Status int

const (
	OK Status = iota
	Attention
	Error
)

// StatusDot is a 6×6 dot: green OK, amber Attention (clears by itself), red Error (needs the user).
func StatusDot(c *ui.Context, s Status) ui.Element {
	t := theme.Of(c)
	color := t.Success
	switch s {
	case Attention:
		color = t.Attention
	case Error:
		color = t.Danger
	}
	return ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Radius(t.Radius.Full).Background(color)
}

// Spinner shows that something is under way.
func Spinner(c *ui.Context) ui.Element {
	return ui.Spinner(c).TextColor(theme.Of(c).Muted)
}

// Kbd shows a key or shortcut.
func Kbd(c *ui.Context, text string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Box(c).Height(th.Space(5)).PaddingX(th.Space(1.25)).Radius(t.Radius.Sm).Border(theme.Hairline, t.Border).Background(t.Sidebar).Center().Children(func() {
		ui.Text(c, text).Font(theme.Mono).FontSize(t.Text.Xs).LineHeight(theme.LineTight).TextColor(t.Muted)
	})
}

// initial is name's first rune upper-cased, "?" when name is empty.
func initial(name string) string {
	r, _ := utf8.DecodeRuneInString(name)
	if name == "" {
		return "?"
	}
	return string(unicode.ToUpper(r))
}

// Avatar is a rounded square with the initial of name, labelled name, in a
// label color the name picks, so each sender keeps theirs.
func Avatar(c *ui.Context, name string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	col := avatarColor(t, name)
	return ui.Box(c).Size(th.Space(8), th.Space(8)).Shrink(0).Radius(t.Radius.Lg).Background(col.Fill).Center().Label(name).Children(func() {
		ui.Text(c, initial(name)).FontWeight(600).FontSize(t.Text.Base).LineHeight(theme.LineTight).TextColor(col.Text)
	})
}

// avatarColor is one of the label colors but gray, by a hash of name.
func avatarColor(t theme.Tokens, name string) theme.LabelColor {
	h := fnv.New32a()
	h.Write([]byte(name))
	colors := t.Labels[1:]
	return colors[h.Sum32()%uint32(len(colors))]
}

// AvatarImage shows an image in its own colors, with the same labelled
// square and size as the initial placeholder.
func AvatarImage(c *ui.Context, name string, image ui.ImageSource) ui.Element {
	if image == nil {
		return Avatar(c, name)
	}
	t, th := theme.Of(c), c.Theme()
	return ui.Box(c).Size(th.Space(8), th.Space(8)).Shrink(0).Radius(t.Radius.Lg).Clip().Label(name).Children(func() {
		ui.Image(c, image).Size(th.Space(8), th.Space(8)).Fit(ui.Cover)
	})
}

// Separator is a hairline: horizontal across its parent, or a short vertical
// one. In a row, give it Grow(1).
func Separator(c *ui.Context, vertical bool) ui.Element {
	b := ui.Box(c).Background(theme.Of(c).Border)
	if vertical {
		return b.Size(theme.Hairline, theme.TitleButtonHeight)
	}
	return b.Height(theme.Hairline).AlignSelf(ui.Stretch)
}

// Skeleton is a pulsing placeholder w wide (0: the full width) and h high. It
// stays still under ReduceMotion, which Loop does not honor itself.
func Skeleton(c *ui.Context, w, h float32) ui.Element {
	t := theme.Of(c)
	b := ui.Box(c).Height(h).Radius(t.Radius.Md).Background(t.Hover)
	if w == 0 {
		b.FillWidth()
	} else {
		b.Width(w)
	}
	if !c.Preferences().ReduceMotion {
		b.Opacity(0.5 + 0.5*b.Loop("pulse", theme.SkeletonPulse, ui.Bounce(ui.EaseInOut)))
	}
	return b
}

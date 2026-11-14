package kit

import (
	"hash/fnv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// Text is text at a step of the text scale, on the interface's line
// (theme.LineUI, which every text inherits).
func Text(c *ui.Context, s string, size float32) ui.Element {
	return ui.Text(c, s).FontSize(size)
}

// BadgeTone is a badge's colors.
type BadgeTone uint8

const (
	BadgeNeutral BadgeTone = iota // the hover gray: a kind, a count, a tag
	BadgeOutline                  // a hairline and no fill: a state of the row it is in
	BadgeSurface                  // the background, for a badge on a card or a tinted row
	BadgeSuccess                  // the status tints: their -bg under their -text
	BadgeWarning
	BadgeDanger
)

// BadgeOpts style a BadgeWith: Icon is a 12 icon before the text, named
// IconLabel to assistive technology when it says what the text does not;
// Muted is its text in the muted color (the status tones keep theirs), Mono
// the monospace font.
type BadgeOpts struct {
	Tone            BadgeTone
	Icon, IconLabel string
	Muted, Mono     bool
}

// Badge is a small gray label.
func Badge(c *ui.Context, text string) ui.Element { return BadgeWith(c, text, BadgeOpts{}) }

// BadgeWith is a badge styled by o, the one small label of the interface: as
// high as a label chip (20), xs text, 6 at its sides, radius-sm. It names
// and does not act: one that is clicked is a BadgeButton.
func BadgeWith(c *ui.Context, text string, o BadgeOpts) ui.Element {
	return badge(c, ui.Row(c), text, o)
}

// BadgeButton is a badge that acts, at a badge's size so that it sits in a
// row of them, as 标签 and 退订 beside a mail's label chips: neutral or
// outline, a step darker hovered and another pressed. Give it its OnClick or
// its Menu; elsewhere what acts is a Button.
func BadgeButton(c *ui.Context, text string, o BadgeOpts) ui.Element {
	t := theme.Of(c)
	b := badge(c, ui.ButtonBase(c).Label(text), text, o).Transition(colors(theme.MotionFast))
	rest := float32(1) // an outline one has no fill at rest
	if o.Tone == BadgeOutline {
		rest = 0
	}
	switch {
	case b.Pressed():
		b.Background(tint(t, rest+2))
	case b.Hovered():
		b.Background(tint(t, rest+1))
	}
	return b
}

// badge gives e a badge's box and content.
func badge(c *ui.Context, e ui.Element, text string, o BadgeOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := e.AlignItems(ui.Center).Gap(th.Space(1)).Height(theme.LabelChip).PaddingX(th.Space(1.5)).Radius(t.Radius.Sm).TextColor(t.Foreground)
	if o.Muted {
		b.TextColor(t.Muted)
	}
	switch o.Tone {
	case BadgeNeutral:
		b.Background(tint(t, 1))
	case BadgeOutline:
		b.Border(theme.Hairline, t.Border)
	case BadgeSurface:
		b.Background(t.Background)
	case BadgeSuccess:
		b.Background(t.SuccessBg).TextColor(t.SuccessText)
	case BadgeWarning:
		b.Background(t.WarningBg).TextColor(t.WarningText)
	case BadgeDanger:
		b.Background(t.DangerBg).TextColor(t.DangerText)
	}
	return b.Children(func() {
		if o.Icon != "" {
			if i := ui.Icon(c, icons.Get(o.Icon)).Size(theme.IconXs, theme.IconXs); o.IconLabel != "" {
				i.Label(o.IconLabel)
			}
		}
		label := Text(c, text, t.Text.Xs).LineHeight(theme.LineTight).SingleLine()
		if o.Mono {
			label.Font(theme.Mono)
		}
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
	tone := ToneOutline
	switch kind {
	case Warning:
		tone = ToneWarning
	case Danger:
		tone = ToneDanger
	}
	return Block(c, ui.Row(c).AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(2), th.Space(2.5)), tone).
		FontSize(t.Text.Sm).Children(func() {
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
		return t.DangerBg, t.DangerBorder, t.DangerText
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
	switch { // as a Default button: a step on hover, another pressed
	case b.Pressed():
		b.Background(fill.Mix(t.Background, 0.2))
	case b.Hovered():
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
	Idle // nothing to report yet, as a test under way
)

// StatusDot is a 6×6 dot: green OK, amber Attention (clears by itself), red Error (needs the user), gray Idle.
func StatusDot(c *ui.Context, s Status) ui.Element {
	t := theme.Of(c)
	color := t.Success
	switch s {
	case Attention:
		color = t.Attention
	case Error:
		color = t.Danger
	case Idle:
		color = t.Idle
	}
	return ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Shrink(0).Radius(t.Radius.Full).Background(color)
}

// Spinner shows that something is under way.
func Spinner(c *ui.Context) ui.Element {
	return ui.Spinner(c).TextColor(theme.Of(c).Muted)
}

// Kbd shows a key or shortcut, a keycap each key: ⇧⌘K is three, Esc one.
func Kbd(c *ui.Context, text string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		for _, k := range kbdKeys(text) {
			ui.Box(c).Height(th.Space(5)).MinWidth(th.Space(5)).PaddingX(th.Space(1.5)).Radius(t.Radius.Sm).Border(theme.Hairline, t.Border).Background(t.Sidebar).Center().Children(func() { // a badge's box
				ui.Text(c, k).Font(theme.Mono).FontSize(t.Text.Xs).LineHeight(theme.LineTight).TextColor(t.Muted)
			})
		}
	})
}

// kbdKeys splits a shortcut into its keys: each modifier and arrow is one,
// the rest of the text the last.
func kbdKeys(text string) []string {
	var keys []string
	rest := text
	for len(rest) > 0 {
		r, n := utf8.DecodeRuneInString(rest)
		if !strings.ContainsRune("⌘⇧⌥⌃←→↑↓", r) {
			break
		}
		keys, rest = append(keys, string(r)), rest[n:]
	}
	if rest != "" {
		keys = append(keys, rest)
	}
	return keys
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
	return AvatarSized(c, name, c.Theme().Space(8))
}

// AvatarSized is Avatar size wide, its initial grown in step.
func AvatarSized(c *ui.Context, name string, size float32) ui.Element {
	t, th := theme.Of(c), c.Theme()
	col := avatarColor(t, name)
	return ui.Box(c).Size(size, size).Shrink(0).Radius(t.Radius.Lg).Background(col.Fill).Center().Label(name).Children(func() {
		ui.Text(c, initial(name)).FontWeight(600).FontSize(t.Text.Sm * size / th.Space(8)).LineHeight(theme.LineTight).TextColor(col.Text)
	})
}

// MenuAvatar is Avatar or AvatarImage for a native menu's small icon, which
// the theme doesn't reach: the sender's color solid, to sit on a menu of either
// appearance, and the initial large enough to read at half size.
func MenuAvatar(c *ui.Context, name string, image ui.ImageSource) ui.Element {
	if image != nil {
		return AvatarImage(c, name, image)
	}
	t, th := theme.Of(c), c.Theme()
	return ui.Box(c).Size(th.Space(8), th.Space(8)).Shrink(0).Radius(t.Radius.Lg).Background(avatarColor(t, name).Text).Center().Label(name).Children(func() {
		ui.Text(c, initial(name)).FontWeight(600).FontSize(t.Text.Sm).LineHeight(theme.LineTight).TextColor(t.Background)
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
	// On the mark tile: a logo with a transparent background (a dark favicon, say)
	// would vanish on the dark theme.
	return ui.Box(c).Size(th.Space(8), th.Space(8)).Shrink(0).Radius(t.Radius.Lg).Clip().Background(t.MarkTile).Label(name).Children(func() {
		ui.Image(c, image).Size(th.Space(8), th.Space(8)).Fit(ui.Cover)
	})
}

// Separator is a hairline: horizontal across its parent, or a short vertical
// one. In a row, give it Grow(1). It takes a Stroke's room but draws one
// device pixel, as a border (a box that thin rounds away).
func Separator(c *ui.Context, vertical bool) ui.Element {
	b := ui.Box(c).BorderColor(theme.Of(c).Border)
	if vertical {
		return b.Size(theme.Stroke, theme.TitleButtonHeight).BorderWidth(0, 0, 0, theme.Hairline)
	}
	return b.Height(theme.Stroke).AlignSelf(ui.Stretch).BorderWidth(theme.Hairline, 0, 0, 0)
}

// Skeleton is a pulsing placeholder w wide (0: the full width) and h high. It
// stays still under ReduceMotion, which Loop does not honor itself.
func Skeleton(c *ui.Context, w, h float32) ui.Element {
	t := theme.Of(c)
	b := ui.Box(c).Height(h).Radius(t.Radius.Md).Background(tint(t, 1))
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

// Progress is a bar of how far a job has come, done of total: 4 high, the
// primary filling a track of the border gray, named label.
func Progress(c *ui.Context, done, total float32, label string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).FillWidth().Height(th.Space(1)).Radius(t.Radius.Full).Background(t.Border).AlignItems(ui.Stretch).Clip().Label(label).Children(func() {
		ui.Box(c).Grow(done).Background(t.Primary)
		ui.Box(c).Grow(max(0, total-done))
	})
}

// Tile is a box w by h holding a glyph or a few letters, centered: a
// service's mark, a file's kind, a template's icon. It rounds as a control
// of its shorter side and fills as tone; give it its Children.
func Tile(c *ui.Context, w, h float32, tone Tone) ui.Element {
	return toned(ui.Box(c).Size(w, h).Shrink(0).Center().Radius(controlRadius(c, min(w, h))), theme.Of(c), tone, false)
}

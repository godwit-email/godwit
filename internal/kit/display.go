package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Badge is a small gray label.
func Badge(c *ui.Context, text string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Box(c).Padding(th.Space(0.25), th.Space(1.5)).Radius(t.Radius.Xs).Background(t.Hover).Children(func() {
		ui.Text(c, text).FontSize(t.Text.Sm).TextColor(t.Foreground)
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
	bg, border, fg := t.Background, t.Border, t.Foreground
	switch kind {
	case Warning:
		bg, border, fg = t.WarningBg, t.WarningBorder, t.WarningText
	case Danger:
		bg, border, fg = t.DangerBg, t.Danger.Alpha(0.3), t.Danger
	}
	return ui.Row(c).Wrap().Gap(th.Space(3)).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).Background(bg).Border(theme.Hairline, border).TextColor(fg).
		FontSize(t.Text.Base).Children(func() {
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
	return ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Radius(t.Radius.Full).Background(color)
}

// Spinner shows that something is under way.
func Spinner(c *ui.Context) ui.Element {
	return ui.Spinner(c).TextColor(theme.Of(c).Muted)
}

// Kbd shows a key or shortcut.
func Kbd(c *ui.Context, text string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Box(c).Height(th.Space(5)).PaddingX(th.Space(1.25)).Radius(t.Radius.Xs).Border(theme.Hairline, t.Border).Background(t.Sidebar).Center().Children(func() {
		ui.Text(c, text).Font("Geist Mono").FontSize(t.Text.Xs).TextColor(t.Muted)
	})
}

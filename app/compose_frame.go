package app

import (
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// ComposeFrame is a compose window's native layer around its page: the title
// bar with the draft's subject and save status, which drags the window clear
// of its controls; the page's web view under it; and, over both, the toasts
// the page asks for, above its send bar. Its setters run on any goroutine and
// ask the window to draw again; App.ComposeView draws it on the UI thread.
type ComposeFrame struct {
	view       ui.NativeWebView
	invalidate func()

	mu            sync.Mutex
	title, status string
	toasts        []ui.Toast
	bar, field    ui.Rect // the page's, in its own coordinates
	origin        ui.Rect // where the view is in the window, as last laid out
	later         *laterAsk
	closed        bool // the window closed: send later answers 0
}

// laterAsk is send later as the page asked for it: the dialog's time, and
// where the page waits for the time chosen (0 when cancelled). UI thread
// only, once set.
type laterAsk struct {
	at              time.Time
	open, confirmed bool
	reply           chan int64
}

// NewComposeFrame is the frame of view, which invalidate draws again.
func NewComposeFrame(view ui.NativeWebView, invalidate func()) *ComposeFrame {
	return &ComposeFrame{view: view, invalidate: invalidate}
}

func (f *ComposeFrame) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
	f.invalidate()
}

// SetTitleBar shows title (the subject, or 新邮件) and status (the save
// state) in the title bar.
func (f *ComposeFrame) SetTitleBar(title, status string) {
	f.set(func() { f.title, f.status = title, status })
}

// Toast shows text as a toast of kind: kit.ToastSuccess, ToastWarning,
// ToastError, or "" for a plain one.
func (f *ComposeFrame) Toast(kind, text string) {
	f.set(func() { f.toasts = append(f.toasts, ui.Toast{ID: text, Title: text, Type: kind}) })
}

// SetToastBar places the toasts above the page's bar, level with field's
// right edge; empty rectangles put them back in the window's corner.
func (f *ComposeFrame) SetToastBar(bar, field ui.Rect) {
	f.set(func() { f.bar, f.field = bar, field })
}

// SendLater opens send later at at, unix ms, and waits for the time chosen,
// 0 when cancelled or the window closes. An ask still open is cancelled.
func (f *ComposeFrame) SendLater(at int64) int64 {
	reply := make(chan int64, 1)
	f.set(func() {
		f.dropLater()
		if f.closed {
			reply <- 0
			return
		}
		f.later = &laterAsk{at: time.UnixMilli(at), open: true, reply: reply}
	})
	return <-reply
}

// Close tells f its window closed: a send later still open answers the
// page 0 and goes, and any asked after gets 0 at once.
func (f *ComposeFrame) Close() {
	f.set(func() {
		f.closed = true
		f.dropLater()
	})
}

// dropLater answers the open send later, if any, with 0 and forgets it; f.mu is held.
func (f *ComposeFrame) dropLater() {
	if f.later != nil {
		select {
		case f.later.reply <- 0:
		default:
		}
		f.later = nil
	}
}

// Origin is where the page's view is in the window: page coordinates (a
// menu's) plus it are the window's.
func (f *ComposeFrame) Origin() (x, y int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int(f.origin.X), int(f.origin.Y)
}

// ComposeView draws f: a compose window's title bar on the window's
// material, its page under it, and its toasts over both.
func (a *App) ComposeView(f *ComposeFrame) func(*ui.Context) {
	return func(c *ui.Context) {
		theme.Apply(c, theme.FromSettings(a.settings, c.Theme().Dark))
		kit.NewFrame(c)
		t, th := theme.Of(c), c.Theme()
		f.mu.Lock()
		title, status, toasts, bar, field, later := f.title, f.status, f.toasts, f.bar, f.field, f.later
		f.toasts = nil
		f.mu.Unlock()
		for _, ts := range toasts {
			c.AddToast(ts)
		}
		barH, _ := chromeMetrics(c.TitleBar().Height, t)
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Height(barH).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(th.Space(2)).Background(a.windowFill(t)).
				Padding(0, c.TitleBar().Right+th.Space(4), 0, c.TitleBar().Left+th.Space(5)).DragWindow().Children(func() {
				if splitChrome() {
					ui.Icon(c, logo).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
				}
				kit.Text(c, title, t.Text.Sm).FontWeight(500).SingleLine().Shrink(1)
				kit.Text(c, status, t.Text.Xs).TextColor(t.Muted).SingleLine().Shrink(0)
			})
			view := kit.WebView(c, f.view).Grow(1).FillWidth()
			if b := view.Bounds(); b.W > 0 {
				f.mu.Lock()
				f.origin = b
				f.mu.Unlock()
			}
			if bar.W > 0 && field.W > 0 {
				o := view.Bounds()
				kit.ToastBarRect(c, ui.Rect{X: o.X + bar.X, Y: o.Y + bar.Y, W: bar.W, H: bar.H},
					ui.Rect{X: o.X + field.X, Y: o.Y + field.Y, W: field.W, H: field.H})
			}
		})
		if later != nil {
			a.sendLater(c, f, later)
		}
		kit.ToastHost(c, i18n.T("toast.close"))
		root := a.windowFill(t)
		if a.vibrancy {
			root = ui.Transparent // the page tints its own part of the material (compose.html?material)
		}
		c.Root().Background(root) // last, as View: overlays reset it
	}
}

// sendLater draws the send later dialog l over f's page, and answers the
// page as it closes: the time chosen, or 0. A time not in the future is
// refused; days before today cannot be chosen.
func (a *App) sendLater(c *ui.Context, f *ComposeFrame, l *laterAsk) {
	now, loc := a.clock.Now(), i18n.Locale() // the interface language, as the strings
	kit.DateTimeDialog(c, &l.open, &l.at, kit.DateTimeDialogOpts{
		DateTimeOpts: kit.DateTimeOpts{
			Min: now, Date: i18n.T("compose.send_later.date"), Time: i18n.T("compose.send_later.time"),
			PrevMonth: i18n.T("common.previous_month"), NextMonth: i18n.T("common.next_month"), Locale: loc,
		},
		Title: i18n.T("compose.send_later.title"), Note: i18n.T("compose.send_later.note"),
		Confirm: i18n.T("compose.send_later.schedule"), Cancel: i18n.T("common.cancel"), Close: i18n.T("common.close"),
		Readout: func(at time.Time) string { return kit.Readout(loc, at, now) },
		Check: func(at time.Time) string {
			if !at.After(a.clock.Now()) {
				return i18n.T("compose.send_later.past")
			}
			return ""
		},
	}, func() { l.confirmed = true })
	if l.open {
		return
	}
	at := int64(0)
	if l.confirmed {
		at = l.at.UnixMilli()
	}
	select {
	case l.reply <- at:
	default: // answered already: a newer ask replaced it
	}
	f.mu.Lock()
	if f.later == l {
		f.later = nil
	}
	f.mu.Unlock()
}

// windowFill is a window's own color under its pages: clear over the
// material, and lighter in the light appearance, the blur still showing.
func (a *App) windowFill(t theme.Tokens) ui.Color {
	switch {
	case !a.vibrancy:
		return t.Window
	case t.Dark:
		return ui.Transparent
	}
	return t.Window.Alpha(theme.MaterialTint)
}

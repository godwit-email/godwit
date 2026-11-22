package app

import (
	"sync"

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
		title, status, toasts, bar, field := f.title, f.status, f.toasts, f.bar, f.field
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
		kit.ToastHost(c, i18n.T("toast.close"))
		root := a.windowFill(t)
		if a.vibrancy {
			root = ui.Transparent // the page tints its own part of the material (compose.html?material)
		}
		c.Root().Background(root) // last, as View: overlays reset it
	}
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

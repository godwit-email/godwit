package webui

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// ErrBadToast is returned for a toast of a kind native does not draw, or
// without text.
var ErrBadToast = errors.New("not a toast")

// maxLine is the most bytes of a toast's text or a title bar's line native
// is given: a toast shows two lines, and a page's longer text is cut there.
const maxLine = 1000

// cut is s, at most maxLine bytes, cut on a character's boundary.
func cut(s string) string {
	if len(s) <= maxLine {
		return s
	}
	s = s[:maxLine]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// toast checks a page's toast: kind is "", "success", "warning" or "error"
// (kit's), and the text is not empty. It returns the text native draws.
func toast(kind, text string) (string, error) {
	switch kind {
	case "", "success", "warning", "error":
	default:
		return "", ErrBadToast
	}
	if text == "" {
		return "", ErrBadToast
	}
	return cut(text), nil
}

// Toast shows text as a toast of kind ("", "success", "warning" or "error")
// in the calling compose window, drawn by native over the page.
func (p *ComposePage) Toast(ctx context.Context, kind, text string) error {
	win, ok := p.d.WinOf(ctx)
	if !ok || p.d.Toast == nil {
		return ErrNoDraft
	}
	text, err := toast(kind, text)
	if err != nil {
		return err
	}
	p.d.Toast(win, kind, text)
	return nil
}

// ToastBar is where the page's bar along its foot and the bar's input are,
// in CSS pixels from the page's top-left: native's toasts rise 8 above bar,
// level with field's right edge. Empty rectangles put them in the corner.
func (p *ComposePage) ToastBar(ctx context.Context, bar, field ui.Rect) error {
	win, ok := p.d.WinOf(ctx)
	if !ok || p.d.ToastBar == nil {
		return ErrNoDraft
	}
	p.d.ToastBar(win, bar, field)
	return nil
}

// TitleBar shows title (the subject, or 新邮件) and status (the save state)
// in the calling compose window's title bar, which native draws.
func (p *ComposePage) TitleBar(ctx context.Context, title, status string) error {
	win, ok := p.d.WinOf(ctx)
	if !ok || p.d.TitleBar == nil {
		return ErrNoDraft
	}
	p.d.TitleBar(win, cut(title), cut(status))
	return nil
}

// Toast shows text as a toast of kind ("", "success", "warning" or "error")
// over the Wit page, drawn by native.
func (p *WitPage) Toast(ctx context.Context, kind, text string) error {
	if _, _, err := p.w.caller(ctx); err != nil {
		return keyed(err)
	}
	text, err := toast(kind, text)
	if err != nil {
		return keyed(err)
	}
	if p.w.d.Toast != nil {
		p.w.d.Toast(kind, text)
	}
	return nil
}

// ToastBar is where the Wit page's composer and its input are, in CSS
// pixels from the page's top-left: native's toasts rise 8 above bar, level
// with field's right edge. Empty rectangles put them in the panel's corner.
func (p *WitPage) ToastBar(ctx context.Context, bar, field ui.Rect) error {
	if _, _, err := p.w.caller(ctx); err != nil {
		return keyed(err)
	}
	if p.w.d.ToastBar != nil {
		p.w.d.ToastBar(bar, field)
	}
	return nil
}

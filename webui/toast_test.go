package webui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A compose page's toasts, bar and title bar go to native for its own window;
// a toast of an unknown kind or without text, or a page with no window, gets
// nowhere; a long line is cut on a character's boundary.
func TestComposeToastsGoToNative(t *testing.T) {
	type shown struct {
		win           int
		kind, text    string
		bar, field    ui.Rect
		title, status string
	}
	var got shown
	p := NewComposePage(NewCompose(), PageDeps{
		WinOf:    func(context.Context) (int, bool) { return 3, true },
		Toast:    func(win int, kind, text string) { got.win, got.kind, got.text = win, kind, text },
		ToastBar: func(win int, bar, field ui.Rect) { got.win, got.bar, got.field = win, bar, field },
		TitleBar: func(win int, title, status string) { got.win, got.title, got.status = win, title, status },
	})
	ctx := t.Context()
	require.NoError(t, p.Toast(ctx, "error", "出错了"))
	assert.Equal(t, shown{win: 3, kind: "error", text: "出错了"}, got)
	bar, field := ui.Rect{X: 8, Y: 600, W: 844, H: 64}, ui.Rect{X: 700, Y: 612, W: 120, H: 40}
	require.NoError(t, p.ToastBar(ctx, bar, field))
	assert.Equal(t, bar, got.bar)
	assert.Equal(t, field, got.field)
	require.NoError(t, p.TitleBar(ctx, "你好", "草稿已保存 · 11:42"))
	assert.Equal(t, "你好", got.title)
	assert.Equal(t, "草稿已保存 · 11:42", got.status)

	got = shown{}
	assert.ErrorIs(t, p.Toast(ctx, "info", "x"), ErrBadToast)
	assert.ErrorIs(t, p.Toast(ctx, "", ""), ErrBadToast)
	assert.Equal(t, shown{}, got)

	long := strings.Repeat("长", maxLine) // 3 bytes each
	require.NoError(t, p.Toast(ctx, "", long))
	assert.True(t, utf8.ValidString(got.text))
	assert.LessOrEqual(t, len(got.text), maxLine+len("…"))

	none := NewComposePage(NewCompose(), PageDeps{WinOf: func(context.Context) (int, bool) { return 0, false }})
	assert.ErrorIs(t, none.Toast(ctx, "", "x"), ErrNoDraft)
	assert.ErrorIs(t, none.TitleBar(ctx, "x", ""), ErrNoDraft)
}

// The Wit page's toasts and composer go to native; another role's page, or a
// toast native does not draw, gets nowhere.
func TestWitPageToastsGoToNative(t *testing.T) {
	pages := NewPages(func(ctx context.Context) any { return ctx.Value(pageKey{}) })
	key, reader := new(int), new(int)
	pages.Register(key, "wit")
	pages.Register(reader, "reader")
	var toasts [][2]string
	var bars [][2]ui.Rect
	w := NewWit(pages, WitPageDeps{
		Toast:    func(kind, text string) { toasts = append(toasts, [2]string{kind, text}) },
		ToastBar: func(bar, field ui.Rect) { bars = append(bars, [2]ui.Rect{bar, field}) },
	})
	w.SetService(stubService{})
	p := NewWitPage(w)

	require.NoError(t, p.Toast(pageCtx(key), "", "已复制"))
	bar, field := ui.Rect{X: 24, Y: 700, W: 808, H: 96}, ui.Rect{X: 24, Y: 700, W: 808, H: 72}
	require.NoError(t, p.ToastBar(pageCtx(key), bar, field))
	assert.Error(t, p.Toast(pageCtx(key), "loud", "x"))
	assert.Error(t, p.Toast(pageCtx(reader), "", "x"))
	assert.Error(t, p.ToastBar(pageCtx(reader), bar, field))
	assert.Equal(t, [][2]string{{"", "已复制"}}, toasts)
	assert.Equal(t, [][2]ui.Rect{{bar, field}}, bars)
}

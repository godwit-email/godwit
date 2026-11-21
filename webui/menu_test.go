package webui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubService stands for the Service: a menu reads none of it.
type stubService struct{ Service }

// PopupMenu hands the page's entries, submenus and all, to native at the page's
// position and returns the ID chosen; a menu past the limits never shows.
func TestWitPagePopupMenu(t *testing.T) {
	pages := NewPages(func(ctx context.Context) any { return ctx.Value(pageKey{}) })
	key := new(int)
	pages.Register(key, "wit")
	reader := new(int)
	pages.Register(reader, "reader")
	var menus [][]MenuEntry
	var at [][2]int
	choice := ""
	w := NewWit(pages, WitPageDeps{PopupMenu: func(es []MenuEntry, x, y int) string {
		menus, at = append(menus, es), append(at, [2]int{x, y})
		return choice
	}})
	w.SetService(stubService{})
	p := NewWitPage(w)
	ctx := pageCtx(key)

	entries := []MenuEntry{
		{ID: "m:a", Label: "Sonnet", Checked: true, Icon: "mail"},
		{Separator: true},
		{Label: "Models", Header: true},
		{ID: "effort", Label: "Effort", Badge: "Low", Items: []MenuEntry{
			{ID: "effort:low", Label: "Low", Checked: true},
			{ID: "effort:high", Label: "High", Disabled: true},
		}},
	}
	choice = "effort:low"
	id, err := p.PopupMenu(ctx, entries, 12, 34)
	require.NoError(t, err)
	assert.Equal(t, "effort:low", id)
	assert.Equal(t, [][]MenuEntry{entries}, menus)
	assert.Equal(t, [][2]int{{12, 34}}, at)

	choice = ""
	id, err = p.PopupMenu(ctx, entries, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, id, "none chosen")

	deep := []MenuEntry{{ID: "1", Items: []MenuEntry{{ID: "2", Items: []MenuEntry{{ID: "3", Items: []MenuEntry{{ID: "4"}}}}}}}}
	_, err = p.PopupMenu(ctx, deep, 0, 0)
	assert.ErrorIs(t, err, ErrBadMenu)
	_, err = p.PopupMenu(ctx, make([]MenuEntry, maxMenuEntries+1), 0, 0)
	assert.ErrorIs(t, err, ErrBadMenu)
	_, err = p.PopupMenu(ctx, []MenuEntry{{ID: "x", Label: "X", Icon: "no-such-icon"}}, 0, 0)
	assert.ErrorIs(t, err, ErrBadMenu, "an icon that would panic native")
	assert.Len(t, menus, 2, "a menu past the limits is not shown")

	for name, c := range map[string]context.Context{"no page": context.Background(), "another role": pageCtx(reader)} {
		_, err = p.PopupMenu(c, entries, 0, 0)
		assert.ErrorIs(t, err, ErrWrongPage, name)
	}
	assert.Len(t, menus, 2, "none for a page that is not the Wit page's")
}

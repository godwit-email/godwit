package webui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pageKey struct{}

// pageCtx is a call from the page with key k.
func pageCtx(k any) context.Context { return context.WithValue(context.Background(), pageKey{}, k) }

func TestPageRolesRegistered(t *testing.T) {
	p := NewPages(func(ctx context.Context) any { return ctx.Value(pageKey{}) })
	wit, win := new(int), new(int) // a registered page; a window's page
	id := p.Register(wit, "wit")
	got, err := p.Caller(pageCtx(wit), "wit")
	require.NoError(t, err)
	assert.Equal(t, id, got)
	k, ok := p.Key(id)
	assert.True(t, ok)
	assert.Same(t, wit, k)
	role, ok := p.Role(wit)
	assert.True(t, ok)
	assert.Equal(t, "wit", role)
	_, ok = p.Role(win)
	assert.False(t, ok, "not registered")

	_, err = p.Caller(pageCtx(wit), "reader")
	assert.ErrorIs(t, err, ErrWrongPage, "another role")
	_, err = p.Caller(pageCtx(win), "wit")
	assert.ErrorIs(t, err, ErrWrongPage, "a window's page")
	_, err = p.Caller(context.Background(), "wit")
	assert.ErrorIs(t, err, ErrWrongPage, "no page")

	p.Unregister(wit)
	_, err = p.Caller(pageCtx(wit), "wit")
	assert.ErrorIs(t, err, ErrWrongPage, "unregistered")
	_, ok = p.Role(wit)
	assert.False(t, ok, "unregistered")
	_, ok = p.Key(id)
	assert.False(t, ok)
	assert.NotEqual(t, id, p.Register(wit, "wit"), "IDs are not reused")
}

// An embedded page's CallerWindow is the main window, which has no compose
// session: ComposePage refuses it and touches nothing.
func TestPageRolesComposeFromPage(t *testing.T) {
	f, _, _ := witFixture(t, "<p>Hello</p>")
	const main = 7
	ctx := f.ctx(main)
	_, err := f.page.Load(ctx)
	assert.ErrorIs(t, err, ErrNoDraft)
	_, err = f.page.WitInfo(ctx)
	assert.ErrorIs(t, err, ErrNoDraft)
	_, err = f.page.Ask(ctx, "hi", nil, "", "", "")
	assert.ErrorIs(t, err, ErrNoDraft)
	_, err = f.page.Skill(ctx, "shorter", "", "")
	assert.ErrorIs(t, err, ErrNoDraft)
	_, err = f.page.Follow(ctx)
	assert.ErrorIs(t, err, ErrNoDraft)
	assert.ErrorIs(t, f.page.Stop(ctx, 1), ErrNoDraft)
	_, err = f.page.Decide(ctx, 1, true)
	assert.ErrorIs(t, err, ErrNoDraft)
	_, err = f.page.Undo(ctx)
	assert.ErrorIs(t, err, ErrNoDraft)
	assert.ErrorIs(t, f.page.OpenLink(ctx, "https://example.com"), ErrNoDraft)
	opened := false
	f.page.d.OpenModels = func() { opened = true }
	assert.ErrorIs(t, f.page.OpenModels(ctx), ErrNoDraft)
	assert.False(t, opened, "another page cannot open the model settings")

	cs, err := f.e.Svc.ListConversations(t.Context(), "assist")
	require.NoError(t, err)
	assert.Empty(t, cs, "no conversation made")
	assert.Empty(t, f.events(main))
	f.c.mu.Lock()
	assert.Empty(t, f.c.follows)
	f.c.mu.Unlock()
}

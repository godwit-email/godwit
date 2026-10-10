package webui

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	e    *coretest.Env
	acc  core.Account
	c    *Compose
	win  int
	page *ComposePage
}

// newFixture is a ready Compose with one account; calls come from f.win.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{e: coretest.New(t), win: 1, c: NewCompose()}
	f.acc = f.e.AddAccount(t, "alice@example.com")
	f.c.SetService(f.e.Svc)
	f.page = NewComposePage(f.c, func(ctx context.Context) (int, bool) {
		w, ok := ctx.Value(winKey{}).(int)
		return w, ok
	})
	return f
}

type winKey struct{}

func (f *fixture) ctx(win int) context.Context {
	return context.WithValue(context.Background(), winKey{}, win)
}

func (f *fixture) draft(t *testing.T, mode string, src int64) core.Draft {
	t.Helper()
	d, err := f.e.Svc.NewDraft(t.Context(), f.acc.ID, mode, src)
	require.NoError(t, err)
	return d
}

func TestComposeLoadOwnDraft(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, d.ID, st.DraftID)
	assert.Equal(t, []Account{{f.acc.ID, "alice@example.com"}}, st.Accounts)
	assert.Equal(t, f.acc.ID, st.From)
	assert.Equal(t, d.Revision, st.Revision)
	assert.Equal(t, "system", st.Theme.Mode)
}

func TestComposeSaveThenLoad(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	body := "第一行\n\n  第三行"
	res, err := f.page.Save(f.ctx(1), ComposeEdit{Body: body, Subject: "Hi", To: []string{"a@example.com"}, Revision: st.Revision})
	require.NoError(t, err)
	assert.False(t, res.Conflict)
	got, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, body, got.Body)
	assert.Equal(t, "Hi", got.Subject)
	assert.Equal(t, []string{"a@example.com"}, got.To)
	assert.Equal(t, res.Revision, got.Revision)
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, "Hi", stored.Subject)
	assert.Equal(t, []string{"a@example.com"}, stored.To)
}

func TestComposeConsecutiveSaves(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	r1, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "one", Revision: d.Revision})
	require.NoError(t, err)
	r2, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "two", Revision: r1.Revision})
	require.NoError(t, err)
	assert.False(t, r1.Conflict || r2.Conflict)
	got, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, "two", got.Body)
}

func TestComposeSaveKeepsQuoteAndSignature(t *testing.T) {
	f := newFixture(t)
	_, err := f.e.Svc.UpdateAccount(t.Context(), f.acc.ID, core.AccountPatch{Signature: ptr("Alice\nGodwit")})
	require.NoError(t, err)
	f.e.Deliver(t, f.acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "plan", "the plan"))
	page, err := f.e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	d := f.draft(t, "reply", page.Items[0].ID)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Contains(t, st.Quote, "the plan")
	assert.Contains(t, st.Signature, "Godwit")
	_, q0, s0 := splitOf(d.HTML)
	require.NotEmpty(t, q0)
	require.NotEmpty(t, s0)
	_, err = f.page.Save(f.ctx(1), ComposeEdit{Body: "thanks", Revision: st.Revision})
	require.NoError(t, err)
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	ed, q1, s1 := splitOf(stored.HTML)
	assert.Equal(t, q0, q1)
	assert.Equal(t, s0, s1)
	assert.Equal(t, "thanks", bodyText(ed))
}

func TestComposeStaleRevisionConflicts(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	d.HTML = "<p>elsewhere</p>"
	_, err = f.e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	res, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "mine", Revision: st.Revision})
	require.NoError(t, err)
	assert.True(t, res.Conflict)
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, "elsewhere", bodyText(stored.HTML))
}

func TestComposeZeroRevisionRefused(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	_, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "x", Subject: "s"})
	assert.ErrorIs(t, err, ErrNoRevision)
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, d.Revision, stored.Revision)
	assert.Empty(t, stored.Subject)
}

func TestComposeCallerWithoutDraftRefused(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	check := func(win int) {
		_, err := f.page.Load(f.ctx(win))
		assert.ErrorIs(t, err, ErrNoDraft)
		_, err = f.page.Save(f.ctx(win), ComposeEdit{Revision: d.Revision})
		assert.ErrorIs(t, err, ErrNoDraft)
		assert.ErrorIs(t, f.page.Closed(f.ctx(win), 1, true), ErrNoDraft)
	}
	check(2)
	f.c.Detach(1)
	check(1)
	_, err := f.page.Load(context.Background()) // no caller window at all
	assert.ErrorIs(t, err, ErrNoDraft)
}

func TestComposeClosedHook(t *testing.T) {
	f := newFixture(t)
	f.c.Attach(1, f.draft(t, "new", 0).ID)
	var win, gen int
	var saved bool
	f.c.OnClosed(func(w, g int, s bool) { win, gen, saved = w, g, s })
	require.NoError(t, f.page.Closed(f.ctx(1), 3, true))
	assert.Equal(t, 1, win)
	assert.Equal(t, 3, gen)
	assert.True(t, saved)
}

func TestComposeNotReady(t *testing.T) {
	c := NewCompose()
	c.Attach(1, 1)
	p := NewComposePage(c, func(context.Context) (int, bool) { return 1, true })
	_, err := p.Load(t.Context())
	assert.ErrorIs(t, err, ErrNotReady)
}

func TestComposeTwoWindowsIsolated(t *testing.T) {
	f := newFixture(t)
	d1, d2 := f.draft(t, "new", 0), f.draft(t, "new", 0)
	f.c.Attach(1, d1.ID)
	f.c.Attach(2, d2.ID)
	var wg sync.WaitGroup
	for win, d := range map[int]core.Draft{1: d1, 2: d2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.page.Save(f.ctx(win), ComposeEdit{Body: "body of " + string(rune('0'+win)), Revision: d.Revision})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	for win, d := range map[int]core.Draft{1: d1, 2: d2} {
		stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
		require.NoError(t, err)
		ed, _, _ := splitOf(stored.HTML)
		assert.Equal(t, "body of "+string(rune('0'+win)), bodyText(ed))
	}
}

func TestComposeBoundSurface(t *testing.T) {
	typ := reflect.TypeFor[*ComposePage]()
	var names []string
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"Closed", "Load", "Save"}, names)
}

func ptr[T any](v T) *T { return &v }

func splitOf(h string) (editable, quote, signature string) { return mail.SplitDraft(h) }

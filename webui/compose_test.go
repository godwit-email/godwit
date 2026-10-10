package webui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	e    *coretest.Env
	acc  core.Account
	c    *Compose
	win  int
	page *ComposePage
	pick func() ([]string, error) // what the file dialog returns
	done []int                    // windows OnDone fired for
}

// newFixture is a ready Compose with one account; calls come from f.win.
func newFixture(t *testing.T, opts ...coretest.Option) *fixture {
	t.Helper()
	f := &fixture{e: coretest.New(t, opts...), win: 1, c: NewCompose()}
	f.acc = f.e.AddAccount(t, "alice@example.com")
	f.c.SetService(f.e.Svc)
	f.page = NewComposePage(f.c, PageDeps{
		WinOf: func(ctx context.Context) (int, bool) {
			w, ok := ctx.Value(winKey{}).(int)
			return w, ok
		},
		PickFiles: func(context.Context, int) ([]string, error) { return f.pick() },
	})
	f.c.OnDone(func(w int) { f.done = append(f.done, w) })
	return f
}

type winKey struct{}

func (f *fixture) ctx(win int) context.Context {
	return context.WithValue(context.Background(), winKey{}, win)
}

// replyDraft is a reply to a delivered mail, with the quote and the
// signature "Alice\nGodwit".
func (f *fixture) replyDraft(t *testing.T) core.Draft {
	t.Helper()
	_, err := f.e.Svc.UpdateAccount(t.Context(), f.acc.ID, core.AccountPatch{Signature: ptr("Alice\nGodwit")})
	require.NoError(t, err)
	f.e.Deliver(t, f.acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "plan", "the plan"))
	page, err := f.e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	return f.draft(t, "reply", page.Items[0].ID)
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
	assert.Equal(t, d.UpdatedAt.UnixMilli(), st.UpdatedAt)
	assert.NotZero(t, st.UpdatedAt)
	assert.Equal(t, "system", st.Theme.Mode)
}

func TestComposeSaveThenLoad(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	body := "<p>第一行</p><p></p><p>  第三行</p>"
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
	r1, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>one</p>", Revision: d.Revision})
	require.NoError(t, err)
	r2, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>two</p>", Revision: r1.Revision})
	require.NoError(t, err)
	assert.False(t, r1.Conflict || r2.Conflict)
	got, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, "<p>two</p>", got.Body)
}

func TestComposeSaveKeepsQuoteAndSignature(t *testing.T) {
	f := newFixture(t)
	d := f.replyDraft(t)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Contains(t, st.Quote, "the plan")
	assert.Contains(t, st.Signature, "Godwit")
	_, q0, s0 := splitOf(d.HTML)
	require.NotEmpty(t, q0)
	require.NotEmpty(t, s0)
	_, err = f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>thanks</p>", Revision: st.Revision})
	require.NoError(t, err)
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	ed, q1, s1 := splitOf(stored.HTML)
	assert.Equal(t, q0, q1)
	assert.Equal(t, s0, s1)
	assert.Equal(t, "<p>thanks</p>", ed)
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
	assert.Equal(t, "<p>elsewhere</p>", stored.HTML)
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
	p := NewComposePage(c, PageDeps{WinOf: func(context.Context) (int, bool) { return 1, true }})
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
			_, err := f.page.Save(f.ctx(win), ComposeEdit{Body: "<p>body of " + string(rune('0'+win)) + "</p>", Revision: d.Revision})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	for win, d := range map[int]core.Draft{1: d1, 2: d2} {
		stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
		require.NoError(t, err)
		ed, _, _ := splitOf(stored.HTML)
		assert.Equal(t, "<p>body of "+string(rune('0'+win))+"</p>", ed)
	}
}

func TestComposeBoundSurface(t *testing.T) {
	typ := reflect.TypeFor[*ComposePage]()
	var names []string
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"AddAttachments", "Closed", "Discard", "Latest", "Load", "RemoveAttachment", "Save", "Send", "SetAccount"}, names)
}

func ptr[T any](v T) *T { return &v }

func splitOf(h string) (editable, quote, signature string) { return mail.SplitDraft(h) }

func (f *fixture) stored(t *testing.T, id int64) core.Draft {
	t.Helper()
	d, err := f.e.Svc.GetDraft(t.Context(), id)
	require.NoError(t, err)
	return d
}

func (f *fixture) gone(t *testing.T, id int64) bool {
	t.Helper()
	_, err := f.e.Svc.GetDraft(t.Context(), id)
	return err != nil
}

func TestComposeLoadRichBody(t *testing.T) {
	i18n.SetLanguage("zh")
	f := newFixture(t)
	d := f.replyDraft(t)
	_, q, sig := splitOf(d.HTML)
	d.HTML = mail.JoinDraft("<p>a <strong>b</strong></p>", q, sig)
	_, err := f.e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, "<p>a <strong>b</strong></p>", st.Body)
	assert.Contains(t, st.Quote, "the plan")
	assert.NotContains(t, st.Quote, "<blockquote")
	assert.Contains(t, st.Signature, "Godwit")
	assert.NotContains(t, st.Signature, "<div")
	assert.Equal(t, "发送", st.Text["compose.send"])
	assert.Equal(t, []Attachment{}, st.Attachments)
}

func TestComposeSaveRichBody(t *testing.T) {
	f := newFixture(t)
	d := f.replyDraft(t)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	_, q0, s0 := splitOf(d.HTML)
	_, err = f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>x</p><ul><li>y</li></ul><script>z</script>", Revision: st.Revision})
	require.NoError(t, err)
	ed, q1, s1 := splitOf(f.stored(t, d.ID).HTML)
	assert.Contains(t, ed, "<ul><li>y</li></ul>")
	assert.NotContains(t, ed, "script")
	assert.Equal(t, q0, q1)
	assert.Equal(t, s0, s1)
}

// A body that carries a marker must not pass for the quote or the signature.
func TestComposeSaveStripsMarkers(t *testing.T) {
	f := newFixture(t)
	d := f.replyDraft(t)
	f.c.Attach(1, d.ID)
	_, q0, s0 := splitOf(d.HTML)
	_, err := f.page.Save(f.ctx(1), ComposeEdit{Body: `<blockquote data-godwit="quote"><p>fake</p></blockquote>`, Revision: d.Revision})
	require.NoError(t, err)
	ed, q1, s1 := splitOf(f.stored(t, d.ID).HTML)
	assert.Contains(t, ed, "fake")
	assert.Equal(t, q0, q1)
	assert.Equal(t, s0, s1)
}

func TestComposeSetAccountKeepsBody(t *testing.T) {
	f := newFixture(t)
	acc2 := f.e.AddAccount(t, "bob@example.com")
	_, err := f.e.Svc.UpdateAccount(t.Context(), acc2.ID, core.AccountPatch{Signature: ptr("Bob\nB.")})
	require.NoError(t, err)
	d := f.replyDraft(t)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	res, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>B</p>", Revision: st.Revision})
	require.NoError(t, err)
	ch, err := f.page.SetAccount(f.ctx(1), acc2.ID, res.Revision)
	require.NoError(t, err)
	assert.False(t, ch.Conflict)
	assert.Contains(t, ch.Signature, "Bob")
	assert.NotContains(t, ch.Signature, "Godwit")
	assert.Equal(t, st.Revision+2, ch.Revision)
	stored := f.stored(t, d.ID)
	assert.Equal(t, acc2.ID, stored.AccountID)
	ed, _, _ := splitOf(stored.HTML)
	assert.Equal(t, "<p>B</p>", ed)
	// The next save keeps the new signature.
	_, err = f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>B2</p>", Revision: ch.Revision})
	require.NoError(t, err)
	_, _, sig := splitOf(f.stored(t, d.ID).HTML)
	assert.Contains(t, sig, "Bob")
}

func TestComposeSetAccountStale(t *testing.T) {
	f := newFixture(t)
	acc2 := f.e.AddAccount(t, "bob@example.com")
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	ch, err := f.page.SetAccount(f.ctx(1), acc2.ID, d.Revision+5)
	require.NoError(t, err)
	assert.True(t, ch.Conflict)
	assert.Equal(t, f.acc.ID, f.stored(t, d.ID).AccountID)
}

func TestComposeAttachments(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	dir := t.TempDir()
	small, big := filepath.Join(dir, "notes.txt"), filepath.Join(dir, "big.bin")
	require.NoError(t, os.WriteFile(small, []byte("hello"), 0o600))
	fh, err := os.Create(big)
	require.NoError(t, err)
	require.NoError(t, fh.Truncate(26<<20)) // sparse
	require.NoError(t, fh.Close())
	f.pick = func() ([]string, error) { return []string{small, big}, nil }
	res, err := f.page.AddAttachments(f.ctx(1), d.Revision)
	require.NoError(t, err)
	assert.False(t, res.Conflict)
	require.Len(t, res.Attachments, 1)
	assert.Equal(t, "notes.txt", res.Attachments[0].Filename)
	assert.Equal(t, 5, res.Attachments[0].Size)
	assert.Equal(t, []string{"big.bin"}, res.TooLarge)
	assert.Empty(t, res.Failed)
	assert.Equal(t, d.Revision+1, res.Revision)

	ch, err := f.page.RemoveAttachment(f.ctx(1), res.Attachments[0].ID, res.Revision)
	require.NoError(t, err)
	assert.Empty(t, ch.Attachments)
	assert.Equal(t, res.Revision+1, ch.Revision)
	assert.Empty(t, f.stored(t, d.ID).Attachments)

	f.pick = func() ([]string, error) { return nil, nil } // cancelled
	res, err = f.page.AddAttachments(f.ctx(1), ch.Revision)
	require.NoError(t, err)
	assert.Equal(t, ch.Revision, res.Revision)
	assert.Empty(t, res.Attachments)
}

func TestComposeAttachStale(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	f.pick = func() ([]string, error) { t.Fatal("no dialog for a stale revision"); return nil, nil }
	res, err := f.page.AddAttachments(f.ctx(1), d.Revision+1)
	require.NoError(t, err)
	assert.True(t, res.Conflict)
	ch, err := f.page.RemoveAttachment(f.ctx(1), 1, d.Revision+1)
	require.NoError(t, err)
	assert.True(t, ch.Conflict)
}

func TestComposeAttachPartial(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.txt")
	require.NoError(t, os.WriteFile(ok, []byte("ok"), 0o600))
	f.pick = func() ([]string, error) { return []string{ok, filepath.Join(dir, "missing.txt")}, nil }
	res, err := f.page.AddAttachments(f.ctx(1), d.Revision)
	require.NoError(t, err)
	assert.Equal(t, []string{"missing.txt"}, res.Failed)
	assert.Empty(t, res.TooLarge)
	stored := f.stored(t, d.ID)
	assert.Equal(t, stored.Revision, res.Revision)
	require.Len(t, res.Attachments, 1)
	assert.Equal(t, Attachment{ID: stored.Attachments[0].ID, Filename: "ok.txt", Size: 2}, res.Attachments[0])
}

func TestComposeReleaseWaitsForSave(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	inSave, resume := make(chan struct{}), make(chan struct{})
	saveHook = func() { close(inSave); <-resume }
	t.Cleanup(func() { saveHook = nil })
	saved := make(chan error)
	go func() {
		_, err := f.page.Save(f.ctx(1), ComposeEdit{Body: "<p>kept</p>", Revision: d.Revision})
		saved <- err
	}()
	<-inSave
	released := make(chan struct{})
	go func() { f.c.Release(1); close(released) }()
	select {
	case <-released:
		t.Fatal("Release did not wait for the save")
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	require.NoError(t, <-saved)
	<-released
	assert.False(t, f.gone(t, d.ID), "the saved draft is not empty")
	_, err := f.page.Load(f.ctx(1))
	assert.ErrorIs(t, err, ErrNoDraft)
}

func TestComposeSendSendsLatest(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync()) // the message stays in the outbox
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	res, err := f.page.Save(f.ctx(1), ComposeEdit{To: []string{"bob@example.com"}, Subject: "s", Body: "<p>latestbody</p>", Revision: d.Revision})
	require.NoError(t, err)
	sr, err := f.page.Send(f.ctx(1), res.Revision, "")
	require.NoError(t, err)
	assert.True(t, sr.Sent)
	assert.Equal(t, []int{1}, f.done)
	assert.True(t, f.gone(t, d.ID))
	raw, err := os.ReadFile(filepath.Join(f.e.DataDir, "outbox", strconv.FormatInt(d.ID, 10)+".eml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "latestbody")
}

func TestComposeSendWarnings(t *testing.T) {
	f := newFixture(t)
	f.e.InstallPlugin(t, "send-warn")
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	res, err := f.page.Save(f.ctx(1), ComposeEdit{To: []string{"bob@example.com"}, Subject: "report", Body: "<p>x</p>", Revision: d.Revision})
	require.NoError(t, err)
	r1, err := f.page.Save(f.ctx(1), ComposeEdit{To: []string{"bob@example.com"}, Subject: "report", Body: "<p>y</p>", Revision: res.Revision})
	require.NoError(t, err)
	sr, err := f.page.Send(f.ctx(1), r1.Revision, "")
	require.NoError(t, err)
	assert.False(t, sr.Sent)
	assert.Equal(t, []SendWarning{{Plugin: "send-warn", Message: "check before sending: report"}}, sr.Warnings)
	require.NotEmpty(t, sr.Digest)
	assert.Empty(t, f.done)

	old, err := f.page.Send(f.ctx(1), res.Revision, sr.Digest)
	require.NoError(t, err)
	assert.True(t, old.Conflict)
	assert.False(t, old.Sent)
	assert.False(t, f.gone(t, d.ID), "nothing sent")

	sr, err = f.page.Send(f.ctx(1), r1.Revision, sr.Digest)
	require.NoError(t, err)
	assert.True(t, sr.Sent)
	assert.True(t, f.gone(t, d.ID))
	assert.Equal(t, []int{1}, f.done)
}

func TestComposeSendBlocked(t *testing.T) {
	f := newFixture(t)
	f.e.InstallPlugin(t, "send-block")
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	res, err := f.page.Save(f.ctx(1), ComposeEdit{To: []string{"bob@example.com"}, Subject: "report", Body: "<p>x</p>", Revision: d.Revision})
	require.NoError(t, err)
	sr, err := f.page.Send(f.ctx(1), res.Revision, "")
	require.NoError(t, err)
	assert.False(t, sr.Sent)
	assert.Equal(t, &SendWarning{Plugin: "send-block", Message: "blocked: report"}, sr.Blocked)
	assert.False(t, f.gone(t, d.ID))
	assert.Empty(t, f.done)
}

func TestComposeDiscard(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	require.NoError(t, f.page.Discard(f.ctx(1)))
	assert.True(t, f.gone(t, d.ID))
	assert.Equal(t, []int{1}, f.done)
}

func TestComposeLatest(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	_, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	d.HTML = "<p>elsewhere</p>"
	d, err = f.e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	st, err := f.page.Latest(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, "<p>elsewhere</p>", st.Body)
	assert.Equal(t, d.Revision, st.Revision)
}

func TestComposeReleaseDeletesEmpty(t *testing.T) {
	f := newFixture(t)
	edit := func(d core.Draft, fn func(*core.Draft)) core.Draft {
		fn(&d)
		d, err := f.e.Svc.SaveDraft(t.Context(), d)
		require.NoError(t, err)
		return d
	}
	reply := f.replyDraft(t)
	withAttachment := f.draft(t, "new", 0)
	src := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(src, []byte("a"), 0o600))
	_, err := f.e.Svc.AddAttachment(t.Context(), withAttachment.ID, src)
	require.NoError(t, err)
	deleted := map[string]core.Draft{
		"new":              f.draft(t, "new", 0),
		"reply":            reply,
		"blank paragraphs": edit(f.draft(t, "new", 0), func(d *core.Draft) { d.HTML = "<p></p><p> </p><p>&nbsp;</p>" + d.HTML }),
	}
	kept := map[string]core.Draft{
		"subject":               edit(f.draft(t, "new", 0), func(d *core.Draft) { d.Subject = "s" }),
		"reply, subject edited": edit(f.replyDraft(t), func(d *core.Draft) { d.Subject = "other" }),
		"bcc":                   edit(f.draft(t, "new", 0), func(d *core.Draft) { d.Bcc = []string{"x@example.com"} }),
		"attachment":            withAttachment,
		"body":                  edit(f.draft(t, "new", 0), func(d *core.Draft) { d.HTML = "<p>text</p>" + d.HTML }),
	}
	win := 0
	for _, set := range []map[string]core.Draft{deleted, kept} {
		for _, d := range set {
			win++
			f.c.Attach(win, d.ID)
			f.c.Release(win)
			_, err := f.page.Load(f.ctx(win))
			assert.ErrorIs(t, err, ErrNoDraft)
		}
	}
	for name, d := range deleted {
		assert.True(t, f.gone(t, d.ID), name)
	}
	for name, d := range kept {
		assert.False(t, f.gone(t, d.ID), name)
	}
}

func TestComposeSaveAfterDeleteRefused(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	require.NoError(t, f.e.Svc.DeleteDraft(t.Context(), d.ID))
	_, err = f.page.Save(f.ctx(1), ComposeEdit{Subject: "s", Body: "<p>x</p>", Revision: st.Revision})
	assert.Error(t, err)
	assert.True(t, f.gone(t, d.ID))
}

// A write from elsewhere while the dialog is open: nothing is added, and the
// page hears Conflict, not the other writer's revision as its own.
func TestComposeAttachExternalEditConflicts(t *testing.T) {
	f := newFixture(t)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	src := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(src, []byte("a"), 0o600))
	for _, picked := range [][]string{{src}, nil} {
		cur := f.stored(t, d.ID)
		f.pick = func() ([]string, error) {
			cur.Subject = "elsewhere"
			_, err := f.e.Svc.SaveDraft(t.Context(), cur)
			require.NoError(t, err)
			return picked, nil
		}
		res, err := f.page.AddAttachments(f.ctx(1), cur.Revision)
		require.NoError(t, err)
		assert.True(t, res.Conflict, "%v", picked)
		assert.Empty(t, f.stored(t, d.ID).Attachments)
	}
}

// Closing while the dialog is open: Release does not wait for the dialog,
// and the files picked afterwards are dropped.
func TestComposeReleaseDuringDialog(t *testing.T) {
	f := newFixture(t)
	empty := f.draft(t, "new", 0)
	kept := f.draft(t, "new", 0)
	kept.Subject = "s"
	kept, err := f.e.Svc.SaveDraft(t.Context(), kept)
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(src, []byte("a"), 0o600))
	for win, d := range map[int]core.Draft{1: empty, 2: kept} {
		f.c.Attach(win, d.ID)
		inDialog, picked := make(chan struct{}), make(chan struct{})
		f.pick = func() ([]string, error) { close(inDialog); <-picked; return []string{src}, nil }
		done := make(chan error)
		go func() {
			_, err := f.page.AddAttachments(f.ctx(win), d.Revision)
			done <- err
		}()
		<-inDialog
		f.c.Release(win) // returns while the dialog is still open
		close(picked)
		assert.ErrorIs(t, <-done, ErrNoDraft)
	}
	assert.True(t, f.gone(t, empty.ID))
	assert.Empty(t, f.stored(t, kept.ID).Attachments)
}

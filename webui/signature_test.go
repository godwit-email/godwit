package webui

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sigFixture struct {
	e     *coretest.Env
	acc   core.Account
	pages *Pages
	s     *Signature
	p     *SignaturePage
	page  any // registered as "signature"
}

func newSigFixture(t *testing.T) *sigFixture {
	t.Helper()
	f := &sigFixture{e: coretest.New(t, coretest.WithoutAutoSync())}
	f.acc = f.e.AddAccount(t, "alice@example.com")
	f.pages = NewPages(func(ctx context.Context) any { return ctx.Value(pageKey{}) })
	f.s = NewSignature(f.pages)
	f.s.SetService(f.e.Svc)
	f.p = NewSignaturePage(f.s)
	f.page = new(int)
	f.pages.Register(f.page, "signature")
	return f
}

func (f *sigFixture) stored(t *testing.T) string {
	t.Helper()
	accs, err := f.e.Svc.ListAccounts(context.Background())
	require.NoError(t, err)
	return accs[0].Signature
}

func TestSignatureSaveRole(t *testing.T) {
	f := newSigFixture(t)
	reader := new(int)
	f.pages.Register(reader, "reader")
	for name, ctx := range map[string]context.Context{
		"reader": pageCtx(reader), "unregistered": pageCtx(new(int)), "window": context.Background(),
	} {
		assert.ErrorIs(t, f.p.Save(ctx, f.acc.ID, "<p>x</p>"), ErrWrongPage, name)
		_, err := f.p.Load(ctx, f.acc.ID)
		assert.ErrorIs(t, err, ErrWrongPage, name)
	}
	assert.Equal(t, "", f.stored(t))
	assert.Error(t, f.p.Save(pageCtx(f.page), f.acc.ID+99, "<p>x</p>"), "an unknown account")

	require.NoError(t, f.p.Save(pageCtx(f.page), f.acc.ID, "<p><u>Bob</u></p>"))
	assert.Equal(t, core.SignatureHTMLPrefix+"<p><u>Bob</u></p>", f.stored(t))
	got, err := f.p.Load(pageCtx(f.page), f.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "<p><u>Bob</u></p>", got)
	require.NoError(t, f.p.Save(pageCtx(f.page), f.acc.ID, ""))
	assert.Equal(t, "", f.stored(t), "an empty editor clears the signature")
}

func TestSignatureLoadLegacy(t *testing.T) {
	f := newSigFixture(t)
	sig := "Bob <b>\nline2"
	_, err := f.e.Svc.UpdateAccount(context.Background(), f.acc.ID, core.AccountPatch{Signature: &sig})
	require.NoError(t, err)
	got, err := f.p.Load(pageCtx(f.page), f.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "<p>Bob &lt;b&gt;<br>line2</p>", got)
}

func TestSignatureFlushAck(t *testing.T) {
	f := newSigFixture(t)
	var tokens []int64
	var acks []bool
	f.s.Flush(func(tok int64) { tokens = append(tokens, tok) }, func(ok bool) { acks = append(acks, ok) })
	require.Len(t, tokens, 1)
	assert.ErrorIs(t, f.p.Saved(pageCtx(new(int)), tokens[0], true), ErrWrongPage)
	assert.Empty(t, acks)
	require.NoError(t, f.p.Saved(pageCtx(f.page), tokens[0], false))
	require.NoError(t, f.p.Saved(pageCtx(f.page), tokens[0], true), "a second ack is a no-op")
	assert.Equal(t, []bool{false}, acks)
}

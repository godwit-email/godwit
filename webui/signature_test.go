package webui

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	pngenc "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sigFixture struct {
	e     *coretest.Env
	acc   core.Account
	sig   int64 // the signature acc uses
	pages *Pages
	pick  string // the file PickImage's dialog answers, "" for none
	s     *Signature
	p     *SignaturePage
	page  any // registered as "signature"
}

func newSigFixture(t *testing.T) *sigFixture {
	t.Helper()
	f := &sigFixture{e: coretest.New(t, coretest.WithoutAutoSync())}
	f.acc = f.e.AddAccount(t, "alice@example.com")
	sig, err := f.e.Svc.CreateSignature(context.Background(), "Work")
	require.NoError(t, err)
	f.sig = sig.ID
	_, err = f.e.Svc.UpdateAccount(context.Background(), f.acc.ID, core.AccountPatch{SignatureID: &f.sig})
	require.NoError(t, err)
	f.pages = NewPages(func(ctx context.Context) any { return ctx.Value(pageKey{}) })
	f.s = NewSignature(f.pages, func(context.Context) (string, error) { return f.pick, nil })
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
		assert.ErrorIs(t, f.p.Save(ctx, f.sig, "<p>x</p>"), ErrWrongPage, name)
		_, err := f.p.Load(ctx, f.sig)
		assert.ErrorIs(t, err, ErrWrongPage, name)
	}
	assert.Equal(t, "", f.stored(t))
	assert.ErrorIs(t, f.p.Save(pageCtx(f.page), f.sig+99, "<p>x</p>"), ErrSignatureGone, "an unknown signature")

	require.NoError(t, f.p.Save(pageCtx(f.page), f.sig, "<p><u>Bob</u></p>"))
	assert.Equal(t, core.SignatureHTMLPrefix+"<p><u>Bob</u></p>", f.stored(t))
	got, err := f.p.Load(pageCtx(f.page), f.sig)
	require.NoError(t, err)
	assert.Equal(t, "<p><u>Bob</u></p>", got)
	assert.ErrorIs(t, f.p.Save(pageCtx(f.page), f.sig, strings.Repeat("x", maxSignatureBytes+1)), ErrSignatureTooLarge)
	assert.Equal(t, core.SignatureHTMLPrefix+"<p><u>Bob</u></p>", f.stored(t))
	require.NoError(t, f.p.Save(pageCtx(f.page), f.sig, ""))
	assert.Equal(t, "", f.stored(t), "an empty editor clears the signature")
}

func TestSignatureLoadLegacy(t *testing.T) {
	f := newSigFixture(t)
	sig := "Bob <b>\nline2"
	_, err := f.e.Svc.UpdateAccount(context.Background(), f.acc.ID, core.AccountPatch{Signature: &sig})
	require.NoError(t, err)
	got, err := f.p.Load(pageCtx(f.page), f.sig)
	require.NoError(t, err)
	assert.Equal(t, "<p>Bob &lt;b&gt;<br>line2</p>", got)
}

func TestSignatureFlushAck(t *testing.T) {
	f := newSigFixture(t)
	var tokens []int64
	var acks []bool
	flush := func() {
		f.s.Flush(func(tok int64) { tokens = append(tokens, tok) }, func(ok bool) { acks = append(acks, ok) })
	}
	flush()
	assert.Empty(t, tokens, "a page that never ran is not asked")
	assert.Equal(t, []bool{true}, acks)
	acks = nil
	_, err := f.p.State(pageCtx(f.page))
	require.NoError(t, err)
	flush()
	require.Len(t, tokens, 1)
	assert.ErrorIs(t, f.p.Saved(pageCtx(new(int)), tokens[0], true), ErrWrongPage)
	assert.Empty(t, acks)
	require.NoError(t, f.p.Saved(pageCtx(f.page), tokens[0], false))
	require.NoError(t, f.p.Saved(pageCtx(f.page), tokens[0], true), "a second ack is a no-op")
	assert.Equal(t, []bool{false}, acks)
	f.s.Close()
	flush()
	assert.Len(t, tokens, 1, "a closed page is not asked")
	assert.Equal(t, []bool{false, true}, acks)
}

func TestSignatureLocaleFollowsLanguage(t *testing.T) {
	f := newSigFixture(t)
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	for _, locale := range i18n.Catalog.Languages() {
		i18n.SetLanguage(locale)
		st, err := f.p.State(pageCtx(f.page))
		require.NoError(t, err)
		assert.Equal(t, locale, st.Locale)
	}
}

func TestSignaturePickImage(t *testing.T) {
	f := newSigFixture(t)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0o600))
		return p
	}
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	var png bytes.Buffer
	require.NoError(t, pngenc.Encode(&png, img))

	f.pick = write("logo.png", png.Bytes())
	_, err := f.p.PickImage(pageCtx(new(int)))
	assert.ErrorIs(t, err, ErrWrongPage, "only the signature page")
	got, err := f.p.PickImage(pageCtx(f.page))
	require.NoError(t, err)
	assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png.Bytes()), got)

	f.pick = write("logo.jpg", []byte("not an image at all"))
	_, err = f.p.PickImage(pageCtx(f.page))
	assert.ErrorIs(t, err, core.ErrSignatureImage, "the content decides, not the name")
	f.pick = write("big.png", append(png.Bytes(), make([]byte, core.SignatureImageMax)...))
	_, err = f.p.PickImage(pageCtx(f.page))
	assert.ErrorIs(t, err, core.ErrSignatureImage)
	f.pick = ""
	got, err = f.p.PickImage(pageCtx(f.page))
	require.NoError(t, err)
	assert.Empty(t, got, "none picked")

	// The editor's data: images are saved as the account's images and loaded back as data:.
	html := `<p>Bob</p><img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(png.Bytes()) + `">`
	require.NoError(t, f.p.Save(pageCtx(f.page), f.sig, html))
	assert.NotContains(t, f.stored(t), "data:")
	assert.Contains(t, f.stored(t), "cid:sig-")
	got, err = f.p.Load(pageCtx(f.page), f.sig)
	require.NoError(t, err)
	assert.Contains(t, got, `src="data:image/png;base64,`)
	four := strings.Repeat(`<img src="data:image/png;base64,`+base64.StdEncoding.EncodeToString(png.Bytes())+`">`, 4)
	require.NoError(t, f.p.Save(pageCtx(f.page), f.sig, four), "the same image four times is one image, and under the size cap")
}

func TestSignatureCodeAndTall(t *testing.T) {
	f := newSigFixture(t)
	var talls []bool
	f.s.OnTall = func(tall bool) { talls = append(talls, tall) }
	_, err := f.p.Code(pageCtx(new(int)), f.sig)
	assert.ErrorIs(t, err, ErrWrongPage)
	assert.ErrorIs(t, f.p.Tall(pageCtx(new(int)), true), ErrWrongPage)

	// What the HTML mode typed is saved clean, and read back as stored.
	require.NoError(t, f.p.Save(pageCtx(f.page), f.sig,
		`<table style="color:red"><tr><td onclick="x()">Bob<script>alert(1)</script></td></tr></table><p>A</p><img src="https://t.example/p.png">`))
	code, err := f.p.Code(pageCtx(f.page), f.sig)
	require.NoError(t, err)
	assert.NotContains(t, code, "script")
	assert.NotContains(t, code, "onclick")
	assert.NotContains(t, code, "t.example")
	assert.Contains(t, code, `style="color: red"`)
	assert.Contains(t, code, "</table>\n<p>A</p>", "one block per line")

	require.NoError(t, f.p.Tall(pageCtx(f.page), true))
	require.NoError(t, f.p.Tall(pageCtx(f.page), false))
	assert.Equal(t, []bool{true, false}, talls)
}

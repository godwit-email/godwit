package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// A draft saved before a restart can be found and edited again through Service.
func TestDraftsSurviveRestart(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s, err := core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	im, smtpAddr := servers(t)
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	d, err := s.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "plan", "<p>first</p>"
	d, err = s.SaveDraft(ctx, d)
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "notes.txt")
	require.NoError(t, os.WriteFile(src, []byte("n"), 0o600))
	_, err = s.AddAttachment(ctx, d.ID, src)
	require.NoError(t, err)
	other, err := s.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	list, err := s.ListDrafts(ctx, acc.ID)
	require.NoError(t, err)
	var ids []int64
	for _, x := range list {
		ids = append(ids, x.ID)
	}
	assert.ElementsMatch(t, []int64{d.ID, other.ID}, ids)
	got, err := s.GetDraft(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"bob@example.com"}, got.To)
	assert.Equal(t, "plan", got.Subject)
	assert.Contains(t, got.HTML, "first")
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "notes.txt", got.Attachments[0].Filename)

	got.HTML = "<p>second</p>"
	_, err = s.SaveDraft(ctx, got)
	require.NoError(t, err)
	got, err = s.GetDraft(ctx, d.ID)
	require.NoError(t, err)
	assert.Contains(t, got.HTML, "second")
	assert.Len(t, got.Attachments, 1)
	_, err = s.GetDraft(ctx, 9999)
	assert.Error(t, err)
	none, err := s.ListDrafts(ctx, acc.ID+1)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// Draft HTML is sanitized before it is stored, so every read returns safe HTML.
func TestDraftHTMLSanitized(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	evil := `<p onclick="steal()">hi</p><script>alert(1)</script><img src="https://tracker.example/p.png" onerror="x()"><a href="javascript:alert(2)">l</a>`
	saved, err := s.SaveDraft(ctx, core.Draft{AccountID: acc.ID, Mode: "new", HTML: evil})
	require.NoError(t, err)
	got, err := s.GetDraft(ctx, saved.ID)
	require.NoError(t, err)
	list, err := s.ListDrafts(ctx, acc.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	for name, h := range map[string]string{"SaveDraft": saved.HTML, "GetDraft": got.HTML, "ListDrafts": list[0].HTML} {
		for _, bad := range []string{"<script", "onclick", "onerror", "tracker.example", "javascript:"} {
			assert.NotContains(t, h, bad, name)
		}
		assert.Contains(t, h, "hi", name)
	}
	got.HTML = `<p>edit</p><iframe src="https://evil.example"></iframe>`
	upd, err := s.SaveDraft(ctx, got)
	require.NoError(t, err)
	assert.NotContains(t, upd.HTML, "iframe")
	assert.Contains(t, upd.HTML, "edit")
}

func TestDraftRevision(t *testing.T) {
	keyring.MockInit()
	s, err := core.New(ctx, core.Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	im, smtpAddr := servers(t)
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	d, err := s.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	r0 := d.Revision
	assert.Equal(t, int64(1), r0)
	stale := d
	d.HTML = "<p>a</p>"
	d, err = s.SaveDraft(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, r0+1, d.Revision)
	d.HTML = "<p>b</p>" // same second, still a new revision
	d, err = s.SaveDraft(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, r0+2, d.Revision)
	list, err := s.ListDrafts(ctx, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, r0+2, list[0].Revision)

	stale.HTML = "<p>old</p>"
	_, err = s.SaveDraft(ctx, stale)
	assert.ErrorIs(t, err, core.ErrStaleDraft)
	stale.Revision = 0 // 0 skips the check
	got, err := s.SaveDraft(ctx, stale)
	require.NoError(t, err)
	assert.Equal(t, r0+3, got.Revision)
}

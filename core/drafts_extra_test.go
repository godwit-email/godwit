package core_test

import (
	"bytes"
	netmail "net/mail"
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func attachFile(t *testing.T, s *core.Service, draftID int64, name, body string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(src, []byte(body), 0o600))
	_, err := s.AddAttachment(ctx, draftID, src)
	require.NoError(t, err)
}

func TestDeleteDraft(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	attachFile(t, e.Svc, d.ID, "notes.txt", "n")
	keep, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	files, _ := filepath.Glob(filepath.Join(e.DataDir, "drafts", "*", "*"))
	require.Len(t, files, 1)

	require.NoError(t, e.Svc.DeleteDraft(ctx, d.ID))
	_, err = e.Svc.GetDraft(ctx, d.ID)
	assert.Error(t, err)
	assert.NoFileExists(t, files[0])
	_, err = e.Svc.GetDraft(ctx, keep.ID)
	assert.NoError(t, err, "other drafts stay")
	assert.ErrorContains(t, e.Svc.DeleteDraft(ctx, d.ID), "not found")
}

func TestSetDraftAccount(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	a := e.AddAccount(t, "alice@example.com")
	b := e.AddAccount(t, "bob@example.com")
	for id, sig := range map[int64]string{a.ID: "Alice A.", b.ID: "Bob B."} {
		_, err := e.Svc.UpdateAccount(ctx, id, core.AccountPatch{Signature: &sig})
		require.NoError(t, err)
	}
	d, err := e.Svc.NewDraft(ctx, a.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject = []string{"carol@example.com"}, "hello"
	d.HTML = "<p>body text</p>" + d.HTML
	d, err = e.Svc.SaveDraft(ctx, d)
	require.NoError(t, err)
	attachFile(t, e.Svc, d.ID, "notes.txt", "n")
	d, err = e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err)

	got, err := e.Svc.SetDraftAccount(ctx, d.ID, b.ID, d.Revision)
	require.NoError(t, err)
	assert.Equal(t, b.ID, got.AccountID)
	assert.Equal(t, d.Revision+1, got.Revision)
	ed, _, sig := mail.SplitDraft(got.HTML)
	assert.Contains(t, ed, "body text")
	assert.Contains(t, sig, "Bob B.")
	assert.NotContains(t, got.HTML, "Alice A.")
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "notes.txt", got.Attachments[0].Filename)

	_, err = e.Svc.SetDraftAccount(ctx, d.ID, a.ID, d.Revision)
	assert.ErrorIs(t, err, core.ErrStaleDraft)
	_, err = e.Svc.SetDraftAccount(ctx, d.ID, 999, got.Revision)
	assert.Error(t, err)

	require.NoError(t, e.Svc.Send(ctx, d.ID))
	require.NoError(t, e.Svc.SyncNow(ctx, b.ID))
	msgs := e.SMTP(b.ID).Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "bob@example.com", msgs[0].From)
	hdr, err := netmail.ReadMessage(bytes.NewReader(msgs[0].Data))
	require.NoError(t, err)
	from, err := hdr.Header.AddressList("From")
	require.NoError(t, err)
	assert.Equal(t, "bob@example.com", from[0].Address)
	assert.Contains(t, string(msgs[0].Data), "notes.txt")
	assert.Empty(t, e.Sent(a.ID))
}

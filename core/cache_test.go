package core_test

import (
	"os"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAttachment delivers a message with a 10-byte attachment to the
// account's inbox and opens it, returning the attachment's ID.
func openAttachment(t *testing.T, e *coretest.Env, accountID int64) int64 {
	t.Helper()
	e.Deliver(t, accountID, "INBOX", coretest.Msg("carol@example.com", "x@example.com", "files", "see attached",
		coretest.Attachment("a.bin", "application/octet-stream", []byte("0123456789"))))
	folders, err := e.Svc.ListFolders(ctx, accountID)
	require.NoError(t, err)
	for _, f := range folders {
		if f.Role != "inbox" {
			continue
		}
		page, err := e.Svc.ListMessages(ctx, core.View{FolderID: f.ID}, "", 0)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		m, err := e.Svc.GetMessage(ctx, page.Items[0].ID, false)
		require.NoError(t, err)
		require.Len(t, m.Attachments, 1)
		_, _, err = e.Svc.GetAttachment(ctx, m.Attachments[0].ID)
		require.NoError(t, err)
		return m.Attachments[0].ID
	}
	t.Fatal("no inbox")
	return 0
}

func TestAttachmentCacheUsageAndClear(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	other := e.AddAccount(t, "bob@example.com")
	usage := func(id int64) int64 {
		t.Helper()
		n, err := e.Svc.AttachmentCacheUsage(ctx, id)
		require.NoError(t, err)
		return n
	}
	attID := openAttachment(t, e, acc.ID)
	openAttachment(t, e, other.ID)
	assert.Equal(t, int64(10), usage(acc.ID))

	require.NoError(t, e.Svc.ClearAttachmentCache(ctx, acc.ID))
	assert.Zero(t, usage(acc.ID))
	assert.Equal(t, int64(10), usage(other.ID), "other accounts keep their cache")

	before := e.IMAP(acc.ID).BytesSent()
	p, _, err := e.Svc.GetAttachment(ctx, attID)
	require.NoError(t, err)
	assert.Greater(t, e.IMAP(acc.ID).BytesSent(), before, "downloaded again")
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "0123456789", string(data))
	assert.Equal(t, int64(10), usage(acc.ID))
}

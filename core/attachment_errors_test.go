package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A download from a folder the server recreated since the sync says to sync
// again, and a stored part that is no part path is refused, not fetched.
func TestAttachmentDownloadErrors(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a@x", "a", 2))
	e.sync(t)
	atts := e.rows(t, "SELECT id FROM attachments WHERE message_id = ? ORDER BY id", e.idOf(t, "a@x"))
	require.Len(t, atts, 2)

	_, err := e.s.db.Execute(bg, "UPDATE attachments SET part = '2.x' WHERE id = ?", atts[1][0])
	require.NoError(t, err)
	_, _, err = e.s.GetAttachment(bg, store.Int(atts[1][0]))
	assert.ErrorContains(t, err, "bad attachment part 2.x")

	e.im.ResetUIDValidity("INBOX")
	_, _, err = e.s.GetAttachment(bg, store.Int(atts[0][0]))
	assert.ErrorContains(t, err, "server folder changed since sync; sync again")
}

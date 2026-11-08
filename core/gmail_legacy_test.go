package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Older versions kept the provider when the user changed IMAP servers.
func TestLegacyGmailProviderArchiveUsesCurrentServer(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Work", nil))
	e.put(t, "Work", msg("legacy-provider@x"))
	e.sync(t)
	id := e.idOf(t, "legacy-provider@x")
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET provider = 'gmail', imap_gmail = NULL WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
	e.sync(t)
	assert.Equal(t, []string{"legacy-provider@x"}, e.onServer(t, "Archive"))
	assert.Empty(t, e.onServer(t, "Work"))
}

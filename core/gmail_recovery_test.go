package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A local edit made while sync discovers a remote deletion stays attached to
// its message until the next push explicitly reports that it cannot apply it.
func TestGmailMissingMessageKeepsPendingChangeUntilConflict(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	m := e.im.AddGmail(msg("gone@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "gone@x")
	m.Unlabel("INBOX")
	m.Unlabel(testmail.GmailAllMail)
	mailsync.BeforeFlagsWrite = func() {
		mailsync.BeforeFlagsWrite = nil
		require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
	}
	t.Cleanup(func() { mailsync.BeforeFlagsWrite = nil })
	e.sync(t)
	exists, err := e.s.MessageExists(bg, id)
	require.NoError(t, err)
	require.True(t, exists, "the pending edit keeps its message")
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "conflict", pending[0].State)
	e.sync(t)
	pending, err = e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "conflict", pending[0].State)
	assert.NotEmpty(t, pending[0].LastError)
}

// Gmail's identity extension does not promise MOVE. It must never fall back
// to COPY/Deleted/EXPUNGE, whose Gmail semantics depend on account settings.
func TestGmailWithoutMoveRejectsArchiveSafely(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	m := e.im.AddGmail(msg("stay@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "stay@x")
	require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
	e.sync(t)
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "failed", pending[0].State)
	assert.Contains(t, pending[0].LastError, "MOVE")
	assert.Equal(t, []string{"INBOX", testmail.GmailAllMail}, m.Folders())
	assert.Zero(t, e.im.MovesAndCopies())
	assert.Equal(t, []int64{id}, e.listed(t, "INBOX"))
}

package core

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlainLegacyArchiveNeedsNoLogin(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Bin", nil))
	e.put(t, "INBOX", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", e.folderID(t, "Bin")))
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET imap_gmail = NULL WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	e.im.Stop()
	require.NoError(t, e.s.MoveToRole(bg, []int64{e.idOf(t, "m@x")}, "archive"), "a server with no Gmail folders is not probed")
	assert.Empty(t, e.listed(t, "INBOX"))
}

func TestGmailHiddenAllMailDropsMailGoneFromTrash(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	m := e.im.AddGmail(msg("bin@x"), nil, testmail.GmailTrash)
	e.sync(t)
	id := e.idOf(t, "bin@x")
	m.Unlabel(testmail.GmailTrash) // Gmail expunges Trash after 30 days
	e.sync(t)
	exists, err := e.s.MessageExists(bg, id)
	require.NoError(t, err)
	assert.False(t, exists, "mail gone from Trash was deleted for good")
}

func TestGmailLabelFoundDuringMoveShowsAfterIt(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("only@x"), nil, "INBOX")
	e.sync(t)
	id := e.idOf(t, "only@x")
	_, err := e.s.ArchiveMessages(bg, []int64{id}, 0)
	require.NoError(t, err)
	m.Label("Work") // a filter on another client, while the archive waits for its undo window
	e.sync(t)
	assert.Empty(t, e.listed(t, "Work"), "hidden while the move is pending")
	clock.Advance(10 * time.Second)
	e.sync(t)
	assert.Equal(t, []string{"Work"}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "Work"))
}

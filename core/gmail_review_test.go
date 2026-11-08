package core

import (
	"fmt"
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
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprintf("early=%v", early), func(t *testing.T) { labelFoundDuringMove(t, early) })
	}
}

func labelFoundDuringMove(t *testing.T, early bool) {
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
	if !early {
		clock.Advance(10 * time.Second)
	}
	e.sync(t)
	e.sync(t) // a found label may release the archive before its window ends
	assert.Equal(t, []string{"Work"}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "Work"))
}

func TestGmailFlagsSkipALabelAnotherClientRemoved(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	m := e.im.AddGmail(msg("f@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "f@x")
	m.Unlabel("INBOX") // archived elsewhere since the last sync
	require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
	e.sync(t)
	e.sync(t)
	got, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Contains(t, got.Flags, `\Flagged`, "the change reached the message through All Mail")
	assert.Equal(t, []string{"done"}, e.opStates(t))
}

func TestGmailOrphanKeptWhileAllMailHasIt(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET sync_limit = 1 WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	m := e.im.AddGmail(msg("old@x"), nil, "INBOX", testmail.GmailAllMail)
	e.im.AddGmail(msg("new@x"), nil, testmail.GmailAllMail) // All Mail's one cached UID
	e.sync(t)
	id := e.idOf(t, "old@x")
	m.Unlabel("INBOX") // archived on another client
	e.sync(t)
	exists, err := e.s.MessageExists(bg, id)
	require.NoError(t, err)
	assert.True(t, exists, "All Mail still holds it")
	assert.Contains(t, e.listed(t, testmail.GmailAllMail), id)
}

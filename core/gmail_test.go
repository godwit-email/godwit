package core

import (
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listed returns the IDs ListMessages gives for folder.
func (e *syncEnv) listed(t *testing.T, folder string) []int64 {
	t.Helper()
	p, err := e.s.ListMessages(bg, View{FolderID: e.folderID(t, folder)}, "", 50)
	require.NoError(t, err)
	return msgIDs(p.Items)
}

// A Gmail message is one message (ADR 0032), whichever folders it is in, with
// its X-GM-MSGID; a message with the same Message-ID and content but its own
// X-GM-MSGID is another.
func TestGmailMessageIsOneInEveryFolder(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	m := e.im.AddGmail(msg("a"), nil, "INBOX", testmail.GmailAllMail)
	twin := e.im.AddGmail(msg("a"), nil, testmail.GmailAllMail)
	e.sync(t)

	got := map[string]int{}
	for _, r := range e.rows(t, "SELECT gmail_msgid, (SELECT count(*) FROM message_locations WHERE message_id = messages.id) FROM messages") {
		got[store.Str(r[0])] = int(store.Int(r[1]))
	}
	assert.Equal(t, map[string]int{strconv.FormatUint(m.ID, 10): 2, strconv.FormatUint(twin.ID, 10): 1}, got)
	inbox := e.listed(t, "INBOX")
	require.Len(t, inbox, 1)
	assert.Equal(t, []int64{inbox[0]}, e.listed(t, testmail.GmailAllMail)[1:], "All Mail lists the same message, after its twin")
}

// A message in several folders reports one as its FolderID: INBOX, else
// another folder before All Mail.
func TestGmailMessagePrefersInboxThenLabelOverAllMail(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Create("zz", nil)) // listed after [Gmail]/All Mail
	e.im.AddGmail(msg("a@x"), nil, testmail.GmailAllMail, "zz")
	e.im.AddGmail(msg("b@x"), nil, testmail.GmailAllMail, "zz", "INBOX")
	e.sync(t)

	for mid, want := range map[string]string{"a@x": "zz", "b@x": "INBOX"} {
		m, err := e.s.GetMessage(bg, e.idOf(t, mid), false)
		require.NoError(t, err)
		assert.Equal(t, e.folderID(t, want), m.FolderID, mid)
	}
}

// Archiving, removing a label, deleting a label or a folder's UIDVALIDITY
// reset on the server takes the message out of that folder only; it leaves
// Godwit when no folder holds it.
func TestGmailServerRemovalDropsOnlyThatFolder(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	for _, f := range []string{"Work", "Old", "Reset"} {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work", "Old", "Reset", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "a@x")

	m.Unlabel("INBOX") // archived on the web
	m.Unlabel("Work")
	m.Unlabel("Old")
	require.NoError(t, e.im.User.Delete("Old"))
	m.Unlabel("Reset")
	require.NoError(t, e.im.User.Delete("Reset"))
	require.NoError(t, e.im.User.Create("Reset", nil))
	e.sync(t)
	assert.Empty(t, e.listed(t, "INBOX"))
	assert.Empty(t, e.listed(t, "Work"))
	assert.Empty(t, e.listed(t, "Reset"))
	assert.Equal(t, []int64{id}, e.listed(t, testmail.GmailAllMail))

	m.Label("Reset")
	e.sync(t)
	assert.Equal(t, []int64{id}, e.listed(t, "Reset"), "the folder's new UID is the same message")

	m.Unlabel("Reset")
	m.Unlabel(testmail.GmailAllMail)
	e.sync(t)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages"))
}

// A message another client moves to a folder synced later in the round stays
// the message it was: it is not deleted in between and imported again.
func TestGmailMoveToLaterFolderKeepsMessage(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Create("zz", nil)) // listed after INBOX
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX")
	e.sync(t)
	id := e.idOf(t, "a@x")

	m.Label("zz")
	m.Unlabel("INBOX")
	e.sync(t)
	assert.Equal(t, []int64{id}, e.listed(t, "zz"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages"))
}

// Another server is never asked for X-GM-MSGID, and its messages have none.
func TestSyncWithoutGmailHasNoMsgID(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", msg("a"))
	e.sync(t)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE gmail_msgid IS NULL"))
}

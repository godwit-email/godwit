package core

import (
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2"
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

// inboxRule labels mail in the inbox with Bills (u1).
func inboxRule() RuleDoc {
	return RuleDoc{Version: RuleVersion, Name: "Inbox", Trigger: TriggerReceive, Match: MatchAll,
		Conditions: []RuleCondition{{Field: FieldFolder, Op: OpIs, Value: "inbox"}},
		Actions:    []RuleAction{{Kind: ActionLabel, Label: "u1"}}}
}

// newGmailRulesEnv is a Gmail account synced once with the folders given
// (so what lands in them next is new mail), the label Bills (u1) and
// inboxRule on.
func newGmailRulesEnv(t *testing.T, folders ...string) *syncEnv {
	t.Helper()
	e := newSyncEnv(t, testmail.Gmail())
	for _, f := range folders {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	e.sync(t)
	e.saveRule(t, inboxRule(), true)
	return e
}

// One new message in a label, INBOX and All Mail is received once, with its
// folders known: a rule on mail in the inbox runs on it, though the label is
// synced before INBOX.
func TestGmailNewMessageReceivedOnceInInbox(t *testing.T) {
	e := newGmailRulesEnv(t, "Aaa") // listed before INBOX
	e.im.AddGmail(msg("a@x"), nil, "Aaa", "INBOX", testmail.GmailAllMail)
	e.sync(t)
	e.waitRuns(t, 1)

	id := e.idOf(t, "a@x")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM events_log WHERE type = 'message.received' AND message_id = ?", id))
	assert.Equal(t, "rule", e.labelOn(t, id))
}

// New mail first found in a folder on its first sync (not new mail there) is
// still received for its place in the inbox.
func TestGmailNewMessageInNewLabelIsReceived(t *testing.T) {
	e := newGmailRulesEnv(t)
	require.NoError(t, e.im.User.Create("Aaa", nil)) // listed before INBOX, synced the first time with it
	e.im.AddGmail(msg("a@x"), nil, "Aaa", "INBOX", testmail.GmailAllMail)
	e.sync(t)
	e.waitRuns(t, 1)

	id := e.idOf(t, "a@x")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM events_log WHERE type = 'message.received' AND message_id = ?", id))
	assert.Equal(t, "rule", e.labelOn(t, id))
}

// The user's own mail in Sent, with All Mail or the inbox (sent to
// themselves), runs no receive rule and counts as sent in contacts, though
// All Mail is synced before Sent.
func TestGmailSentMailIsNotReceived(t *testing.T) {
	e := newGmailRulesEnv(t)
	e.im.AddGmail(eml("out@x", "alice@example.com", "bob@example.com", "hi", day, "b"), []imap.Flag{imap.FlagSeen}, testmail.GmailSent, testmail.GmailAllMail)
	e.im.AddGmail(eml("self@x", "alice@example.com", "alice@example.com", "note", day, "b"), []imap.Flag{imap.FlagSeen}, testmail.GmailSent, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	e.rulesIdle(t)

	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs"))
	cs, err := e.s.Contacts(bg)
	require.NoError(t, err)
	var bob *Contact
	for i := range cs {
		if cs[i].Address == "bob@example.com" {
			bob = &cs[i]
		}
	}
	require.NotNil(t, bob)
	assert.Equal(t, []int{1, 0}, []int{bob.Sent, bob.Received})
}

// Archiving a message also in Work moves it from INBOX into the All Mail UID
// it already has (#506): Work keeps its server location, and a star queued
// after the archive still reaches the server.
func TestGmailArchiveKeepsOtherFoldersAndQueuedFlags(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "a@x")

	require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
	require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
	e.sync(t)
	assert.Equal(t, []string{"done", "done"}, e.opStates(t))
	assert.Equal(t, []string{"Work", testmail.GmailAllMail}, m.Folders())
	assert.Equal(t, []string{`a@x \Flagged`}, e.onServer(t, "Work"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM message_locations WHERE message_id = ? AND folder_id = ? AND uid = ?",
		id, e.folderID(t, "Work"), m.UID("Work")), "Work's server location stays")
	assert.Equal(t, []int64{id}, e.listed(t, testmail.GmailAllMail))
}

// A move the server answers without COPYUID is claimed by X-GM-MSGID, even
// where another message has the same Message-ID and size.
func TestGmailMoveWithoutCopyUIDClaimedByMsgID(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutCOPYUID())
	require.NoError(t, e.im.User.Create("Work", nil))
	e.im.AddGmail(msg("a@x"), nil, "INBOX", testmail.GmailAllMail)
	e.im.AddGmail(msg("a@x"), nil, "Work") // a twin, as far as Message-ID and size tell
	e.sync(t)
	var id int64
	for _, r := range e.rows(t, "SELECT id FROM messages WHERE "+shownCol+" = ?", e.folderID(t, "INBOX")) {
		id = store.Int(r[0])
	}

	require.NoError(t, e.s.Move(bg, []int64{id}, e.folderID(t, "Work")))
	e.sync(t)
	assert.Equal(t, []string{"done"}, e.opStates(t))
	assert.Contains(t, e.listed(t, "Work"), id)
	assert.Len(t, e.listed(t, "Work"), 2)
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM messages"))
}

// Another server is never asked for X-GM-MSGID, and its messages have none.
func TestSyncWithoutGmailHasNoMsgID(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", msg("a"))
	e.sync(t)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE gmail_msgid IS NULL"))
}

package core

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContacts(t *testing.T) {
	e := newSyncEnv(t)
	unsub := "List-Unsubscribe: <mailto:leave@shop.example>"
	e.put(t, "INBOX", eml("s1@x", "Shop <news@shop.example>", "alice@example.com", "a", "Mon, 05 Oct 2026 09:00:00 +0000", "b", unsub))
	e.putFlagged(t, "INBOX", eml("s2@x", "Shop News <NEWS@shop.example>", "alice@example.com", "b", "Tue, 06 Oct 2026 09:00:00 +0000", "b"), imap.FlagSeen)
	e.put(t, "Archive", eml("s3@x", "news@shop.example", "alice@example.com", "c", "Sun, 04 Oct 2026 09:00:00 +0000", "b", unsub))
	e.put(t, "Trash", eml("s4@x", "news@shop.example", "alice@example.com", "d", "Wed, 07 Oct 2026 09:00:00 +0000", "b", unsub))
	e.put(t, "INBOX", eml("b1@x", "Bob <bob@example.com>", "alice@example.com", "e", "Thu, 08 Oct 2026 09:00:00 +0000", "b"))
	e.put(t, "Sent", eml("me@x", "Alice <alice@example.com>", "bob@example.com", "f", day, "b"))
	e.put(t, "Sent", eml("c1@x", "Alice <alice@example.com>", "Chen Jing <chen@corp.example>, alice@example.com", "g", "Fri, 09 Oct 2026 09:00:00 +0000", "b",
		"Cc: Bob <BOB@example.com>, chen@corp.example"))
	e.put(t, "INBOX", eml("me2@x", "Alice <Alice@example.com>", "alice@example.com", "h", day, "b"))
	e.sync(t)
	require.NoError(t, e.s.BlockSender(bg, "news@shop.example"))

	got, err := e.s.Contacts(bg)
	require.NoError(t, err)
	var addrs []string
	for _, c := range got {
		addrs = append(addrs, c.Address)
	}
	require.Equal(t, []string{"bob@example.com", "news@shop.example", "chen@corp.example"}, addrs, "most mail, then the latest; the user and Trash left out")
	bob, shop, chen := got[0], got[1], got[2]
	assert.Equal(t, Contact{Name: "Bob", Address: "bob@example.com", Messages: 3, Received: 1, Sent: 2, Unread: 1, Inbox: 1,
		Last: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC).Local()}, bob, "sent to and copied, once per message")
	assert.Equal(t, Contact{Name: "Shop News", Address: "news@shop.example", Messages: 3, Received: 3, Unread: 2, Inbox: 2,
		Last: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC).Local(), ListMessage: e.idOf(t, "s1@x"), Blocked: true}, shop,
		"the latest name; the newest with List-Unsubscribe outside Trash")
	assert.Equal(t, Contact{Name: "Chen Jing", Address: "chen@corp.example", Messages: 1, Sent: 1,
		Last: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC).Local()}, chen, "only written to: the name the user wrote")

	page, err := e.s.ContactMail(bg, "Bob <bob@example.com>", "", 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{e.idOf(t, "c1@x"), e.idOf(t, "b1@x"), e.idOf(t, "me@x")}, msgIDs(page.Items), "either way, newest first")
	assert.Empty(t, page.NextCursor)

	// Page by page to the end: the mail Received and Sent count, Trash's left out.
	var all []int64
	cursor := ""
	for {
		page, err := e.s.ContactMail(bg, "news@shop.example", cursor, 1)
		require.NoError(t, err)
		all = append(all, msgIDs(page.Items)...)
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	assert.Equal(t, []int64{e.idOf(t, "s2@x"), e.idOf(t, "s1@x"), e.idOf(t, "s3@x")}, all)
	assert.Len(t, all, shop.Messages)

	d, err := e.s.ContactDetail(bg, "news@shop.example")
	require.NoError(t, err)
	assert.Equal(t, ContactDetail{Archivable: 2}, d)
}

func TestArchiveSenderSkipsStarredAndRepliedAndUndoes(t *testing.T) {
	e := newSyncEnv(t)
	shop := "Shop <news@shop.example>"
	e.put(t, "INBOX", eml("plain@x", shop, "alice@example.com", "plain", day, "b"))
	e.put(t, "INBOX", eml("plain2@x", "NEWS@shop.example", "alice@example.com", "plain 2", day, "b"))
	e.putFlagged(t, "INBOX", eml("starred@x", shop, "alice@example.com", "starred", day, "b"), imap.FlagFlagged)
	e.putFlagged(t, "INBOX", eml("answered@x", shop, "alice@example.com", "answered", day, "b"), imap.FlagAnswered)
	e.put(t, "INBOX", eml("asked@x", shop, "alice@example.com", "asked", day, "b"))
	e.put(t, "Sent", eml("reply@x", "alice@example.com", "news@shop.example", "Re: asked", day, "b", "In-Reply-To: <asked@x>"))
	e.put(t, "INBOX", eml("thread@x", shop, "alice@example.com", "star in thread", day, "b"))
	e.putFlagged(t, "Archive", eml("later@x", shop, "alice@example.com", "Re: star in thread", day, "b", "In-Reply-To: <thread@x>"), imap.FlagFlagged)
	e.put(t, "INBOX", eml("bob@x", "bob@example.com", "alice@example.com", "other sender", day, "b"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "asked@x"), e.threadOf(t, "reply@x"))
	require.Equal(t, e.threadOf(t, "thread@x"), e.threadOf(t, "later@x"))
	inbox, archive := e.folderID(t, "INBOX"), e.folderID(t, "Archive")

	d, err := e.s.ContactDetail(bg, "news@shop.example")
	require.NoError(t, err)
	res, err := e.s.ArchiveSender(bg, "news@shop.example")
	require.NoError(t, err)
	assert.Equal(t, 4, res.Skipped, "starred, answered, replied from Sent, starred later in the thread")
	assert.Equal(t, []int{2, 4}, []int{d.Archivable, d.Kept}, "as previewed")
	assert.Equal(t, []ArchivedMessage{{e.idOf(t, "plain@x"), inbox, archive}, {e.idOf(t, "plain2@x"), inbox, archive}}, res.Moved)
	assert.Equal(t, 5, e.count(t, "SELECT count(*) FROM messages WHERE folder_id = ?", inbox))

	// One moved on since: undo leaves it where the user put it.
	trash := e.folderID(t, "Trash")
	require.NoError(t, e.s.Move(bg, []int64{e.idOf(t, "plain2@x")}, trash))
	require.NoError(t, e.s.UndoArchive(bg, res))
	assert.Equal(t, [][]any{{inbox}}, e.rows(t, "SELECT folder_id FROM messages WHERE message_id = 'plain@x'"))
	assert.Equal(t, [][]any{{trash}}, e.rows(t, "SELECT folder_id FROM messages WHERE message_id = 'plain2@x'"))
	e.sync(t)
	assert.Contains(t, e.onServer(t, "INBOX"), "plain@x")
	assert.NotContains(t, e.onServer(t, "Archive"), "plain@x")
}

func TestArchiveSenderNeedsArchiveFolder(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Delete("Archive"))
	e.put(t, "INBOX", eml("plain@x", "news@shop.example", "alice@example.com", "plain", day, "b"))
	e.sync(t)
	_, err := e.s.ArchiveSender(bg, "news@shop.example")
	var nf *ErrNoRoleFolder
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE folder_id = ?", e.folderID(t, "INBOX")))
}

// A role the user moved to another folder leaves the detected one ordinary:
// its mail counts, and a message there is no reply.
func TestContactsFollowChosenRoleFolders(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Outgoing", nil))
	shop := "Shop <news@shop.example>"
	e.put(t, "INBOX", eml("old@x", shop, "alice@example.com", "old", day, "b"))
	e.put(t, "Sent", eml("oldre@x", shop, "alice@example.com", "Re: old", day, "b", "In-Reply-To: <old@x>"))
	e.put(t, "INBOX", eml("new@x", shop, "alice@example.com", "new", day, "b"))
	e.put(t, "Outgoing", eml("newre@x", "alice@example.com", "news@shop.example", "Re: new", day, "b", "In-Reply-To: <new@x>"))
	e.sync(t)
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "sent", e.folderID(t, "Outgoing")))

	got, err := e.s.Contacts(bg)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []int{3, 1}, []int{got[0].Received, got[0].Sent}, "the old Sent is an ordinary folder now; the chosen one counts as sent")

	res, err := e.s.ArchiveSender(bg, "news@shop.example")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Skipped, "replied to from the chosen Sent")
	assert.Equal(t, []ArchivedMessage{{e.idOf(t, "old@x"), e.folderID(t, "INBOX"), e.folderID(t, "Archive")}}, res.Moved)
}

// A contact goes by the name their mail used most, the latest of a tie: a
// notifier's own, not whoever set off its latest.
func TestContactNameMostUsed(t *testing.T) {
	s := newMailContextService(t)
	addMessage(t, s, "a", "GitHub <notifications@github.com>", "", "", 100)
	addMessage(t, s, "b", "GitHub <notifications@github.com>", "", "", 200)
	addMessage(t, s, "c", "Baran <notifications@github.com>", "", "", 300)
	addMessage(t, s, "d", "Carol <carol@x>", "", "", 100)
	addMessage(t, s, "e", "Caroline <carol@x>", "", "", 200)
	got, err := s.Contacts(t.Context())
	require.NoError(t, err)
	names := map[string]string{}
	for _, c := range got {
		names[c.Address] = c.Name
	}
	assert.Equal(t, map[string]string{"notifications@github.com": "GitHub", "carol@x": "Caroline"}, names)
}

// Mail to a contact counts and lists alike: past a page of near misses of
// their address, with nothing repeated on the next, and for an address JSON
// escapes (& is \u0026 in the stored recipients).
func TestContactMailPagesPastNearMisses(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Sent", eml("near1@x", "alice@example.com", "notbob@example.com", "a", "Fri, 09 Oct 2026 09:00:00 +0000", "b"))
	e.put(t, "Sent", eml("near2@x", "alice@example.com", "notbob@example.com", "b", "Thu, 08 Oct 2026 09:00:00 +0000", "b"))
	e.put(t, "Sent", eml("bob@x", "alice@example.com", "bob@example.com", "c", "Wed, 07 Oct 2026 09:00:00 +0000", "b"))
	e.put(t, "Sent", eml("amp@x", "alice@example.com", "a&b@example.com", "d", day, "b"))
	e.sync(t)

	page, err := e.s.ContactMail(bg, "bob@example.com", "", 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{e.idOf(t, "bob@x")}, msgIDs(page.Items))
	assert.Empty(t, page.NextCursor, "the last page")

	page, err = e.s.ContactMail(bg, "a&b@example.com", "", 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{e.idOf(t, "amp@x")}, msgIDs(page.Items))
}

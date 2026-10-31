package core

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSenders(t *testing.T) {
	e := newSyncEnv(t)
	unsub := "List-Unsubscribe: <mailto:leave@shop.example>"
	e.put(t, "INBOX", eml("s1@x", "Shop <news@shop.example>", "alice@example.com", "a", "Mon, 05 Oct 2026 09:00:00 +0000", "b", unsub))
	e.putFlagged(t, "INBOX", eml("s2@x", "Shop News <NEWS@shop.example>", "alice@example.com", "b", "Tue, 06 Oct 2026 09:00:00 +0000", "b"), imap.FlagSeen)
	e.put(t, "Archive", eml("s3@x", "news@shop.example", "alice@example.com", "c", "Sun, 04 Oct 2026 09:00:00 +0000", "b", unsub))
	e.put(t, "Trash", eml("s4@x", "news@shop.example", "alice@example.com", "d", "Wed, 07 Oct 2026 09:00:00 +0000", "b", unsub))
	e.put(t, "INBOX", eml("b1@x", "Bob <bob@example.com>", "alice@example.com", "e", "Thu, 08 Oct 2026 09:00:00 +0000", "b"))
	e.put(t, "Sent", eml("me@x", "Alice <alice@example.com>", "bob@example.com", "f", day, "b"))
	e.put(t, "INBOX", eml("me2@x", "Alice <Alice@example.com>", "alice@example.com", "g", day, "b"))
	e.sync(t)

	got, err := e.s.Senders(bg)
	require.NoError(t, err)
	require.Len(t, got, 2, "the user's own address and Trash are left out")
	shop, bob := got[0], got[1]
	assert.Equal(t, "news@shop.example", shop.Address)
	assert.Equal(t, "Shop News", shop.Name, "the latest name")
	assert.Equal(t, 3, shop.Messages)
	assert.Equal(t, 2, shop.Unread)
	assert.Equal(t, 2, shop.Inbox)
	assert.Equal(t, "2026-10-06", shop.Last.UTC().Format("2006-01-02"))
	assert.Equal(t, e.idOf(t, "s1@x"), shop.ListMessage, "the newest with List-Unsubscribe outside Trash")
	assert.Equal(t, e.threadOf(t, "s1@x"), shop.ListThread)
	assert.Equal(t, Sender{Name: "Bob", Address: "bob@example.com", Messages: 1, Unread: 1, Inbox: 1, Last: bob.Last}, bob)
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

	res, err := e.s.ArchiveSender(bg, "news@shop.example")
	require.NoError(t, err)
	assert.Equal(t, 4, res.Skipped, "starred, answered, replied from Sent, starred later in the thread")
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

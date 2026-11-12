package core

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/internal/testmail"
	gomail "github.com/godwit-email/godwit/core/mail"
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
	assert.Equal(t, Contact{Name: "Bob", Address: "bob@example.com", Addresses: []ContactAddress{{Address: "bob@example.com"}}, Messages: 3, Received: 1, Sent: 2, Unread: 1, Inbox: 1,
		Last: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC).Local(), Latest: e.idOf(t, "b1@x")}, bob, "sent to and copied, once per message")
	assert.Equal(t, Contact{Name: "Shop News", Address: "news@shop.example", Addresses: []ContactAddress{{Address: "news@shop.example", Blocked: true}}, Messages: 3, Received: 3, Unread: 2, Inbox: 2,
		Last: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC).Local(), Latest: e.idOf(t, "s2@x"), ListMessage: e.idOf(t, "s1@x"), Blocked: true}, shop,
		"the latest name; the newest with List-Unsubscribe outside Trash")
	assert.Equal(t, Contact{Name: "Chen Jing", Address: "chen@corp.example", Addresses: []ContactAddress{{Address: "chen@corp.example"}}, Messages: 1, Sent: 1,
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
	assert.Equal(t, 5, e.count(t, "SELECT count(*) FROM messages WHERE "+shownCol+" = ?", inbox))

	// One moved on since: undo leaves it where the user put it.
	trash := e.folderID(t, "Trash")
	require.NoError(t, e.s.Move(bg, []int64{e.idOf(t, "plain2@x")}, trash))
	require.NoError(t, e.s.UndoArchive(bg, res))
	assert.Equal(t, [][]any{{inbox}}, e.rows(t, "SELECT "+shownCol+" FROM messages WHERE message_id = 'plain@x'"))
	assert.Equal(t, [][]any{{trash}}, e.rows(t, "SELECT "+shownCol+" FROM messages WHERE message_id = 'plain2@x'"))
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
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE "+shownCol+" = ?", e.folderID(t, "INBOX")))
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

func TestContactsCardDAVMergePriorityAndOperations(t *testing.T) {
	SerializeKeyringForTest()
	prevRoots := conn.TLSRoots
	conn.TLSRoots = testmail.CardDAVRoots()
	t.Cleanup(func() { conn.TLSRoots = prevRoots })

	e := newSyncEnv(t)

	workSrv := testmail.StartCardDAV(t)
	workSrv.PutCollection("team", "Team")
	// Shares alice.shared@example.com with Personal; includes user's own account alice@example.com (must be stripped).
	// Lacks ORG/TITLE so Personal's ORG+TITLE should fill Subtitle.
	workSrv.PutCard("team", "alice-work", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Bob Work",
		"EMAIL;PREF=1:bob.work@godwit.example",
		"EMAIL:bob.shared@example.com",
		"EMAIL:alice@example.com",
		"END:VCARD",
	}, "\r\n"))
	// Pure own-account card must NOT create a Contact entry.
	workSrv.PutCard("team", "self-only", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Alice Self",
		"EMAIL:ALICE@example.com",
		"END:VCARD",
	}, "\r\n"))
	// Zero-mail address-book contacts.
	workSrv.PutCard("team", "zero-zara", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Zara Bell",
		"ORG:Bell Labs",
		"TITLE:Researcher",
		"EMAIL:zara@bell.example",
		"END:VCARD",
	}, "\r\n"))
	workSrv.PutCard("team", "zero-aaron", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Aaron Stone",
		"ORG:Stone Co",
		"EMAIL:aaron@stone.example",
		"END:VCARD",
	}, "\r\n"))
	// Card with neither FN nor ORG falls back to mail header frequency tally.
	workSrv.PutCard("team", "nameless-bot", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"TITLE:Automation",
		"EMAIL:bot1@notify.example",
		"EMAIL:bot2@notify.example",
		"END:VCARD",
	}, "\r\n"))

	persSrv := testmail.StartCardDAV(t)
	persSrv.PutCollection("friends", "Friends")
	persSrv.PutCollection("hidden", "Hidden")
	persSrv.PutCard("friends", "bob-home", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:Bob Personal",
		"ORG:Personal Studio",
		"TITLE:Principal",
		"EMAIL;TYPE=PREF:bob.home@me.example",
		"EMAIL:Bob.Shared@example.com",
		"END:VCARD",
	}, "\r\n"))
	persSrv.PutCard("hidden", "ghost", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Ghost User",
		"EMAIL:ghost@hidden.example",
		"END:VCARD",
	}, "\r\n"))

	abWork, err := e.s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "Work", URL: workSrv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	abPers, err := e.s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "Personal", URL: persSrv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	// Disable "hidden" collection -> ghost must not appear.
	require.NoError(t, e.s.SetAddressBookCollectionEnabled(bg, abPers.Collections[1].ID, false))

	unsub := "List-Unsubscribe: <mailto:leave@me.example>"
	e.put(t, "INBOX", eml("bh1@x", "Bobby Old <bob.home@me.example>", "alice@example.com", "one", "Mon, 05 Oct 2026 09:00:00 +0000", "b", unsub))
	e.put(t, "INBOX", eml("bs1@x", "Bob Shared <bob.shared@example.com>", "alice@example.com", "two", "Tue, 06 Oct 2026 09:00:00 +0000", "b"))
	// One Sent message addressing two of Bob's merged addresses counts Sent once.
	e.put(t, "Sent", eml("bsent@x", "alice@example.com", "bob.work@godwit.example", "three", "Wed, 07 Oct 2026 09:00:00 +0000", "b",
		"Cc: bob.home@me.example"))
	// Bot messages across two addresses on nameless-bot card.
	e.put(t, "INBOX", eml("n1@x", "Notify Bot <bot1@notify.example>", "alice@example.com", "n1", "Sun, 04 Oct 2026 09:00:00 +0000", "b"))
	e.put(t, "INBOX", eml("n2@x", "Notify Bot <bot1@notify.example>", "alice@example.com", "n2", "Sun, 04 Oct 2026 10:00:00 +0000", "b"))
	e.put(t, "INBOX", eml("n3@x", "Other Bot <bot2@notify.example>", "alice@example.com", "n3", "Sun, 04 Oct 2026 11:00:00 +0000", "b"))
	e.sync(t)

	// Block only one of Bob's 3 addresses -> partial block (Contact.Blocked == false).
	require.NoError(t, e.s.BlockSender(bg, "bob.home@me.example"))

	got, err := e.s.Contacts(bg)
	require.NoError(t, err)
	require.Len(t, got, 4, "Bob (3 msgs), Notify Bot (3 msgs), Aaron (0 msgs), Zara (0 msgs); self-only and disabled excluded")

	assert.Equal(t, "Bob Work", got[0].Name, "Work book has higher priority")
	assert.Equal(t, "Personal Studio · Principal", got[0].Subtitle, "missing ORG/TITLE on Work filled from Personal")
	assert.True(t, got[0].FromAddressBook)
	assert.False(t, got[0].Blocked, "only 1 of 3 addresses blocked")
	assert.Equal(t, "bob.work@godwit.example", got[0].Address)
	assert.Equal(t, []ContactAddress{
		{Address: "bob.work@godwit.example", Pref: true, Blocked: false},
		{Address: "bob.home@me.example", Pref: false, Blocked: true},
		{Address: "bob.shared@example.com", Pref: false, Blocked: false},
	}, got[0].Addresses)
	assert.Equal(t, 3, got[0].Messages)
	assert.Equal(t, 2, got[0].Received)
	assert.Equal(t, 1, got[0].Sent, "deduplicated across To+Cc of same contact")
	assert.Equal(t, 2, got[0].Unread)
	assert.Equal(t, 2, got[0].Inbox)
	assert.Equal(t, e.idOf(t, "bs1@x"), got[0].Latest)
	assert.Equal(t, e.idOf(t, "bh1@x"), got[0].ListMessage)

	// Nameless card falls back to mail frequency tally for Name, keeps card TITLE for Subtitle.
	assert.Equal(t, "Notify Bot", got[1].Name)
	assert.Equal(t, "Automation", got[1].Subtitle)
	assert.True(t, got[1].FromAddressBook)

	// Zero-mail contacts come AFTER all contacts with mail, with zero Last, sorted by Name.
	assert.Equal(t, "Aaron Stone", got[2].Name)
	assert.Equal(t, "Stone Co", got[2].Subtitle)
	assert.Zero(t, got[2].Messages)
	assert.True(t, got[2].Last.IsZero())
	assert.Equal(t, "Zara Bell", got[3].Name)
	assert.Equal(t, "Bell Labs · Researcher", got[3].Subtitle)
	assert.Zero(t, got[3].Messages)
	assert.True(t, got[3].Last.IsZero())

	// Reorder Personal ahead of Work -> Name and primary PREF address flip to Personal's card.
	require.NoError(t, e.s.ReorderAddressBooks(bg, []int64{abPers.ID, abWork.ID}))
	got, err = e.s.Contacts(bg)
	require.NoError(t, err)
	assert.Equal(t, "Bob Personal", got[0].Name)
	assert.Equal(t, "bob.home@me.example", got[0].Address)
	assert.Equal(t, []ContactAddress{
		{Address: "bob.home@me.example", Pref: true, Blocked: true},
		{Address: "bob.work@godwit.example", Pref: false, Blocked: false},
		{Address: "bob.shared@example.com", Pref: false, Blocked: false},
	}, got[0].Addresses)

	// Blocking all 3 addresses makes Contact.Blocked true.
	require.NoError(t, e.s.BlockSender(bg, "bob.work@godwit.example"))
	require.NoError(t, e.s.BlockSender(bg, "bob.shared@example.com"))
	got, err = e.s.Contacts(bg)
	require.NoError(t, err)
	assert.True(t, got[0].Blocked)

	// ContactDetail, ContactMail, and ArchiveSender passed any sub-address cover the whole merged component.
	d, err := e.s.ContactDetail(bg, "bob.work@godwit.example")
	require.NoError(t, err)
	assert.Equal(t, ContactDetail{Archivable: 2}, d)

	mp, err := e.s.ContactMail(bg, "bob.work@godwit.example", "", 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{e.idOf(t, "bsent@x"), e.idOf(t, "bs1@x"), e.idOf(t, "bh1@x")}, msgIDs(mp.Items))

	arch, err := e.s.ArchiveSender(bg, "bob.work@godwit.example")
	require.NoError(t, err)
	assert.Len(t, arch.Moved, 2)
	d, err = e.s.ContactDetail(bg, "bob.home@me.example")
	require.NoError(t, err)
	assert.Equal(t, ContactDetail{Archivable: 0}, d)
}

func TestBareFromAddressBookFallback(t *testing.T) {
	SerializeKeyringForTest()
	prevRoots := conn.TLSRoots
	conn.TLSRoots = testmail.CardDAVRoots()
	t.Cleanup(func() { conn.TLSRoots = prevRoots })

	e := newSyncEnv(t)

	srv1 := testmail.StartCardDAV(t)
	srv1.PutCollection("main", "Main")
	// Even a brand name like "PayPal" in FN must NOT mutate m.From or trigger SecurityFraud.
	srv1.PutCard("main", "bare-paypal", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:PayPal Billing",
		"EMAIL:bare@vendor.example",
		"END:VCARD",
	}, "\r\n"))
	srv1.PutCard("main", "same-as-addr", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Echo Person",
		"EMAIL:echo@vendor.example",
		"END:VCARD",
	}, "\r\n"))
	srv1.PutCard("main", "github-static", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:GitHub Static",
		"EMAIL:notify@codehost.example",
		"END:VCARD",
	}, "\r\n"))

	srv2 := testmail.StartCardDAV(t)
	srv2.PutCollection("secondary", "Secondary")
	srv2.PutCard("secondary", "bare-alt", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Vendor Alt",
		"EMAIL:bare@vendor.example",
		"END:VCARD",
	}, "\r\n"))

	ab1, err := e.s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "Primary", URL: srv1.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	ab2, err := e.s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "Secondary", URL: srv2.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)

	// 1. Bare From address -> falls back to highest-priority address book FN.
	e.put(t, "INBOX", eml("bare@x", "bare@vendor.example", "alice@example.com", "Invoice 1", "Mon, 05 Oct 2026 09:00:00 +0000", "body text"))
	// 2. Display name equal to email address (case-insensitively) -> treated as bare, falls back to address book FN.
	e.put(t, "INBOX", eml("echo@x", `"ECHO@vendor.example" <echo@vendor.example>`, "alice@example.com", "Echo msg", "Tue, 06 Oct 2026 09:00:00 +0000", "body text"))
	// 3. Explicit display name -> MUST NOT be overwritten by address book FN.
	e.put(t, "INBOX", eml("gh@x", `"Alice (via GitHub)" <notify@codehost.example>`, "alice@example.com", "PR review", "Wed, 07 Oct 2026 09:00:00 +0000", "body text"))
	// 4. Bare From NOT in any address book -> FromName stays "", Participants shows bare address.
	e.put(t, "INBOX", eml("stranger@x", "stranger@unknown.example", "alice@example.com", "Hello", "Thu, 08 Oct 2026 09:00:00 +0000", "body text"))
	e.sync(t)

	// Check ListThreads Participants (newest first: stranger, gh, echo, bare).
	tp, err := e.s.ListThreads(bg, View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, tp.Items, 4)
	assert.Equal(t, []string{"stranger@unknown.example"}, tp.Items[0].Participants)
	assert.Equal(t, []string{"Alice (via GitHub)"}, tp.Items[1].Participants, "explicit From display name preserved")
	assert.Equal(t, []string{"Echo Person"}, tp.Items[2].Participants, "address-as-name treated as bare")
	assert.Equal(t, []string{"PayPal Billing"}, tp.Items[3].Participants, "bare From uses highest-priority card FN")
	assert.Equal(t, "bare@vendor.example", tp.Items[3].From, "ThreadSummary.From stays raw")

	// Check ListMessages FromName and raw From preservation.
	lp, err := e.s.ListMessages(bg, View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, lp.Items, 4)
	assert.Equal(t, "", lp.Items[0].FromName)
	assert.Equal(t, "Alice (via GitHub)", lp.Items[1].FromName)
	assert.Equal(t, "Echo Person", lp.Items[2].FromName)
	assert.Equal(t, "PayPal Billing", lp.Items[3].FromName)
	assert.Equal(t, "bare@vendor.example", lp.Items[3].From, "MessageSummary.From never mutated")
	assert.NotEqual(t, gomail.SecurityFraud, gomail.EvaluateSecurity(lp.Items[3].From, "").Status)

	// Check GetThread, GetMessage, GetMessageLazyImages, SearchWith, ContactMail.
	bareID := e.idOf(t, "bare@x")
	th, err := e.s.GetThread(bg, e.threadOf(t, "bare@x"))
	require.NoError(t, err)
	require.Len(t, th.Messages, 1)
	assert.Equal(t, "PayPal Billing", th.Messages[0].FromName)
	assert.Equal(t, "bare@vendor.example", th.Messages[0].From)

	gm, err := e.s.GetMessage(bg, bareID, false)
	require.NoError(t, err)
	assert.Equal(t, "PayPal Billing", gm.FromName)
	assert.Equal(t, "bare@vendor.example", gm.From)

	gmLazy, err := e.s.GetMessageLazyImages(bg, bareID, false)
	require.NoError(t, err)
	assert.Equal(t, "PayPal Billing", gmLazy.FromName)

	found, err := e.s.SearchWith(bg, SearchQuery{Text: "Invoice"}, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "PayPal Billing", found[0].FromName)

	cm, err := e.s.ContactMail(bg, "bare@vendor.example", "", 10)
	require.NoError(t, err)
	require.Len(t, cm.Items, 1)
	assert.Equal(t, "PayPal Billing", cm.Items[0].FromName)

	// Reordering Secondary ahead of Primary switches fallback to "Vendor Alt".
	require.NoError(t, e.s.ReorderAddressBooks(bg, []int64{ab2.ID, ab1.ID}))
	gm2, err := e.s.GetMessageLazyImages(bg, bareID, false)
	require.NoError(t, err)
	assert.Equal(t, "Vendor Alt", gm2.FromName)

	// Disabling collections clears fallback cleanly.
	require.NoError(t, e.s.SetAddressBookCollectionEnabled(bg, ab2.Collections[0].ID, false))
	require.NoError(t, e.s.SetAddressBookCollectionEnabled(bg, ab1.Collections[0].ID, false))
	gm3, err := e.s.GetMessageLazyImages(bg, bareID, false)
	require.NoError(t, err)
	assert.Equal(t, "", gm3.FromName)
}

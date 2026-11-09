package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func (e *syncEnv) inboxView(t *testing.T) View {
	t.Helper()
	fs, err := e.s.ListFolders(bg, e.acc.ID)
	require.NoError(t, err)
	for _, f := range fs {
		if f.Role == "inbox" {
			return View{FolderID: f.ID}
		}
	}
	t.Fatal("no inbox")
	return View{}
}

func (e *syncEnv) second(t *testing.T) *syncEnv {
	t.Helper()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	acc, err := e.s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "carol@example.com", false))
	require.NoError(t, err)
	return &syncEnv{s: e.s, im: im, acc: acc, dir: e.dir}
}

func TestListThreadsPaging(t *testing.T) {
	e := newSyncEnv(t)
	days := []string{"01", "02", "02", "04", "05"} // threads 2 and 3 tie on last_date
	for i, d := range days {
		id := string(rune('b' + i))
		e.put(t, "INBOX", eml(id+"@x", "Bob <bob@example.com>", "alice@example.com", "s"+id, "Mon, "+d+" Oct 2026 09:00:00 +0000", "b"))
	}
	e.put(t, "INBOX", eml("re@x", "Dan <dan@example.com>", "alice@example.com", "Re: sb", "Tue, 06 Oct 2026 09:00:00 +0000", "b", "In-Reply-To: <b@x>"))
	e.sync(t)
	v := e.inboxView(t)
	var want []int64
	for _, r := range e.rows(t, "SELECT id FROM threads ORDER BY last_date DESC, id DESC") {
		want = append(want, r[0].(int64))
	}
	var got []int64
	cur := ""
	sizes := []int{}
	for {
		p, err := e.s.ListThreads(bg, v, cur, 2)
		require.NoError(t, err)
		sizes = append(sizes, len(p.Items))
		for _, it := range p.Items {
			got = append(got, it.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cur = p.NextCursor
	}
	assert.Equal(t, []int{2, 2, 1}, sizes)
	assert.Equal(t, want, got) // full order, no duplicates, ties broken by id DESC
	assert.Equal(t, e.threadOf(t, "b@x"), got[0])
	p, _ := e.s.ListThreads(bg, v, "", 50)
	assert.Equal(t, 2, p.Items[0].Count)
	assert.Equal(t, 2, p.Items[0].Unread)
	assert.Equal(t, []string{"Dan", "Bob"}, p.Items[0].Participants)

	e2 := e.second(t)
	e2.put(t, "INBOX", eml("c1@x", "Eve <eve@example.com>", "carol@example.com", "other", "Mon, 05 Oct 2026 09:00:00 +0000", "b"))
	e2.sync(t)
	u, err := e.s.ListThreads(bg, View{Unified: "inbox"}, "", 50)
	require.NoError(t, err)
	assert.Len(t, u.Items, 6)
	_, err = e.s.ListThreads(bg, v, "!!bad", 5)
	assert.Error(t, err)
	_, err = e.s.ListThreads(bg, View{}, "", 5)
	assert.Error(t, err)
}

func TestViewStarredAccount(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("a1@x", "Bob <bob@example.com>", "alice@example.com", "a1", "Mon, 05 Oct 2026 09:00:00 +0000", "b1"))
	e.sync(t)
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "a1@x")}, []string{`\Flagged`}, nil))

	e2 := e.second(t)
	e2.put(t, "INBOX", eml("c1@x", "Eve <eve@example.com>", "carol@example.com", "c1", "Mon, 05 Oct 2026 09:00:00 +0000", "b2"))
	e2.sync(t)
	require.NoError(t, e.s.SetFlags(bg, []int64{e2.idOf(t, "c1@x")}, []string{`\Flagged`}, nil))

	all, err := e.s.ListMessages(bg, View{Unified: "starred"}, "", 50)
	require.NoError(t, err)
	assert.Len(t, all.Items, 2)

	acc1View := View{Unified: fmt.Sprintf("starred:%d", e.acc.ID)}
	acc1, err := e.s.ListMessages(bg, acc1View, "", 50)
	require.NoError(t, err)
	assert.Len(t, acc1.Items, 1)
	assert.Equal(t, e.idOf(t, "a1@x"), acc1.Items[0].ID)

	acc2View := View{Unified: fmt.Sprintf("starred:%d", e2.acc.ID)}
	acc2, err := e.s.ListMessages(bg, acc2View, "", 50)
	require.NoError(t, err)
	assert.Len(t, acc2.Items, 1)
	assert.Equal(t, e2.idOf(t, "c1@x"), acc2.Items[0].ID)

	_, err = e.s.ListMessages(bg, View{Unified: "starred:invalid"}, "", 50)
	assert.Error(t, err)
}

func TestGetThreadOrder(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", "Mon, 05 Oct 2026 09:00:00 +0000", "q"))
	e.put(t, "Sent", eml("r@x", "alice@example.com", "bob@example.com", "Re: plan", "Mon, 05 Oct 2026 10:00:00 +0000", "a", "In-Reply-To: <o@x>"))
	e.put(t, "INBOX", eml("r2@x", "Bob <bob@example.com>", "alice@example.com", "Re: plan", "Mon, 05 Oct 2026 08:00:00 +0000", "early", "In-Reply-To: <o@x>"))
	e.sync(t)
	th, err := e.s.GetThread(bg, e.threadOf(t, "o@x"))
	require.NoError(t, err)
	require.Len(t, th.Messages, 3)
	assert.Equal(t, "plan", th.Subject)
	assert.Equal(t, "early", th.Messages[0].Snippet)
	assert.Equal(t, "a", th.Messages[2].Snippet)
	assert.Equal(t, []string{"bob@example.com"}, th.Messages[2].To)
}

func TestThreadSenderSkipsOwnReplies(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("original@x", "Bob <bob@example.com>", "alice@example.com", "plan", day, "question"))
	reply := eml("reply@x", "Alice <ALICE@example.com>", "bob@example.com", "Re: plan",
		"Tue, 06 Oct 2026 10:00:00 +0000", "reply", "In-Reply-To: <original@x>")
	e.put(t, "Sent", reply)
	e.put(t, "INBOX", reply)
	e.sync(t)
	page, err := e.s.ListThreads(bg, e.inboxView(t), "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "Bob <bob@example.com>", page.Items[0].From)
	assert.Equal(t, "reply", page.Items[0].Snippet)
}

// Mail you sent is never unread, even when the server never flagged it \Seen
// (another client, an import): the thread counts the rest, and GetThread shows
// it read without writing \Seen. A copy of it outside Sent, a self-Cc in the
// inbox, is a message of its own, unread.
func TestThreadUnreadSkipsSent(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", "Mon, 05 Oct 2026 09:00:00 +0000", "q"))
	mine := eml("r@x", "alice@example.com", "bob@example.com", "Re: plan", "Mon, 05 Oct 2026 10:00:00 +0000", "a", "In-Reply-To: <o@x>", "Cc: alice@example.com")
	e.put(t, "Sent", mine)
	e.put(t, "INBOX", mine)
	e.sync(t)
	p, err := e.s.ListThreads(bg, View{Unified: "inbox"}, "", 50)
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	assert.Equal(t, 3, p.Items[0].Count)
	assert.Equal(t, 2, p.Items[0].Unread) // Bob's and the inbox copy, not yours in Sent
	assert.Equal(t, "a", p.Items[0].Snippet)

	th, err := e.s.GetThread(bg, p.Items[0].ID)
	require.NoError(t, err)
	seen := map[int64]bool{}
	for _, m := range th.Messages {
		seen[m.FolderID] = seen[m.FolderID] || slices.Contains(m.Flags, `\Seen`)
	}
	assert.Equal(t, map[int64]bool{e.folderID(t, "INBOX"): false, e.folderID(t, "Sent"): true}, seen)
	sent, err := e.s.ListMessages(bg, View{FolderID: e.folderID(t, "Sent")}, "", 50)
	require.NoError(t, err)
	require.Len(t, sent.Items, 1)
	assert.NotContains(t, sent.Items[0].Flags, `\Seen`, "nothing written")
	assert.Equal(t, []string{"r@x"}, e.onServer(t, "Sent"))
}

// The Sent role is the one the user gave: a folder detected as Sent but
// given another role counts its unread mail, the one given Sent does not.
func TestThreadUnreadSkipsGivenSent(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Outgoing", nil))
	e.put(t, "Sent", eml("in@x", "Bob <bob@example.com>", "alice@example.com", "kept", day, "q"))
	e.put(t, "Outgoing", eml("out@x", "alice@example.com", "carol@example.com", "hi", day, "a"))
	e.sync(t)
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "sent", e.folderID(t, "Outgoing")))
	unread := map[string]int{}
	for _, f := range []string{"Sent", "Outgoing"} {
		p, err := e.s.ListThreads(bg, View{FolderID: e.folderID(t, f)}, "", 50)
		require.NoError(t, err)
		require.Len(t, p.Items, 1)
		unread[p.Items[0].Subject] = p.Items[0].Unread
	}
	assert.Equal(t, map[string]int{"kept": 1, "hi": 0}, unread)
}

// On a regular server a message COPY made into another folder is a message of
// its own, though it shares the Message-ID: the thread lists it, with its own
// flags, and an action on one leaves the other alone.
func TestThreadCopiesAreSeparateMessages(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Work", nil))
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", "Mon, 05 Oct 2026 09:00:00 +0000", "q"))
	c := e.dial(t)
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	_, err = c.Copy(imap.UIDSetNum(1), "Work").Wait()
	require.NoError(t, err)
	e.sync(t)

	tid := store.Int(e.rows(t, "SELECT DISTINCT thread_id FROM messages WHERE message_id = 'o@x'")[0][0])
	th, err := e.s.GetThread(bg, tid)
	require.NoError(t, err)
	require.Len(t, th.Messages, 2)
	inbox, work := th.Messages[0], th.Messages[1]
	assert.Equal(t, []int64{e.folderID(t, "INBOX")}, inbox.FolderIDs)
	assert.Equal(t, []int64{e.folderID(t, "Work")}, work.FolderIDs)

	require.NoError(t, e.s.SetFlags(bg, []int64{work.ID}, []string{`\Flagged`}, nil))
	e.sync(t)
	assert.Equal(t, []string{"o@x"}, e.onServer(t, "INBOX"))
	assert.Equal(t, []string{`o@x \Flagged`}, e.onServer(t, "Work"))
	p, err := e.s.ListThreads(bg, e.inboxView(t), "", 50)
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	assert.Equal(t, []int{2, 2, 1}, []int{p.Items[0].Count, p.Items[0].Unread, p.Items[0].Flagged})
	_, ids, members, err := e.s.mailTarget(bg, "thread", tid)
	require.NoError(t, err)
	assert.Equal(t, []int64{inbox.ID, work.ID}, ids)
	assert.Len(t, members, 2)

	require.NoError(t, e.s.MoveToRole(bg, []int64{inbox.ID}, "archive"))
	e.sync(t)
	assert.Equal(t, []string{`o@x \Flagged`}, e.onServer(t, "Work"), "the copy stays")
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM messages"))
}

// A Gmail message in INBOX, a label and All Mail is one message of its thread,
// listed with every folder; its flags and warning are the message's, and the
// same mail in another account stays that account's.
func TestGmailThreadShowsMessageOnce(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Create("Work", nil))
	e.im.AddGmail(eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", "Mon, 05 Oct 2026 09:00:00 +0000", "q"), nil, testmail.GmailAllMail, "Work", "INBOX")
	e.im.AddGmail(eml("r@x", "Dan <dan@example.com>", "alice@example.com", "Re: plan", "Mon, 05 Oct 2026 10:00:00 +0000", "a", "In-Reply-To: <o@x>"), nil, testmail.GmailAllMail, "Work")
	e.sync(t)
	tid := store.Int(e.rows(t, "SELECT DISTINCT thread_id FROM messages WHERE message_id = 'o@x'")[0][0])

	th, err := e.s.GetThread(bg, tid)
	require.NoError(t, err)
	require.Len(t, th.Messages, 2)
	inbox, work, all := e.folderID(t, "INBOX"), e.folderID(t, "Work"), e.folderID(t, testmail.GmailAllMail)
	assert.Equal(t, inbox, th.Messages[0].FolderID)
	assert.ElementsMatch(t, []int64{inbox, work, all}, th.Messages[0].FolderIDs)
	assert.Equal(t, work, th.Messages[1].FolderID, "a label before All Mail")
	assert.ElementsMatch(t, []int64{work, all}, th.Messages[1].FolderIDs)
	p, err := e.s.ListMessages(bg, View{FolderID: all}, "", 50)
	require.NoError(t, err)
	require.Len(t, p.Items, 2)
	assert.Equal(t, []int64{all, all}, []int64{p.Items[0].FolderID, p.Items[1].FolderID}, "a folder's list reports that folder")

	require.NoError(t, e.s.SetFlags(bg, []int64{th.Messages[1].ID}, []string{`\Flagged`}, nil))
	require.NoError(t, e.s.SetMessageSecurity(bg, []int64{th.Messages[0].ID}, "manual_warning"))
	l, err := e.s.ListThreads(bg, e.inboxView(t), "", 50)
	require.NoError(t, err)
	require.Len(t, l.Items, 1)
	assert.Equal(t, []int{2, 2, 1}, []int{l.Items[0].Count, l.Items[0].Unread, l.Items[0].Flagged})
	assert.Equal(t, []string{"Dan", "Bob"}, l.Items[0].Participants)
	assert.Equal(t, gomail.DBToSecurityReport("Bob <bob@example.com>", "", "warning", "manual_warning").Status, l.Items[0].Security.Status)
	_, ids, _, err := e.s.mailTarget(bg, "thread", tid)
	require.NoError(t, err)
	assert.Equal(t, msgIDs(th.Messages), ids, "AI reads each message once")

	other := e.second(t) // the same mail in another account's inbox is that account's own
	other.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", "Mon, 05 Oct 2026 09:00:00 +0000", "q"))
	other.sync(t)
	u, err := e.s.ListThreads(bg, View{Unified: "inbox"}, "", 50)
	require.NoError(t, err)
	assert.Len(t, u.Items, 2)
}

// Gmail mail you sent yourself, in INBOX and Sent, reads as read.
func TestGmailSelfSentReadsAsRead(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	e.im.AddGmail(eml("self@x", "alice@example.com", "alice@example.com", "note", day, "b"), nil, testmail.GmailSent, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	p, err := e.s.ListThreads(bg, e.inboxView(t), "", 50)
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	assert.Zero(t, p.Items[0].Unread)
	th, err := e.s.GetThread(bg, p.Items[0].ID)
	require.NoError(t, err)
	assert.Contains(t, th.Messages[0].Flags, `\Seen`)
}

func TestGetMessageSanitized(t *testing.T) {
	e := newSyncEnv(t)
	raw, err := os.ReadFile("mail/testdata/hostile.eml")
	require.NoError(t, err)
	e.put(t, "INBOX", string(raw))
	e.sync(t)
	r := e.rows(t, "SELECT id FROM messages")
	m, err := e.s.GetMessage(bg, r[0][0].(int64), false)
	require.NoError(t, err)
	assert.Equal(t, 4, m.RemoteImagesBlocked)
	for _, bad := range []string{"<script", "javascript:", "onerror", "<iframe", " href="} {
		assert.NotContains(t, strings.ToLower(m.HTML), bad)
	}
}

// A tracking pixel is dropped and counted apart from the remote images,
// even for a sender whose images are trusted.
func TestGetMessageTrackers(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", htmlEml("p@x", `<p>hi</p><img src="https://img.example/logo.png" width="120" height="40"><img src="https://t.example/o.gif" width="1" height="1">`))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 1, m.RemoteImagesBlocked)
	assert.Equal(t, 1, m.TrackersBlocked)

	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.RemoteImages = "trusted"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	require.NoError(t, e.s.TrustImageSender(bg, "bob@example.com"))
	allowed, err := e.s.ImagesAllowed(bg, id)
	require.NoError(t, err)
	require.True(t, allowed)
	m, err = e.s.GetMessage(bg, id, allowed)
	require.NoError(t, err)
	assert.Contains(t, m.HTML, "logo.png")
	assert.NotContains(t, m.HTML, "o.gif")
	assert.Equal(t, 1, m.TrackersBlocked)
}

func TestGetMessageInlineImage(t *testing.T) {
	e := newSyncEnv(t)
	raw, err := os.ReadFile("mail/testdata/inline-image.eml")
	require.NoError(t, err)
	e.put(t, "INBOX", string(raw))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Contains(t, m.HTML, "data:image/")
	assert.NotZero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
}

// inlineImagesMail has an inline image per cid, each given by its base64 body.
func inlineImagesMail(cids []string, bodies map[string]string) string {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6\r\nkgAAAABJRU5ErkJggg=="
	raw := "From: Alice <alice@example.com>\r\nTo: Bob <bob@example.com>\r\nDate: Wed, 08 Oct 2026 10:00:00 +0000\r\n" +
		"Message-ID: <imgs@example.com>\r\nMIME-Version: 1.0\r\nSubject: Logos\r\nContent-Type: multipart/related; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>hi</p>"
	for _, cid := range cids {
		raw += "<img src=\"cid:" + cid + "\">"
	}
	raw += "\r\n"
	for _, cid := range cids {
		body := png
		if b, ok := bodies[cid]; ok {
			body = b
		}
		raw += "--b1\r\nContent-Type: image/png\r\nContent-ID: <" + cid + ">\r\nContent-Disposition: inline; filename=\"" + cid +
			".png\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + body + "\r\n"
	}
	return raw + "--b1--\r\n"
}

// A message's inline images download over one connection, not one login each:
// every login is a TLS handshake and a SELECT, so a signature with a few logos
// made the first open take seconds.
func TestGetMessageInlineImagesOneLogin(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	before := e.im.Logins()
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
	assert.Equal(t, before+1, e.im.Logins(), "one login for all inline images")
	// Cached: opening again needs no connection.
	_, err = e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, before+1, e.im.Logins())
}

// The lazy form reads no image and needs no connection; InlineImages then gets them
// all over one.
func TestGetMessageLazyImagesThenInlineImages(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	before := e.im.Logins()
	m, err := e.s.GetMessageLazyImages(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, before, e.im.Logins(), "the body needs no connection")
	assert.Zero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
	assert.Zero(t, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, 3, strings.Count(m.HTML, `src="cid:`))
	assert.Len(t, m.Attachments, 3)

	var got []string
	require.NoError(t, e.s.InlineImages(bg, id, func(cid string, a gomail.Attachment) {
		got = append(got, cid)
		assert.NotEmpty(t, a.Data)
	}))
	assert.Equal(t, []string{"a", "b", "c"}, got)
	assert.Equal(t, before+1, e.im.Logins())
}

// A connection lost between two images costs a new login, not the other images.
func TestGetMessageInlineImagesSurviveLostConnection(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	before := e.im.Logins()
	e.im.DropFetchAfter(1)
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, before+2, e.im.Logins())
}

// One image the server cannot give stays blocked; those after it still show.
func TestGetMessageInlineImageFailureKeepsTheOthers(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	_, err := e.s.db.Execute(bg, "UPDATE attachments SET part = '9' WHERE content_id LIKE '%b%'")
	require.NoError(t, err)
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(m.HTML, "data:image/"))
}

// An image past the limit, whatever the row said, costs itself only: those after
// it still show, and the next open does not download it again.
func TestGetMessageInlineImageTooLargeKeepsTheOthers(t *testing.T) {
	e := newSyncEnv(t)
	big := base64.StdEncoding.EncodeToString(make([]byte, 4<<10))
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, map[string]string{"b": big}))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	_, err := e.s.db.Execute(bg, "UPDATE attachments SET size = 1") // understated metadata
	require.NoError(t, err)
	old := attachLimit
	attachLimit = 1000
	t.Cleanup(func() { attachLimit = old })
	before := e.im.Logins()
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, before+2, e.im.Logins(), "the cut connection is replaced, the big image not tried again")
	before = e.im.Logins()
	m, err = e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, before, e.im.Logins(), "known too large, the rest cached: no connection")
}

// Whoever takes the account lock between two files, even long after the last one,
// finds the fetcher's connection closed: a server may limit an account's connections.
func TestAttachmentFetcherConnectionClosedForNextLockHolder(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 3))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	before := e.im.Logins()
	f := e.s.newAttachmentFetcher(bg)
	_, _, err := f.get(ids[0][0].(int64))
	require.NoError(t, err)
	_, _, err = f.get(ids[1][0].(int64))
	require.NoError(t, err)
	assert.Equal(t, before+1, e.im.Logins(), "no one came between: the connection is reused")
	kept := e.s.kept[e.acc.ID]
	require.NotNil(t, kept)
	unlock, err := e.s.lockAccount(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Nil(t, e.s.kept[e.acc.ID])
	_, err = kept.recv.Find(bg, "INBOX", "a1@x", 0)
	assert.Error(t, err, "closed once the lock was taken")
	unlock()
	_, _, err = f.get(ids[2][0].(int64))
	require.NoError(t, err)
	assert.Equal(t, before+2, e.im.Logins())
	require.NoError(t, f.close())
	assert.Nil(t, e.s.kept[e.acc.ID], "close leaves nothing kept")
}

// Eviction, after the last image, makes room around all of them.
func TestGetMessageInlineImagesEvictAroundThemselves(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 1))
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	old, _, err := e.s.GetAttachment(bg, e.rows(t, "SELECT id FROM attachments WHERE filename = 'f0.pdf'")[0][0].(int64))
	require.NoError(t, err)
	setFor(t, &cacheLimit, 1)
	id := e.rows(t, "SELECT id FROM messages WHERE subject = 'Logos'")[0][0].(int64)
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
	_, err = os.Stat(old)
	assert.True(t, os.IsNotExist(err))
}

// A call cancelled after some downloads still keeps the cache within budget.
func TestAttachmentFetcherEvictsAfterCancel(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	old, _, err := e.s.GetAttachment(bg, ids[0][0].(int64))
	require.NoError(t, err)
	setFor(t, &cacheLimit, 1)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	f := e.s.newAttachmentFetcher(ctx)
	_, _, err = f.get(ids[1][0].(int64))
	require.NoError(t, err)
	cancel()
	require.NoError(t, f.close())
	_, err = os.Stat(old)
	assert.True(t, os.IsNotExist(err))
}

// Eviction outside the account lock waits for a save, and a save for it, so a file
// saved right after its row cleared is not removed by that eviction.
func TestEvictAndSaveExcludeEachOther(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	p, _, err := e.s.GetAttachment(bg, ids[0][0].(int64))
	require.NoError(t, err)

	e.s.cacheMu.Lock()
	evicted := make(chan error, 1)
	go func() { evicted <- e.s.evict(bg, 0) }()
	saved := make(chan error, 1)
	go func() { _, _, err := e.s.GetAttachment(bg, ids[1][0].(int64)); saved <- err }()
	time.Sleep(100 * time.Millisecond)
	_, err = os.Stat(p)
	assert.NoError(t, err, "eviction waits")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"), "neither has changed a row")
	e.s.cacheMu.Unlock()
	require.NoError(t, <-evicted)
	require.NoError(t, <-saved)
}

// Eviction reads its victims under cacheMu: a file whose row changed while it
// waited for the lock is not removed on what the row said before.
func TestEvictReadsVictimsUnderCacheLock(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 1))
	e.sync(t)
	aid := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	p, _, err := e.s.GetAttachment(bg, aid)
	require.NoError(t, err)
	e.s.cacheMu.Lock()
	evicted := make(chan error, 1)
	go func() { evicted <- e.s.evict(bg, 0) }()
	time.Sleep(100 * time.Millisecond)
	_, err = e.s.db.Execute(bg, "UPDATE attachments SET cache_path = NULL WHERE id = ?", aid)
	require.NoError(t, err)
	e.s.cacheMu.Unlock()
	require.NoError(t, <-evicted)
	_, err = os.Stat(p)
	assert.NoError(t, err)
}

// A refused login is not tried again for every image.
func TestGetMessageInlineImagesStopAtRefusedLogin(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	dials := 0
	setFor(t, &afterAccountRead, func(int64) { dials++ })
	e.im.SetPassword("changed")
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Zero(t, strings.Count(m.HTML, "data:image/"))
	assert.Equal(t, 1, dials)
}

// Whoever waits for the account lock goes between two images, as it did when each
// was its own call, and no connection stays open behind its back.
func TestGetMessageInlineImagesGiveWayToLockWaiter(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineImagesMail([]string{"a", "b", "c"}, nil))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	before := e.im.Logins()
	unlock, err := e.s.lockAccount(bg, e.acc.ID)
	require.NoError(t, err)
	queuing := make(chan struct{}, 2)
	setFor(t, &beforeLockWait, func(int64) { queuing <- struct{}{} })
	queued := func() { <-queuing; time.Sleep(50 * time.Millisecond) } // from the hook to the lock's queue
	type result struct {
		m   Message
		err error
	}
	done := make(chan result, 1)
	go func() { m, err := e.s.GetMessage(bg, id, false); done <- result{m, err} }()
	queued() // the read first
	waiter := make(chan int, 1)
	go func() {
		release, err := e.s.lockAccount(bg, e.acc.ID)
		if err != nil {
			waiter <- -1
			return
		}
		defer release()
		waiter <- e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL")
	}()
	queued() // the waiter behind it
	unlock()
	assert.Equal(t, 1, <-waiter, "the waiter goes after the first image")
	r := <-done
	require.NoError(t, r.err)
	assert.Equal(t, 3, strings.Count(r.m.HTML, "data:image/"))
	assert.Equal(t, before+2, e.im.Logins(), "hung up for the waiter, dialed again after it")
}

func TestSearchChineseBodyOnly(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("z@x", "finance@x", "alice@example.com", "通知", "Mon, 05 Oct 2026 09:00:00 +0000", "请查收本月发票和报销单"))
	e.sync(t)
	for q, n := range map[string]int{"发票": 1, "报销单": 1, "不存在": 0, "a%": 0} {
		got, err := e.s.Search(bg, e.acc.ID, q, 10)
		require.NoError(t, err)
		assert.Len(t, got, n, q)
	}
}

func TestSearchQueryIsNotFTSSyntax(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.s.Search(bg, 0, `"unbalanced AND (`, 10)
	assert.NoError(t, err)
}

func TestSearchAllAccounts(t *testing.T) {
	e := newSyncEnv(t)
	e2 := e.second(t)
	e.put(t, "INBOX", eml("s1@x", "a@x", "alice@example.com", "n", "Mon, 05 Oct 2026 09:00:00 +0000", "发票"))
	e2.put(t, "INBOX", eml("s2@x", "a@x", "carol@example.com", "n", "Mon, 05 Oct 2026 09:00:00 +0000", "发票"))
	e.sync(t)
	e2.sync(t)
	got, err := e.s.Search(bg, 0, "发票", 10)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	got, _ = e.s.Search(bg, e.acc.ID, "发票", 10)
	assert.Len(t, got, 1)
}

func TestListMessagesFlat(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("m1@x", "bob@example.com", "alice@example.com", "t", "Mon, 05 Oct 2026 09:00:00 +0000", "1"))
	e.put(t, "INBOX", eml("m2@x", "bob@example.com", "alice@example.com", "Re: t", "Mon, 05 Oct 2026 10:00:00 +0000", "2", "In-Reply-To: <m1@x>"))
	e.put(t, "INBOX", eml("m3@x", "bob@example.com", "alice@example.com", "Re: t", "Mon, 05 Oct 2026 11:00:00 +0000", "3", "In-Reply-To: <m2@x>"))
	e.sync(t)
	v := e.inboxView(t)
	p1, err := e.s.ListMessages(bg, v, "", 2)
	require.NoError(t, err)
	require.Len(t, p1.Items, 2)
	assert.Equal(t, "3", p1.Items[0].Snippet)
	p2, err := e.s.ListMessages(bg, v, p1.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, p2.Items, 1)
	assert.Equal(t, "1", p2.Items[0].Snippet)
	assert.Empty(t, p2.NextCursor)
}

func TestGetAttachmentLazy(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 1))
	e.sync(t)
	aid := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	require.Zero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
	path, info, err := e.s.GetAttachment(bg, aid)
	require.NoError(t, err)
	assert.Equal(t, "f0.pdf", info.Filename)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.4\n", string(b))
	assert.Equal(t, path, e.rows(t, "SELECT cache_path FROM attachments")[0][0])
	e.im.Stop()
	path2, _, err := e.s.GetAttachment(bg, aid)
	require.NoError(t, err)
	assert.Equal(t, path, path2)
}

// A charset on a binary attachment leaves its bytes alone.
func TestGetAttachmentBinaryCharset(t *testing.T) {
	e := newSyncEnv(t)
	for _, cs := range []string{"binary", "iso-8859-1"} {
		e.put(t, "INBOX", strings.Replace(withAttachments(cs+"@x", "hi", 1), "application/pdf", "application/pdf; charset="+cs, 1))
	}
	e.sync(t)
	for _, r := range e.rows(t, "SELECT id FROM attachments") {
		path, _, err := e.s.GetAttachment(bg, r[0].(int64))
		require.NoError(t, err)
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "%PDF-1.4\n", string(b))
	}
}

func TestGetAttachmentNotOnServerAndEvict(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	p1, _, err := e.s.GetAttachment(bg, ids[0][0].(int64))
	require.NoError(t, err)
	p2, _, err := e.s.GetAttachment(bg, ids[1][0].(int64))
	require.NoError(t, err)
	require.NoError(t, e.s.evict(bg, 9, ids[1][0].(int64))) // each file is 9 bytes; room for one
	_, err = os.Stat(p1)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(p2)
	assert.NoError(t, err)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NULL"))

	_, err = e.s.db.Execute(bg, "UPDATE message_locations SET server_ref = NULL")
	require.NoError(t, err)
	_, _, err = e.s.GetAttachment(bg, ids[0][0].(int64))
	assert.ErrorIs(t, err, ErrNotOnServerYet)
}

func bigAttachment(id string, n int) string {
	enc := base64.StdEncoding.EncodeToString(make([]byte, n))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return "Message-ID: <" + id + ">\r\nFrom: bob@example.com\r\nTo: alice@example.com\r\nSubject: big\r\nDate: Mon, 05 Oct 2026 10:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n--XX\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"big.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b.String() + "--XX--\r\n"
}

func TestGetAttachmentStreamLimit(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", bigAttachment("a1@x", 4<<20))
	e.sync(t)
	aid := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	_, err := e.s.db.Execute(bg, "UPDATE attachments SET size = 1") // understated metadata
	require.NoError(t, err)
	old := attachLimit
	attachLimit = 1000
	t.Cleanup(func() { attachLimit = old })
	before := e.im.BytesSent()
	_, _, err = e.s.GetAttachment(bg, aid)
	assert.ErrorIs(t, err, ErrTooLarge)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
	if runtime.GOOS != "windows" { // Windows loopback buffers absorb the whole literal before the abort lands
		assert.Less(t, e.im.BytesSent()-before, int64(2<<20), "download must stop early, not read the whole 5.6 MB literal")
	}
}

func TestReadSectionNilLiteral(t *testing.T) {
	_, err := readSection(nil, "base64", func() {})
	assert.Error(t, err) // IMAP allows BODY[...] NIL; must not panic
}

func TestEvictReportsRemovalFailure(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	p1, _, err := e.s.GetAttachment(bg, ids[0][0].(int64))
	require.NoError(t, err)
	_, _, err = e.s.GetAttachment(bg, ids[1][0].(int64))
	require.NoError(t, err)
	require.NoError(t, os.Remove(p1))
	require.NoError(t, os.MkdirAll(filepath.Join(p1, "sub"), 0o700)) // cannot be removed as a file
	require.NoError(t, os.WriteFile(filepath.Join(p1, "sub", "x"), []byte("x"), 0o600))
	err = e.s.evict(bg, 0, 0)
	assert.Error(t, err)
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NULL"), "rows are cleared even when a file stays behind")
}

func TestEvictKeepsEveryFileGiven(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	for _, r := range ids {
		_, _, err := e.s.GetAttachment(bg, r[0].(int64))
		require.NoError(t, err)
	}
	require.NoError(t, e.s.evict(bg, 0, ids[0][0].(int64), ids[1][0].(int64)))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
}

func TestGetAttachmentRereadsRowUnderLock(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 1))
	e.sync(t)
	aid := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	unlock, err := e.s.lockAccount(bg, e.acc.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, _, err := e.s.GetAttachment(bg, aid); done <- err }()
	time.Sleep(100 * time.Millisecond) // the call has read its row and waits for the lock
	_, err = e.s.db.Execute(bg, "DELETE FROM messages")
	require.NoError(t, err)
	unlock()
	assert.ErrorContains(t, <-done, "not found")
}

func TestCacheFailuresAreReported(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	p1, _, err := e.s.GetAttachment(bg, ids[0][0].(int64))
	require.NoError(t, err)
	_, _, err = e.s.GetAttachment(bg, ids[1][0].(int64))
	require.NoError(t, err)

	dead, cancel := context.WithCancel(bg)
	cancel()
	assert.Error(t, e.s.touch(dead, ids[0][0].(int64)))
	assert.Error(t, e.s.evict(dead, 0, 0))

	_, err = e.s.db.Execute(bg, `CREATE TRIGGER nope BEFORE UPDATE OF cache_path ON attachments BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	require.NoError(t, err)
	assert.Error(t, e.s.evict(bg, 0, 0))
	_, err = os.Stat(p1)
	assert.NoError(t, err, "file must survive when its row could not be cleared")
}

func TestAuthFailureDuringDownload(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 1))
	e.sync(t)
	aid := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	require.NoError(t, keyring.Set(keyringService, e.s.keyringUser(e.acc.ID), "wrong"))
	ch, unsub := e.s.Subscribe()
	defer unsub()
	_, _, err := e.s.GetAttachment(bg, aid)
	assert.ErrorIs(t, err, backend.ErrAuthFailed)
	acc, _ := e.s.account(bg, e.acc.ID)
	assert.Equal(t, "reauth_required", acc.Status)
	assert.Len(t, ofType(collect(ch), events.SyncFailed), 1)
}

func TestReadSectionTolerance(t *testing.T) {
	want := []byte("hello, tolerant world!!")
	enc := base64.StdEncoding.EncodeToString(want)
	for name, in := range map[string]string{
		"unpadded":   strings.TrimRight(enc, "="),
		"padded":     enc,
		"spaces":     enc[:8] + " \t" + enc[8:16] + "\r\n " + enc[16:],
		"unpadded+w": strings.TrimRight(enc, "=")[:10] + "\t\r\n" + strings.TrimRight(enc, "=")[10:],
	} {
		got, err := readSection(strings.NewReader(in), "base64", func() {})
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
	got, err := readSection(strings.NewReader("YQ"), "base64", func() {})
	require.NoError(t, err)
	assert.Equal(t, "a", string(got))

	got, err = readSection(strings.NewReader("=E9=AA=8C=0A\r\n\x00"), "quoted-printable", func() {})
	require.NoError(t, err)
	assert.Equal(t, "验\n\r\n", string(got))
}

func TestRebuildSearchIndex(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("r@x", "finance@x", "alice@example.com", "通知", "Mon, 05 Oct 2026 09:00:00 +0000", "请查收本月发票和报销单"))
	e.sync(t)
	search := func(q string) int {
		got, err := e.s.Search(bg, e.acc.ID, q, 10)
		require.NoError(t, err)
		return len(got)
	}
	require.Equal(t, 1, search("报销单"))
	// Damage the index: lose the real row, keep a stale one pointing at the message.
	_, err := e.s.db.Execute(bg, "DELETE FROM messages_fts")
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "INSERT INTO messages_fts(rowid, subject, from_addr, body) VALUES (?, 'x', 'x', 'stale words')", e.idOf(t, "r@x"))
	require.NoError(t, err)
	require.Equal(t, 0, search("报销单"))

	require.NoError(t, e.s.RebuildSearchIndex(bg))
	assert.Equal(t, 1, search("报销单"))
	assert.Equal(t, 0, search("stale words"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages_fts"))
}

// A message whose body was cut says so, and its full original can be fetched
// within MaxMessageBytes.
func TestTruncatedMessageAndRawOriginal(t *testing.T) {
	setFor(t, &mailsync.PartFetchBytes, 4096)
	e := newSyncEnv(t)
	raw := eml("big@x", "bob@example.com", "alice@example.com", "big", day, strings.Repeat("all of the words\r\n", 2000))
	e.put(t, "INBOX", raw)
	e.put(t, "INBOX", msg("small@x"))
	e.sync(t)
	big, err := e.s.GetMessage(bg, e.idOf(t, "big@x"), false)
	require.NoError(t, err)
	assert.True(t, big.Truncated)
	small, err := e.s.GetMessage(bg, e.idOf(t, "small@x"), false)
	require.NoError(t, err)
	assert.False(t, small.Truncated)

	got, err := e.s.GetRawMessage(bg, big.ID)
	require.NoError(t, err)
	assert.Equal(t, raw, string(got))

	setFor(t, &attachLimit, int64(len(raw)-1))
	_, err = e.s.GetRawMessage(bg, big.ID)
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestMarkAllAsRead(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", msg("m1@x"))
	e.put(t, "INBOX", msg("m2@x"))
	e.sync(t)
	v := e.inboxView(t)
	n, err := e.s.MarkAllAsRead(bg, v)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	e.sync(t)
	assert.Equal(t, []string{`m1@x \Seen`, `m2@x \Seen`}, e.onServer(t, "INBOX"))
}

func TestThreadReplyTarget(t *testing.T) {
	e := newSyncEnv(t)
	at := func(h int) string { return fmt.Sprintf("Mon, 05 Oct 2026 %02d:00:00 +0000", h) }
	const self = "alice@example.com"
	want := func(mid, why string) {
		t.Helper()
		e.sync(t)
		th, err := e.s.GetThread(bg, e.threadOf(t, "o@x"))
		require.NoError(t, err)
		assert.Equal(t, e.idOf(t, mid), th.ReplyTarget, why)
	}

	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", self, "plan", at(9), "q"))
	want("o@x", "the only mail")

	e.put(t, "Sent", eml("f@x", self, "carol@example.com", "Fwd: plan", at(10), "fwd", "References: <o@x>"))
	want("o@x", "the user's forward is skipped")

	e.put(t, "INBOX", eml("c@x", "carol@example.com", self, "Re: Fwd: plan", at(11), "seen", "In-Reply-To: <f@x>", "References: <o@x> <f@x>"))
	want("c@x", "the recipient's answer is a mail to answer")

	e.put(t, "Sent", eml("r@x", self, "bob@example.com", "Re: plan", at(12), "a", "In-Reply-To: <o@x>", "References: <o@x>"))
	want("r@x", "the user's own reply follows up with its recipients")

	e.put(t, "INBOX", eml("d@x", "dave@example.com", self, "Fwd: plan", at(13), "fyi", "References: <o@x>"))
	want("d@x", "someone else's forward is a mail to answer")

	e.put(t, "Sent", eml("r2@x", self, "bob@example.com", "Re: plan", at(14), "b", "References: <o@x>"))
	want("r2@x", "the user's reply from a client that sends References alone is no forward")

	e.put(t, "Sent", eml("r3@x", self, "bob@example.com", "Re: Fwd: plan", at(15), "c", "References: <o@x>"))
	want("r3@x", "a reply to a forward is no forward")
}

func TestThreadReplyTargetOnlyForwards(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Sent", eml("f@x", "alice@example.com", "carol@example.com", "Fwd: gone", "Mon, 05 Oct 2026 10:00:00 +0000", "fwd", "References: <gone@x>"))
	e.sync(t)
	th, err := e.s.GetThread(bg, e.threadOf(t, "f@x"))
	require.NoError(t, err)
	assert.Zero(t, th.ReplyTarget)
}

package core

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mailsync"
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

	_, err = e.s.db.Execute(bg, "UPDATE messages SET server_folder_id = NULL, server_uid = NULL")
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
	assert.ErrorIs(t, err, conn.ErrAuthFailed)
	acc, _ := e.s.account(bg, e.acc.ID)
	assert.Equal(t, "reauth_required", acc.Status)
	assert.Len(t, ofType(collect(ch), events.SyncFailed), 1)
}

func TestReadSectionBase64Tolerance(t *testing.T) {
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

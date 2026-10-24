package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

var bg = context.Background()

type syncEnv struct {
	s    *Service
	im   *testmail.IMAP
	smtp *testmail.Inbox
	acc  Account
	dir  string
}

func newSyncEnv(t *testing.T, opts ...testmail.Option) *syncEnv {
	t.Helper()
	return newSyncEnvWith(t, Options{}, opts...)
}

// newSyncEnvWith is newSyncEnv with o's options (DataDir and ImageClient are set here).
func newSyncEnvWith(t *testing.T, o Options, opts ...testmail.Option) *syncEnv {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	o.DataDir, o.ImageClient = dir, loopbackImageClient()
	s, err := New(bg, o)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	smtpAddr, in := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t, opts...)
	acc, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "alice@example.com", true))
	require.NoError(t, err)
	return &syncEnv{s: s, im: im, smtp: in, acc: acc, dir: dir}
}

// loopbackImageClient keeps arrival prefetch from contacting real sender sites.
func loopbackImageClient() *http.Client {
	imageClient := imagefetch.NewClient(true)
	imageClient.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("test images: refusing non-loopback address %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return imageClient
}

func (e *syncEnv) put(t *testing.T, folder, raw string) {
	t.Helper()
	_, err := e.im.User.Append(folder, bytes.NewReader([]byte(raw)), &imap.AppendOptions{})
	require.NoError(t, err)
}

func (e *syncEnv) sync(t *testing.T) {
	t.Helper()
	require.NoError(t, e.s.SyncNow(bg, e.acc.ID))
}

func (e *syncEnv) rows(t *testing.T, sql string, args ...any) [][]any {
	t.Helper()
	r, err := e.s.db.Query(bg, sql, args...)
	require.NoError(t, err)
	return r.Values
}

func (e *syncEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	return int(store.Int(e.rows(t, sql, args...)[0][0]))
}

// threadOf returns the thread_id of the message with the given Message-ID.
func (e *syncEnv) threadOf(t *testing.T, mid string) int64 {
	t.Helper()
	r := e.rows(t, "SELECT thread_id FROM messages WHERE message_id = ?", mid)
	require.Len(t, r, 1, mid)
	return store.Int(r[0][0])
}

func (e *syncEnv) idOf(t *testing.T, mid string) int64 {
	t.Helper()
	r := e.rows(t, "SELECT id FROM messages WHERE message_id = ?", mid)
	require.Len(t, r, 1, mid)
	return store.Int(r[0][0])
}

func (e *syncEnv) dial(t *testing.T) *imapclient.Client {
	t.Helper()
	host, port := splitAddr(t, e.im.Addr)
	c, err := conn.DialIMAP(bg, conn.Endpoint{Host: host, Port: port, Security: conn.None, Username: "alice", Password: "secret"})
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func splitAddr(t *testing.T, a string) (string, int) {
	i := strings.LastIndex(a, ":")
	p, err := strconv.Atoi(a[i+1:])
	require.NoError(t, err)
	return a[:i], p
}

// eml builds a message. hdr holds extra header lines ("In-Reply-To: <a@x>").
func eml(id, from, to, subject, date, body string, hdr ...string) string {
	h := fmt.Sprintf("Message-ID: <%s>\r\nFrom: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n", id, from, to, subject, date)
	for _, l := range hdr {
		h += l + "\r\n"
	}
	return h + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n"
}

// withAttachments builds a multipart/mixed message with a text body and n PDFs.
func withAttachments(id, body string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <%s>\r\nFrom: bob@example.com\r\nTo: alice@example.com\r\nSubject: files %s\r\n", id, id)
	b.WriteString("Date: Mon, 05 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n")
	b.WriteString("--XX\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + body + "\r\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"f%d.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n", i)
	}
	b.WriteString("--XX--\r\n")
	return b.String()
}

const htmlOnly = "Message-ID: <h@x>\r\nFrom: Carol <carol@example.com>\r\nTo: alice@example.com\r\nSubject: html\r\n" +
	"Date: Mon, 05 Oct 2026 11:00:00 +0000\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Hello <b>marmalade</b> world</p>\r\n"

func collect(ch <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func ofType(evs []events.Event, typ string) []events.Event {
	var out []events.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestSyncNewMessages(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("p@x", "Bob <bob@example.com>", "alice@example.com", "plain", "Mon, 05 Oct 2026 09:00:00 +0000", "the quarterly numbers are in"))
	e.put(t, "INBOX", withAttachments("a1@x", "see =C3=A9 attached", 1))
	e.put(t, "INBOX", htmlOnly)
	ch, unsub := e.s.Subscribe()
	defer unsub()
	e.sync(t)

	folders, err := e.s.ListFolders(bg, e.acc.ID)
	require.NoError(t, err)
	roles := map[string]string{}
	for _, f := range folders {
		roles[f.Name] = f.Role
	}
	assert.Equal(t, map[string]string{"INBOX": "inbox", "Sent": "sent", "Trash": "trash", "Archive": "archive"}, roles)
	for _, f := range folders {
		if f.Role == "inbox" {
			assert.Equal(t, 3, f.Unread)
		}
	}

	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM messages"))
	r := e.rows(t, "SELECT body_text, snippet, subject, from_addr, has_attachments FROM messages WHERE message_id = 'p@x'")
	assert.Equal(t, "the quarterly numbers are in", strings.TrimSpace(store.Str(r[0][0])))
	assert.Equal(t, "the quarterly numbers are in", store.Str(r[0][1]))
	assert.Equal(t, "plain", store.Str(r[0][2]))
	assert.Equal(t, "Bob <bob@example.com>", store.Str(r[0][3]))
	assert.Zero(t, store.Int(r[0][4]))
	r = e.rows(t, "SELECT body_text, body_html, snippet FROM messages WHERE message_id = 'h@x'")
	assert.Contains(t, store.Str(r[0][1]), "<b>marmalade</b>")
	assert.Equal(t, "Hello marmalade world", store.Str(r[0][2]))
	r = e.rows(t, "SELECT body_text, has_attachments, id FROM messages WHERE message_id = 'a1@x'")
	assert.Equal(t, "see é attached", strings.TrimSpace(store.Str(r[0][0])))
	assert.Equal(t, int64(1), store.Int(r[0][1]))
	att := e.rows(t, "SELECT message_id, filename, part, encoding, cache_path FROM attachments")
	require.Len(t, att, 1)
	assert.Equal(t, store.Int(r[0][2]), store.Int(att[0][0]))
	assert.Equal(t, "f0.pdf", store.Str(att[0][1]))
	assert.Equal(t, "2", store.Str(att[0][2]))
	assert.Equal(t, "base64", store.Str(att[0][3]))
	assert.Nil(t, att[0][4])
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'quarterly'"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'marmalade'"))

	evs := collect(ch)
	assert.Empty(t, ofType(evs, events.MessageReceived), "a first sync is not new mail")
	got := ofType(evs, events.MessagesChanged)
	require.Len(t, got, 1)
	assert.Len(t, got[0].MessageIDs, 3)
	assert.Equal(t, e.acc.ID, got[0].AccountID)

	// Nothing new: UID FETCH last+1:* returns the old max UID, which must be ignored.
	e.sync(t)
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM messages"))
	assert.Empty(t, ofType(collect(ch), events.MessageReceived))

	e.put(t, "Archive", withAttachments("a3@x", "three", 3))
	e.sync(t)
	assert.Len(t, ofType(collect(ch), events.MessageReceived), 1, "an arrival is")
	id := store.Int(e.rows(t, "SELECT id FROM messages WHERE message_id = 'a3@x'")[0][0])
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM attachments WHERE message_id = ?", id))
	assert.Equal(t, 4, e.count(t, "SELECT count(*) FROM attachments"))
	acc, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", acc.Status)
}

func TestSyncFirstSyncHonoursLimit(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET sync_limit = 2 WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	for i := 1; i <= 4; i++ {
		e.put(t, "INBOX", eml(fmt.Sprintf("m%d@x", i), "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "b"))
	}
	e.sync(t)
	assert.Equal(t, [][]any{{"m3@x"}, {"m4@x"}}, e.rows(t, "SELECT message_id FROM messages ORDER BY server_uid"))
	e.put(t, "INBOX", eml("m5@x", "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "b"))
	e.sync(t)
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM messages"))
}

func TestSyncRemoteFlagsAndExpunge(t *testing.T) {
	e := newSyncEnv(t)
	for i := 1; i <= 3; i++ {
		e.put(t, "INBOX", eml(fmt.Sprintf("m%d@x", i), "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", fmt.Sprintf("body%d unique%d", i, i)))
	}
	e.sync(t)
	gone := store.Int(e.rows(t, "SELECT id FROM messages WHERE message_id = 'm2@x'")[0][0])

	c := e.dial(t)
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	require.NoError(t, c.Store(imap.UIDSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close())
	require.NoError(t, c.Store(imap.UIDSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
	require.NoError(t, c.Expunge().Close())

	ch, unsub := e.s.Subscribe()
	defer unsub()
	e.sync(t)
	assert.Equal(t, [][]any{{"m1@x", `\Seen`}, {"m3@x", ""}}, e.rows(t, "SELECT message_id, flags FROM messages ORDER BY server_uid"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'unique2'"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages_fts WHERE rowid = ?", gone))
	changed := ofType(collect(ch), events.MessagesChanged)
	require.Len(t, changed, 1)
	assert.ElementsMatch(t, []int64{gone, store.Int(e.rows(t, "SELECT id FROM messages WHERE message_id = 'm1@x'")[0][0])}, changed[0].MessageIDs)

	folders, err := e.s.ListFolders(bg, e.acc.ID)
	require.NoError(t, err)
	for _, f := range folders {
		if f.Role == "inbox" {
			assert.Equal(t, 1, f.Unread)
		}
	}
}

func TestSyncIncompleteFetchDoesNotDelete(t *testing.T) {
	e := newSyncEnv(t)
	for i := 1; i <= 3; i++ {
		e.put(t, "INBOX", eml(fmt.Sprintf("m%d@x", i), "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "b"))
	}
	e.sync(t)
	e.im.DropNextFetch()
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM messages"))
}

func TestSyncUIDValidityChange(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Archive", withAttachments("old1@x", "oldbody one", 1))
	e.put(t, "Archive", eml("old2@x", "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "oldbody two"))
	e.put(t, "INBOX", eml("keep@x", "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "keep"))
	e.sync(t)

	cache := filepath.Join(e.dir, "cache", strconv.FormatInt(e.acc.ID, 10))
	require.NoError(t, os.MkdirAll(cache, 0o700))
	attID := store.Int(e.rows(t, "SELECT id FROM attachments")[0][0])
	cached := filepath.Join(cache, strconv.FormatInt(attID, 10))
	require.NoError(t, os.WriteFile(cached, []byte("pdf"), 0o600))
	_, err := e.s.db.Execute(bg, "UPDATE attachments SET cache_path = ? WHERE id = ?", cached, attID)
	require.NoError(t, err)
	archive := store.Int(e.rows(t, "SELECT id FROM folders WHERE name = 'Archive'")[0][0])
	oldMsg := store.Int(e.rows(t, "SELECT id FROM messages WHERE message_id = 'old2@x'")[0][0])
	_, err = e.s.db.Execute(bg, "INSERT INTO ops(account_id, message_id, kind, state) VALUES (?, ?, 'flags', 'pending')", e.acc.ID, oldMsg)
	require.NoError(t, err)

	require.NoError(t, e.im.User.Delete("Archive"))
	require.NoError(t, e.im.User.Create("Archive", nil))
	e.put(t, "Archive", eml("new1@x", "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "newbody"))
	e.put(t, "Archive", eml("new2@x", "bob@example.com", "alice@example.com", "s", "Mon, 05 Oct 2026 09:00:00 +0000", "newbody"))
	e.sync(t)

	assert.Equal(t, [][]any{{"new1@x"}, {"new2@x"}}, e.rows(t, "SELECT message_id FROM messages WHERE folder_id = ? ORDER BY server_uid", archive))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'keep@x'"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM attachments"))
	_, err = os.Stat(cached)
	assert.True(t, os.IsNotExist(err))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'oldbody'"))
	assert.Equal(t, "conflict", store.Str(e.rows(t, "SELECT state FROM ops")[0][0]))
}

const day = "Mon, 05 Oct 2026 09:00:00 +0000"

func TestThreading(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("a@t", "Bob <bob@example.com>", "alice@example.com", "同一主题", day, "a"))
	e.put(t, "INBOX", eml("b@t", "Bob <bob@example.com>", "alice@example.com", "Re: 同一主题", day, "b", "In-Reply-To: <a@t>"))
	e.put(t, "Sent", eml("c@t", "alice@example.com", "bob@example.com", "Re: 同一主题", day, "c", "References: <a@t> <b@t>"))
	e.put(t, "INBOX", eml("d@t", "Bob <bob@example.com>", "Alice <alice@example.com>", "Re: 同一主题", "Thu, 08 Oct 2026 09:00:00 +0000", "d"))
	e.put(t, "INBOX", eml("e@t", "carol@example.com", "alice@example.com", "Re: 同一主题", day, "e"))
	e.put(t, "INBOX", eml("f@t", "bob@example.com", "alice@example.com", "同一主题", "Mon, 19 Oct 2026 09:00:00 +0000", "f"))
	e.sync(t)

	ta := e.threadOf(t, "a@t")
	assert.NotZero(t, ta)
	assert.Equal(t, ta, e.threadOf(t, "b@t"))
	assert.Equal(t, ta, e.threadOf(t, "c@t"))
	assert.Equal(t, ta, e.threadOf(t, "d@t"), "subject + shared participant + within 7 days")
	assert.NotEqual(t, ta, e.threadOf(t, "e@t"), "only shares self")
	assert.NotEqual(t, ta, e.threadOf(t, "f@t"), "more than 7 days later")
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM threads"))
	last := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC).Unix()
	assert.Equal(t, last, store.Int(e.rows(t, "SELECT last_date FROM threads WHERE id = ?", ta)[0][0]))
}

func TestThreadingOutOfOrder(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Sent", eml("c@t", "alice@example.com", "bob@example.com", "Re: x", day, "c", "References: <a@t> <b@t>"))
	e.sync(t)
	e.put(t, "INBOX", eml("a@t", "bob@example.com", "alice@example.com", "x", day, "a"))
	e.put(t, "INBOX", eml("b@t", "bob@example.com", "alice@example.com", "Re: x", day, "b", "In-Reply-To: <a@t>"))
	e.sync(t)
	t1 := e.threadOf(t, "c@t")
	assert.Equal(t, t1, e.threadOf(t, "a@t"))
	assert.Equal(t, t1, e.threadOf(t, "b@t"))

	e.put(t, "INBOX", eml("y@t", "dave@example.com", "alice@example.com", "other", day, "y"))
	e.sync(t)
	t2 := e.threadOf(t, "y@t")
	require.NotEqual(t, t1, t2)
	e.put(t, "INBOX", eml("x@t", "dave@example.com", "alice@example.com", "Re: both", day, "x", "References: <a@t> <y@t>"))
	ch, unsub := e.s.Subscribe()
	defer unsub()
	e.sync(t)
	merged, dropped := min(t1, t2), max(t1, t2)
	evs := collect(ch)
	changed := ofType(evs, events.MessagesChanged)
	require.Len(t, changed, 1)
	assert.Equal(t, []int64{merged}, changed[0].ThreadIDs)
	if dropped == t2 {
		assert.Equal(t, []int64{e.idOf(t, "y@t")}, changed[0].MessageIDs)
	} else {
		assert.ElementsMatch(t, []int64{e.idOf(t, "a@t"), e.idOf(t, "b@t"), e.idOf(t, "c@t")}, changed[0].MessageIDs)
	}
	for _, ev := range evs {
		assert.NotContains(t, ev.ThreadIDs, dropped, ev.Type)
	}
	received := ofType(evs, events.MessageReceived)
	require.Len(t, received, 1)
	assert.Equal(t, []int64{merged}, received[0].ThreadIDs)
	for _, id := range []string{"a@t", "b@t", "c@t", "y@t", "x@t"} {
		assert.Equal(t, merged, e.threadOf(t, id), id)
	}
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM threads"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM thread_refs WHERE thread_id != ?", merged))
}

func TestSyncNowSerializesPerAccount(t *testing.T) {
	e := newSyncEnv(t)
	for i := 1; i <= 5; i++ {
		e.put(t, "INBOX", eml(fmt.Sprintf("m%d@x", i), "bob@example.com", "alice@example.com", "s", day, "b"))
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.s.SyncNow(bg, e.acc.ID)
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, 5, e.count(t, "SELECT count(*) FROM messages"))
}

func TestSyncAuthFailureNeedsReauth(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, keyring.Set("godwit", e.s.keyringUser(e.acc.ID), "hunter2-wrong"))
	ch, unsub := e.s.Subscribe()
	defer unsub()
	err := e.s.SyncNow(bg, e.acc.ID)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	acc, aerr := e.s.account(bg, e.acc.ID)
	require.NoError(t, aerr)
	assert.Equal(t, "reauth_required", acc.Status)
	failed := ofType(collect(ch), events.SyncFailed)
	require.Len(t, failed, 1)
	assert.Equal(t, e.acc.ID, failed[0].AccountID)
	assert.NotEmpty(t, failed[0].Error)
	assert.NotContains(t, failed[0].Error, "hunter2-wrong")

	require.NoError(t, keyring.Set("godwit", e.s.keyringUser(e.acc.ID), "secret"))
	e.sync(t)
	acc, _ = e.s.account(bg, e.acc.ID)
	assert.Equal(t, "ok", acc.Status)
}

func TestSyncClaimsAwaitingMove(t *testing.T) {
	e := newSyncEnv(t)
	e.sync(t) // creates the folder rows
	archive := store.Int(e.rows(t, "SELECT id FROM folders WHERE name = 'Archive'")[0][0])
	raw := eml("moved@x", "bob@example.com", "alice@example.com", "moved", day, "moved body")
	// A local message whose server location was cleared by a MOVE without COPYUID.
	res, err := e.s.db.Execute(bg, `INSERT INTO messages(account_id, folder_id, message_id, subject, size, flags)
		VALUES (?, ?, 'moved@x', 'moved', ?, '\Seen')`, e.acc.ID, archive, len(raw))
	require.NoError(t, err)
	local := res.LastInsertID
	_, err = e.s.db.Execute(bg, "INSERT INTO ops(account_id, message_id, kind, dst_folder_id, state) VALUES (?, ?, 'move', ?, 'awaiting_claim')", e.acc.ID, local, archive)
	require.NoError(t, err)

	e.put(t, "Archive", raw)
	e.put(t, "Archive", eml("other@x", "bob@example.com", "alice@example.com", "other", day, "other"))
	e.sync(t)

	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM messages"))
	r := e.rows(t, "SELECT server_folder_id, server_uid, server_uidvalidity FROM messages WHERE id = ?", local)
	assert.Equal(t, archive, store.Int(r[0][0]))
	assert.Equal(t, int64(1), store.Int(r[0][1]))
	assert.NotZero(t, store.Int(r[0][2]))
	assert.Equal(t, "done", store.Str(e.rows(t, "SELECT state FROM ops")[0][0]))
}

func TestSyncInterruptedFlagsFetchChangesNothing(t *testing.T) {
	e := newSyncEnv(t)
	for i := 1; i <= 3; i++ {
		e.put(t, "INBOX", eml(fmt.Sprintf("m%d@x", i), "bob@example.com", "alice@example.com", "s", day, "b"))
	}
	e.sync(t)
	c := e.dial(t)
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	require.NoError(t, c.Store(imap.UIDSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close())
	require.NoError(t, c.Store(imap.UIDSetNum(2, 3), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
	require.NoError(t, c.Expunge().Close())
	e.put(t, "INBOX", eml("m4@x", "bob@example.com", "alice@example.com", "s", day, "b"))

	// INBOX is the first non-empty folder: FETCH new (1), FETCH texts of m4 (2), FETCH FLAGS (3, dropped).
	e.im.DropFetchAfter(2)
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'm4@x'"), "the drop must hit the FLAGS phase")
	assert.Equal(t, [][]any{{"m1@x", ""}, {"m2@x", ""}, {"m3@x", ""}},
		e.rows(t, "SELECT message_id, flags FROM messages WHERE message_id != 'm4@x' ORDER BY server_uid"))

	e.sync(t)
	assert.Equal(t, [][]any{{"m1@x", `\Seen`}, {"m4@x", ""}}, e.rows(t, "SELECT message_id, flags FROM messages ORDER BY server_uid"))
}

func TestSyncFolderDeletedOnServer(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Old", nil))
	e.put(t, "Old", eml("o@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.sync(t)
	gone := e.idOf(t, "o@x")
	tid := e.threadOf(t, "o@x")
	require.NoError(t, e.im.User.Delete("Old"))
	ch, unsub := e.s.Subscribe()
	defer unsub()
	e.sync(t)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM threads"))
	changed := ofType(collect(ch), events.MessagesChanged)
	require.Len(t, changed, 1)
	assert.Equal(t, []int64{gone}, changed[0].MessageIDs)
	assert.Equal(t, []int64{tid}, changed[0].ThreadIDs)
}

func TestSyncExpungeRecomputesLastDate(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("a@t", "bob@example.com", "alice@example.com", "x", day, "a"))
	e.put(t, "INBOX", eml("b@t", "bob@example.com", "alice@example.com", "Re: x", "Wed, 07 Oct 2026 09:00:00 +0000", "b", "In-Reply-To: <a@t>"))
	e.sync(t)
	tid := e.threadOf(t, "a@t")
	require.Equal(t, tid, e.threadOf(t, "b@t"))
	c := e.dial(t)
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	require.NoError(t, c.Store(imap.UIDSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
	require.NoError(t, c.Expunge().Close())
	e.sync(t)
	want := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC).Unix()
	assert.Equal(t, want, store.Int(e.rows(t, "SELECT last_date FROM threads WHERE id = ?", tid)[0][0]))
}

func TestSyncResetSurvivesUndeletableCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory permissions do not stop deletion on Windows")
	}
	e := newSyncEnv(t)
	e.put(t, "Archive", withAttachments("old@x", "old", 1))
	e.sync(t)
	old := e.idOf(t, "old@x")
	cache := filepath.Join(e.dir, "cache", strconv.FormatInt(e.acc.ID, 10))
	require.NoError(t, os.MkdirAll(cache, 0o700))
	cached := filepath.Join(cache, "1")
	require.NoError(t, os.WriteFile(cached, []byte("pdf"), 0o600))
	_, err := e.s.db.Execute(bg, "UPDATE attachments SET cache_path = ?", cached)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(cache, 0o500)) // the file cannot be removed
	t.Cleanup(func() { os.Chmod(cache, 0o700) })

	require.NoError(t, e.im.User.Delete("Archive"))
	require.NoError(t, e.im.User.Create("Archive", nil))
	ch, unsub := e.s.Subscribe()
	defer unsub()
	e.sync(t)
	changed := ofType(collect(ch), events.MessagesChanged)
	require.Len(t, changed, 1)
	assert.Equal(t, []int64{old}, changed[0].MessageIDs)
	_, err = os.Stat(cached)
	require.NoError(t, err, "removal failed as arranged")

	// The startup orphan sweep collects it: nothing references it any more.
	require.NoError(t, os.Chmod(cache, 0o700))
	require.NoError(t, store.CleanOrphans(cache, func(p string) bool {
		return e.count(t, "SELECT count(*) FROM attachments WHERE cache_path = ?", p) > 0
	}))
	_, err = os.Stat(cached)
	assert.True(t, os.IsNotExist(err))
}

// A huge text part is downloaded only up to mailsync.PartFetchBytes, stored as
// truncated, and not downloaded again by later syncs.
func TestSyncHugeBodyIsBounded(t *testing.T) {
	setFor(t, &mailsync.PartFetchBytes, 4096)
	e := newSyncEnv(t)
	text := strings.Repeat("the quick brown fox jumps over the lazy dog 0123456789\r\n", 6000) // ~330 KB
	e.put(t, "INBOX", eml("big@x", "bob@example.com", "alice@example.com", "big", day, text))
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	var wrapped strings.Builder
	for i := 0; i < len(b64); i += 76 {
		wrapped.WriteString(b64[i:min(i+76, len(b64))] + "\r\n")
	}
	e.put(t, "INBOX", "Message-ID: <b64@x>\r\nFrom: bob@example.com\r\nTo: alice@example.com\r\nSubject: b64\r\nDate: "+day+
		"\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"+wrapped.String())
	before := e.im.BytesSent()
	e.sync(t)
	assert.Less(t, e.im.BytesSent()-before, int64(48<<10), "two 4 KB slices, not 750 KB")
	for _, id := range []string{"big@x", "b64@x"} {
		r := e.rows(t, "SELECT body_text, body_truncated FROM messages WHERE message_id = ?", id)
		require.Len(t, r, 1, id)
		body := store.Str(r[0][0])
		assert.Equal(t, int64(1), store.Int(r[0][1]), id)
		assert.NotEmpty(t, body, id)
		assert.LessOrEqual(t, len(body), 4096, id)
		assert.True(t, strings.HasPrefix(text, body), "%s: a clean prefix of the text", id)
	}
	before = e.im.BytesSent()
	e.sync(t)
	assert.Less(t, e.im.BytesSent()-before, int64(8<<10), "nothing is downloaded again")
}

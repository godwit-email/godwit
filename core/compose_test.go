package core

import (
	"bytes"
	"context"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testclock"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

type sendEnv struct {
	*syncEnv
	smtp  *testmail.Inbox
	clock *testclock.Clock // the Services' clock, reopened ones' too
}

// newSendEnv is newSyncEnv with access to the SMTP inbox. With autoSave the SMTP
// server also files every accepted message into the IMAP Sent folder.
func newSendEnv(t *testing.T, autoSave bool) *sendEnv {
	t.Helper()
	keyring.MockInit()
	clock := useTestClock(t)
	dir := t.TempDir()
	s, err := New(bg, Options{DataDir: dir, ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	im := testmail.StartIMAP(t)
	var opts []testmail.SMTPOption
	if autoSave {
		opts = append(opts, testmail.SaveToSent(im))
	}
	smtpAddr, in := testmail.StartSMTP(t, opts...)
	ai := internalInput(t, im.Addr, smtpAddr, "alice@example.com", true)
	ai.Name, ai.Signature = "Alice", "Alice Liddell\nGodwit"
	acc, err := s.AddAccount(bg, ai)
	require.NoError(t, err)
	return &sendEnv{syncEnv: &syncEnv{s: s, im: im, acc: acc, dir: dir}, smtp: in, clock: clock}
}

// useTestClock gives the Services the test creates a fake clock.
func useTestClock(t *testing.T) *testclock.Clock {
	c := testclock.New(time.Now())
	setFor(t, &testClock, Clock(c))
	return c
}

// sendNew sends a fresh draft to bob, lets its undo window pass and returns
// its Message-ID.
func (e *sendEnv) sendNew(t *testing.T) string {
	t.Helper()
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "hello", "<p>hi</p>"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	return store.Str(e.rows(t, "SELECT message_id_hdr FROM outbox ORDER BY id DESC LIMIT 1")[0][0])
}

func (e *sendEnv) state(t *testing.T) string {
	t.Helper()
	return store.Str(e.rows(t, "SELECT state FROM outbox ORDER BY id DESC LIMIT 1")[0][0])
}

func TestReplyAllDraft(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com, Carol <carol@example.com>", "plan",
		day, "the plan", "Cc: dan@example.com"))
	e.sync(t)
	d, err := e.s.NewDraft(bg, e.acc.ID, "reply_all", e.idOf(t, "o@x"))
	require.NoError(t, err)
	assert.NotZero(t, d.ID)
	assert.Equal(t, []string{"Bob <bob@example.com>"}, d.To)
	assert.Equal(t, []string{"Carol <carol@example.com>", "dan@example.com"}, d.Cc)
	assert.Equal(t, "Re: plan", d.Subject)
	assert.Contains(t, d.HTML, "<blockquote>the plan")
	assert.Contains(t, d.HTML, "Godwit")
	assert.Greater(t, strings.Index(d.HTML, "Godwit"), strings.Index(d.HTML, "<blockquote>"), "signature at the end")
}

// Display names with commas, quotes and backslashes survive sync, reply and Send.
func TestReplyToQuotedDisplayNames(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", `"Doe, Jane \"JD\" \\ x" <jane@example.com>`, "alice@example.com", "plan", day, "the plan",
		`Cc: "Smith, Bob" <bob@example.com>`))
	e.sync(t)
	m, err := e.s.GetMessage(bg, e.idOf(t, "q@x"), false)
	require.NoError(t, err)
	from, err := netmail.ParseAddress(m.From)
	require.NoError(t, err, m.From)
	assert.Equal(t, `Doe, Jane "JD" \ x`, from.Name)
	d, err := e.s.NewDraft(bg, e.acc.ID, "reply_all", e.idOf(t, "q@x"))
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	assert.ElementsMatch(t, []string{"jane@example.com", "bob@example.com"}, got[0].To)
	h, err := netmail.ReadMessage(bytes.NewReader(got[0].Data))
	require.NoError(t, err)
	to, err := h.Header.AddressList("To")
	require.NoError(t, err)
	assert.Equal(t, []*netmail.Address{{Name: `Doe, Jane "JD" \ x`, Address: "jane@example.com"}}, to)
	cc, err := h.Header.AddressList("Cc")
	require.NoError(t, err)
	assert.Equal(t, []*netmail.Address{{Name: "Smith, Bob", Address: "bob@example.com"}}, cc)
}

func TestForwardCarriesAttachments(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", withAttachments("f@x", "hi", 1))
	e.sync(t)
	d, err := e.s.NewDraft(bg, e.acc.ID, "forward", e.idOf(t, "f@x"))
	require.NoError(t, err)
	assert.Equal(t, "Fwd: files f@x", d.Subject)
	assert.Empty(t, d.To)
	require.Len(t, d.Attachments, 1)
	assert.Equal(t, "f0.pdf", d.Attachments[0].Filename)
	path := store.Str(e.rows(t, "SELECT path FROM draft_attachments")[0][0])
	assert.Equal(t, filepath.Join(e.dir, "drafts", strconv.FormatInt(d.ID, 10)), filepath.Dir(path))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.4\n", string(b))
}

func TestSendHTMLWithBcc(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", day, "the plan"))
	e.sync(t)
	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "o@x"))
	require.NoError(t, err)
	d.HTML = "<p>Hi <b>Bob</b></p>" + d.HTML
	d.Bcc = []string{"dave@example.com"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "notes.txt")
	require.NoError(t, os.WriteFile(src, []byte("notes"), 0o600))
	_, err = e.s.AddAttachment(bg, d.ID, src)
	require.NoError(t, err)
	draftDir := filepath.Join(e.dir, "drafts", strconv.FormatInt(d.ID, 10))
	require.DirExists(t, draftDir)

	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)

	got := e.smtp.Messages()
	require.Len(t, got, 1)
	assert.ElementsMatch(t, []string{"bob@example.com", "dave@example.com"}, got[0].To)
	assert.Equal(t, "alice@example.com", got[0].From)
	hdr, err := netmail.ReadMessage(bytes.NewReader(got[0].Data))
	require.NoError(t, err)
	assert.NotContains(t, hdr.Header, "Bcc", "no Bcc: header line")
	assert.NotContains(t, string(got[0].Data), "dave@example.com")
	assert.Contains(t, string(got[0].Data), "multipart/alternative")
	p, err := mail.Parse(got[0].Data)
	require.NoError(t, err)
	assert.Equal(t, "o@x", p.InReplyTo)
	assert.Equal(t, []string{"o@x"}, p.References)
	assert.Contains(t, p.HTML, "<b>Bob</b>")
	assert.Contains(t, p.Text, "Hi Bob")
	require.Len(t, p.Attachments, 1)
	assert.Equal(t, "notes", string(p.Attachments[0].Data))

	sent := e.onServer(t, "Sent")
	require.Len(t, sent, 1)
	assert.Equal(t, p.MessageID+` \Seen`, sent[0])
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Equal(t, "done", e.state(t))
	assert.NoDirExists(t, draftDir)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM drafts"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM draft_attachments"))
	entries, _ := os.ReadDir(filepath.Join(e.dir, "outbox"))
	assert.Empty(t, entries, "raw file removed once archived")
}

func TestProviderAutoSavedSentNotDuplicated(t *testing.T) {
	e := newSendEnv(t, true)
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.onServer(t, "Sent"), 1)
	assert.Equal(t, "done", e.state(t))
}

func TestArchiveUncertainNotDuplicated(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	e.im.DropAfterNextAppend()
	_ = e.s.SyncNow(bg, e.acc.ID) // the connection dies mid-sync
	assert.Equal(t, "archive_uncertain", e.state(t))
	e.sync(t)
	assert.Len(t, e.onServer(t, "Sent"), 1)
	assert.Equal(t, "done", e.state(t))
	assert.Len(t, e.smtp.Messages(), 1)
}

func TestResolveOnlyUncertainOrFailed(t *testing.T) {
	e := newSendEnv(t, false)
	for _, st := range []string{"queued", "sending", "sent", "archiving", "done"} {
		r, err := e.s.db.Execute(bg, "INSERT INTO outbox(account_id, state) VALUES (?, ?)", e.acc.ID, st)
		require.NoError(t, err)
		for _, a := range []string{"confirm_sent", "resend", "discard", "edit"} {
			if st == "queued" && a == "resend" {
				continue // retry now
			}
			assert.ErrorIs(t, e.s.ResolveOutbox(bg, r.LastInsertID, a), ErrInvalidState, st+" "+a)
		}
	}
	assert.Error(t, e.s.ResolveOutbox(bg, 999, "discard"))
	r, err := e.s.db.Execute(bg, "INSERT INTO outbox(account_id, state) VALUES (?, 'failed')", e.acc.ID)
	require.NoError(t, err)
	assert.Error(t, e.s.ResolveOutbox(bg, r.LastInsertID, "bogus"))
	require.NoError(t, e.s.ResolveOutbox(bg, r.LastInsertID, "confirm_sent"))
	assert.Equal(t, "sent", e.state(t))
}

func TestUncertainSendIsNotResent(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.FailAfterData(1)
	mid := e.sendNew(t)
	e.sync(t)
	assert.Equal(t, "send_uncertain", e.state(t))
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
	assert.Equal(t, "send_uncertain", e.state(t))

	e.put(t, "Sent", eml("x"+mid, "alice@example.com", "bob@example.com", "hello", day, "near miss"))
	e.sync(t)
	assert.Equal(t, "send_uncertain", e.state(t))

	e.put(t, "Sent", eml(mid, "alice@example.com", "bob@example.com", "hello", day, "the one"))
	e.sync(t)
	assert.Equal(t, "done", e.state(t))
	assert.Len(t, e.onServer(t, "Sent"), 2, "found, so not appended")
	assert.Len(t, e.smtp.Messages(), 1)
}

func TestCrashDuringSendingBecomesUncertain(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	e.sendNew(t)
	_, err := e.s.db.Execute(bg, "UPDATE outbox SET state = CASE WHEN id = 1 THEN 'sending' ELSE 'archiving' END")
	require.NoError(t, err)
	e.reopen(t)
	var states []string
	for _, r := range e.rows(t, "SELECT state FROM outbox ORDER BY id") {
		states = append(states, store.Str(r[0]))
	}
	assert.Equal(t, []string{"send_uncertain", "archive_uncertain"}, states)
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "hello", out[0].Subject)
}

func TestResolveOutboxResend(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.FailAfterData(1)
	e.sendNew(t)
	e.sync(t)
	require.Equal(t, "send_uncertain", e.state(t))
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	require.NoError(t, e.s.ResolveOutbox(bg, out[0].ID, "resend"))
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 2)
	assert.Equal(t, "done", e.state(t))
	assert.Len(t, e.onServer(t, "Sent"), 1)
}

func TestSendRetriesThenFails(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET smtp_port = 1")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		e.sync(t)
	}
	assert.Equal(t, "failed", e.state(t))
	assert.Equal(t, 5, e.count(t, "SELECT attempts FROM outbox"))
	out, _ := e.s.ListOutbox(bg, e.acc.ID)
	require.Len(t, out, 1)
	assert.NotEmpty(t, out[0].LastError)
}

func TestSendValidates(t *testing.T) {
	e := newSendEnv(t, false)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	check := func() {
		t.Helper()
		assert.Error(t, e.s.Send(bg, d.ID))
		assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM drafts"))
		assert.Zero(t, e.count(t, "SELECT count(*) FROM outbox"))
	}
	check() // no recipients
	d.Cc = []string{"not an address"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	check()

	d.Cc = []string{"bob@example.com"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	old := attachLimit
	attachLimit = 150
	t.Cleanup(func() { attachLimit = old })
	src := filepath.Join(t.TempDir(), "a.bin")
	require.NoError(t, os.WriteFile(src, make([]byte, 200), 0o600))
	_, err = e.s.AddAttachment(bg, d.ID, src)
	assert.ErrorIs(t, err, ErrTooLarge)
	require.NoError(t, os.WriteFile(src, make([]byte, 100), 0o600))
	a1, err := e.s.AddAttachment(bg, d.ID, src)
	require.NoError(t, err)
	_, err = e.s.AddAttachment(bg, d.ID, src)
	require.NoError(t, err)
	assert.ErrorIs(t, e.s.Send(bg, d.ID), ErrTooLarge)
	check()

	require.NoError(t, e.s.RemoveAttachment(bg, d.ID, a1.ID))
	_, err = e.s.SaveDraft(bg, d)
	require.ErrorIs(t, err, ErrStaleDraft, "an attachment change is a new revision")
	d, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	assert.Len(t, d.Attachments, 1)
	attachLimit = old // the encoded message is bigger than 150 bytes; TestSendRejectsOversizeEncodedMessage covers that
	require.NoError(t, e.s.Send(bg, d.ID))
	assert.Error(t, e.s.Send(bg, d.ID), "a draft is sent once")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM outbox"))
}

func TestSendRejectsOversizeEncodedMessage(t *testing.T) {
	e := newSendEnv(t, false)
	old := attachLimit
	attachLimit = 1000
	t.Cleanup(func() { attachLimit = old })
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To = []string{"bob@example.com"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "a.bin")
	require.NoError(t, os.WriteFile(src, make([]byte, 900), 0o600)) // under the limit; base64 and headers push it over
	_, err = e.s.AddAttachment(bg, d.ID, src)
	require.NoError(t, err)
	assert.ErrorIs(t, e.s.Send(bg, d.ID), ErrTooLarge)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM drafts"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM draft_attachments"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM outbox"))
	entries, _ := os.ReadDir(filepath.Join(e.dir, "outbox"))
	assert.Empty(t, entries)
}

// The user's decision taken while a sync is under way is applied after it, not lost.
func TestResolveWaitsForSync(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.FailAfterData(1)
	e.sendNew(t)
	e.sync(t)
	require.Equal(t, "send_uncertain", e.state(t))
	id := store.Int(e.rows(t, "SELECT id FROM outbox")[0][0])
	done := make(chan error, 1)
	beforeOutboxItem = func(int64) {
		beforeOutboxItem = nil
		go func() { done <- e.s.ResolveOutbox(bg, id, "confirm_sent") }()
		select {
		case err := <-done:
			t.Errorf("ResolveOutbox ran during the sync: %v", err)
			done <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Cleanup(func() { beforeOutboxItem = nil })
	e.sync(t)
	require.NoError(t, <-done)
	assert.Equal(t, "sent", e.state(t))
}

// A row changed after processOutbox read it is left alone.
func TestOutboxSkipsChangedItem(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	e.im.DropAfterNextAppend()
	_ = e.s.SyncNow(bg, e.acc.ID)
	require.Equal(t, "archive_uncertain", e.state(t))
	require.NoError(t, e.s.ResolveOutbox(bg, 1, "resend")) // queued again
	_, err := e.s.db.Execute(bg, "UPDATE outbox SET state = 'archive_uncertain'")
	require.NoError(t, err)
	beforeOutboxItem = func(id int64) {
		beforeOutboxItem = nil
		_, err := e.s.db.Execute(bg, "UPDATE outbox SET state = 'queued' WHERE id = ?", id) // as if resolved meanwhile
		require.NoError(t, err)
	}
	t.Cleanup(func() { beforeOutboxItem = nil })
	e.sync(t)
	assert.Equal(t, "queued", e.state(t), "the stale archive_uncertain snapshot must not overwrite it")
}

// SaveDraft overlapping Send either lands before Send reads the draft or fails;
// it never succeeds only to have the older content queued.
func TestSaveDraftDuringSend(t *testing.T) {
	e := newSendEnv(t, false)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject = []string{"bob@example.com"}, "old"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	saved := make(chan error, 1)
	afterDraftLoad = func() {
		afterDraftLoad = nil
		nd := d
		nd.Subject = "new"
		go func() { _, err := e.s.SaveDraft(bg, nd); saved <- err }()
		select {
		case err := <-saved:
			t.Error("SaveDraft ran while Send was building the message")
			saved <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Cleanup(func() { afterDraftLoad = nil })
	require.NoError(t, e.s.Send(bg, d.ID))
	assert.ErrorIs(t, <-saved, ErrStaleDraft, "the draft is read-only once sent")
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "old", out[0].Subject)
}

func TestResolveDiscard(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.FailAfterData(1)
	e.sendNew(t)
	e.sync(t)
	raw := store.Str(e.rows(t, "SELECT raw_path FROM outbox")[0][0])
	require.FileExists(t, raw)
	require.NoError(t, e.s.ResolveOutbox(bg, 1, "discard"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM outbox"))
	assert.NoFileExists(t, raw)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
	assert.Empty(t, e.onServer(t, "Sent"))
}

func TestResolveConfirmSent(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.FailAfterData(1)
	mid := e.sendNew(t)
	e.sync(t)
	raw := store.Str(e.rows(t, "SELECT raw_path FROM outbox")[0][0])
	require.NoError(t, e.s.ResolveOutbox(bg, 1, "confirm_sent"))
	e.sync(t)
	assert.Equal(t, "done", e.state(t))
	assert.Equal(t, []string{mid + ` \Seen`}, e.onServer(t, "Sent"))
	assert.NoFileExists(t, raw)
	assert.Len(t, e.smtp.Messages(), 1, "confirm_sent does not resend")
}

// sendBlocked queues a message and starts an explicit SyncNow that is held by
// the SMTP server after DATA. It returns the SyncNow result channel.
func (e *sendEnv) sendBlocked(t *testing.T) <-chan error {
	t.Helper()
	entered, release := e.smtp.BlockData()
	t.Cleanup(release)
	e.sendNew(t)
	done := make(chan error, 1)
	go func() { done <- e.s.SyncNow(bg, e.acc.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SyncNow did not reach SMTP DATA")
	}
	return done
}

// returned waits briefly for SyncNow's result. The call's work ended before the
// shutdown returned (the registry waits for it); only handing the result back to
// this goroutine may still be pending. A SyncNow still blocked in SMTP never
// returns here, because the server is held until the test ends.
func returned(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("the explicit SyncNow was not stopped")
		return nil
	}
}

func TestRemoveAccountWaitsForExplicitSync(t *testing.T) {
	e := newSendEnv(t, false)
	done := e.sendBlocked(t)
	require.NoError(t, e.s.RemoveAccount(bg, e.acc.ID))
	assert.ErrorIs(t, returned(t, done), context.Canceled)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM outbox"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM accounts"))
}

func TestCloseWaitsForExplicitSync(t *testing.T) {
	e := newSendEnv(t, false)
	done := e.sendBlocked(t)
	require.NoError(t, e.s.Close())
	assert.ErrorIs(t, returned(t, done), context.Canceled)
	assert.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), ErrClosed)
	e.reopen(t)
	r := e.rows(t, "SELECT state, last_error FROM outbox")
	assert.Equal(t, "send_uncertain", store.Str(r[0][0]))
	assert.NotEmpty(t, store.Str(r[0][1]), "the sync recorded its result before the database closed")
}

// Every explicit call refuses to run after Close.
func TestExplicitCallsAfterClose(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", withAttachments("c@x", "hi", 1))
	e.sync(t)
	mid := e.idOf(t, "c@x")
	att := store.Int(e.rows(t, "SELECT id FROM attachments")[0][0])
	in := internalInput(t, e.im.Addr, "127.0.0.1:1", "x@example.com", false)
	require.NoError(t, e.s.Close())
	_, _, err := e.s.GetAttachment(bg, att)
	assert.ErrorIs(t, err, ErrClosed, "GetAttachment")
	_, err = e.s.GetRawMessage(bg, mid)
	assert.ErrorIs(t, err, ErrClosed, "GetRawMessage")
	assert.ErrorIs(t, e.s.ResolveOutbox(bg, 1, "discard"), ErrClosed, "ResolveOutbox")
	assert.ErrorIs(t, e.s.TestAccount(bg, in), ErrClosed, "TestAccount")
	assert.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), ErrClosed, "SyncNow")
}

// Drafts are sanitized on read too (rows written before sanitizing on save),
// and saving what was read changes nothing.
func TestDraftReadSanitizedAndStable(t *testing.T) {
	e := newSendEnv(t, false)
	_, err := e.s.db.Execute(bg, `INSERT INTO drafts(account_id, mode, to_addrs, cc_addrs, bcc_addrs, subject, body_html, updated_at)
		VALUES (?, 'new', '[]', '[]', '[]', 'old', ?, 0)`, e.acc.ID, `<p onclick="x()">hi</p><script>alert(1)</script><img src="https://t.example/p.png">`)
	require.NoError(t, err)
	list, err := e.s.ListDrafts(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	got, err := e.s.GetDraft(bg, list[0].ID)
	require.NoError(t, err)
	for _, h := range []string{list[0].HTML, got.HTML} {
		for _, bad := range []string{"<script", "onclick", "t.example"} {
			assert.NotContains(t, h, bad)
		}
		assert.Contains(t, h, "hi")
	}

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.HTML = `<div style="color: red; font-size: 14px"><b>bold</b> <a href="https://example.com/a?b=1&amp;c=2">link</a> &lt;tag&gt; 中文</div>` +
		`<table><tr><td colspan="2">cell</td></tr></table><blockquote><p>quoted</p></blockquote>`
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	first := d.HTML
	for range 3 {
		d, err = e.s.GetDraft(bg, d.ID)
		require.NoError(t, err)
		assert.Equal(t, first, d.HTML, "load is stable")
		d, err = e.s.SaveDraft(bg, d)
		require.NoError(t, err)
		assert.Equal(t, first, d.HTML, "save of a loaded draft is stable")
	}
}

func TestSignatureHTMLPlainEscapes(t *testing.T) {
	assert.Equal(t, `<div data-godwit="signature"><p>Bob &lt;bob@x.com&gt;<br>line2</p></div>`,
		signatureHTML("Bob <bob@x.com>\nline2", nil, false), "no delimiter by default")
	assert.Equal(t, `<div data-godwit="signature"><p>-- <br>Bob &lt;bob@x.com&gt;<br>line2</p></div>`,
		signatureHTML("Bob <bob@x.com>\nline2", nil, true))
	assert.Equal(t, "", signatureHTML("", nil, true))
}

func TestSignatureHTMLMarked(t *testing.T) {
	sig := SignatureHTMLPrefix + `<p><b>Bob</b> <a href="https://x.com">site</a><script>alert(1)</script>` +
		`<img src="https://x.com/t.png" onerror="alert(2)"></p><div data-godwit="quote">q</div>`
	got := signatureHTML(sig, nil, true)
	assert.True(t, strings.HasPrefix(got, `<div data-godwit="signature"><p>-- </p><p><b>Bob</b>`), got)
	assert.True(t, strings.HasPrefix(signatureHTML(sig, nil, false), `<div data-godwit="signature"><p><b>Bob</b>`), "no delimiter by default")
	assert.Equal(t, 1, strings.Count(got, "data-godwit"), got)
	assert.Contains(t, got, "<b>Bob</b>")
	assert.Contains(t, got, `href="https://x.com"`)
	for _, bad := range []string{"<script", "alert", "onerror", "t.png"} {
		assert.NotContains(t, got, bad)
	}
	assert.Equal(t, "", signatureHTML(SignatureHTMLPrefix, nil, true))
}

// Settings.SignatureDelimiter: off (the default) a new draft's signature has
// no "-- " line in the HTML or the text part; on, both have it.
func TestSignatureDelimiterSetting(t *testing.T) {
	e := newSendEnv(t, false)
	sig := SignatureHTMLPrefix + "<p>Alice</p>"
	_, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Signature: &sig})
	require.NoError(t, err)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	assert.False(t, st.SignatureDelimiter, "off by default")

	send := func(delim bool) string {
		t.Helper()
		st.SignatureDelimiter = delim
		_, err := e.s.SetSettings(bg, st)
		require.NoError(t, err)
		d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
		require.NoError(t, err)
		_, _, block := mail.SplitDraft(d.HTML)
		assert.Equal(t, delim, strings.Contains(block, "-- "), "the HTML, delim %v", delim)
		d.To, d.HTML = []string{"bob@example.com"}, "<p>hi</p>"+d.HTML
		d, err = e.s.SaveDraft(bg, d)
		require.NoError(t, err)
		require.NoError(t, e.s.Send(bg, d.ID))
		e.clock.Advance(UndoSendWindow)
		e.sync(t)
		got := e.smtp.Messages()
		p, err := mail.Parse(got[len(got)-1].Data)
		require.NoError(t, err)
		return p.Text
	}
	assert.NotContains(t, send(false), "-- ", "the text part follows the HTML")
	assert.Regexp(t, `(?m)^-- \r?$`, send(true), "a line of its own")
}

func TestSignatureEditorHTMLLegacy(t *testing.T) {
	assert.Equal(t, "<p>a &amp; b<br>c</p>", SignatureEditorHTML("a & b\nc", nil))
	assert.Equal(t, "", SignatureEditorHTML("", nil))
}

func TestSentArchivedToOverriddenFolder(t *testing.T) {
	e := newSendEnv(t, false)
	require.NoError(t, e.im.User.Create("Sent Messages", nil))
	e.sync(t)
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "sent", e.folderID(t, "Sent Messages")))
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.onServer(t, "Sent Messages"), 1)
	assert.Empty(t, e.onServer(t, "Sent"))
	assert.Equal(t, "done", e.state(t))
}

func TestSendRefusesLinkReference(t *testing.T) {
	e := newSendEnv(t, false)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.HTML = []string{"bob@example.com"}, "<p>Join: "+linkRefPrefix+"zoom.us/j?m=999&h=0000000000000000</p>"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	require.ErrorIs(t, e.s.Send(bg, d.ID), ErrLinkRef)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM outbox"))
}

package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// draftToSend is a reply to a synced message with every field set: Cc, Bcc,
// an attachment, the quote and the signature.
func (e *sendEnv) draftToSend(t *testing.T) Draft {
	t.Helper()
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", day, "the plan"))
	e.sync(t)
	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "o@x"))
	require.NoError(t, err)
	d.HTML = "<p>Hi Bob</p>" + d.HTML
	d.Cc, d.Bcc = []string{"carol@example.com"}, []string{"dave@example.com"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "notes.txt")
	require.NoError(t, os.WriteFile(src, []byte("notes"), 0o600))
	_, err = e.s.AddAttachment(bg, d.ID, src)
	require.NoError(t, err)
	d, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	return d
}

// onlyItem is the account's one outbox item.
func (e *sendEnv) onlyItem(t *testing.T) OutboxItem {
	t.Helper()
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	return out[0]
}

func (e *sendEnv) listed(t *testing.T, draftID int64) bool {
	t.Helper()
	ds, err := e.s.ListDrafts(bg, e.acc.ID)
	require.NoError(t, err)
	for _, d := range ds {
		if d.ID == draftID {
			return true
		}
	}
	return false
}

func TestCancelWithinUndoWindow(t *testing.T) {
	e := newSendEnv(t, false)
	d := e.draftToSend(t)
	require.NoError(t, e.s.Send(bg, d.ID))
	it := e.onlyItem(t)
	assert.Equal(t, "queued", it.State)
	assert.Equal(t, d.ID, it.DraftID)
	assert.Equal(t, e.clock.Now().Add(UndoSendWindow).UnixMilli(), it.SendAfter.UnixMilli())
	assert.False(t, e.listed(t, d.ID), "a sent draft is not listed")
	got, err := e.s.GetDraft(bg, d.ID)
	require.NoError(t, err, "it can still be read")
	assert.Equal(t, d, got)

	e.clock.Advance(UndoSendWindow / 2)
	e.sync(t)
	assert.Empty(t, e.smtp.Messages(), "not sent within the window")
	require.NoError(t, e.s.CancelOutbox(bg, it.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	assert.Empty(t, e.smtp.Messages())
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, out)
	got, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, got, "the draft comes back as it was sent")
	assert.True(t, e.listed(t, d.ID))
	got.Subject = "again"
	_, err = e.s.SaveDraft(bg, got)
	require.NoError(t, err, "writable again")
	assert.Error(t, e.s.CancelOutbox(bg, it.ID), "cancelled already")

	require.NoError(t, e.s.Send(bg, d.ID), "sent again")
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	require.Len(t, e.smtp.Messages(), 1)
	_, err = e.s.GetDraft(bg, d.ID)
	assert.Error(t, err, "the draft goes once the message is sent")
	entries, _ := os.ReadDir(filepath.Join(e.dir, "drafts"))
	assert.Empty(t, entries)
}

// While the item holds the draft, every write of it is refused and nothing is
// queued twice.
func TestQueuedDraftIsReadOnly(t *testing.T) {
	e := newSendEnv(t, false)
	d := e.draftToSend(t)
	require.NoError(t, e.s.Send(bg, d.ID))
	src := filepath.Join(t.TempDir(), "more.txt")
	require.NoError(t, os.WriteFile(src, []byte("more"), 0o600))
	smtpAddr, _ := testmail.StartSMTP(t)
	other, err := e.s.AddAccount(bg, internalInput(t, e.im.Addr, smtpAddr, "x@example.com", false))
	require.NoError(t, err)

	edited := d
	edited.Subject = "changed"
	_, err = e.s.SaveDraft(bg, edited)
	assert.ErrorIs(t, err, ErrStaleDraft, "SaveDraft")
	edited.Revision = 0
	_, err = e.s.SaveDraft(bg, edited)
	assert.ErrorIs(t, err, ErrStaleDraft, "SaveDraft without a revision")
	_, err = e.s.AddAttachment(bg, d.ID, src)
	assert.ErrorIs(t, err, ErrStaleDraft, "AddAttachment")
	_, _, err = e.s.AddAttachmentAt(bg, d.ID, d.Revision, src)
	assert.ErrorIs(t, err, ErrStaleDraft, "AddAttachmentAt")
	assert.ErrorIs(t, e.s.RemoveAttachment(bg, d.ID, d.Attachments[0].ID), ErrStaleDraft, "RemoveAttachment")
	_, err = e.s.SetDraftAccount(bg, d.ID, other.ID, d.Revision)
	assert.ErrorIs(t, err, ErrStaleDraft, "SetDraftAccount")
	assert.ErrorIs(t, e.s.DeleteDraft(bg, d.ID), ErrStaleDraft, "DeleteDraft")
	_, err = e.s.DeleteDraftIfEmpty(bg, d.ID, d.Revision)
	assert.ErrorIs(t, err, ErrStaleDraft, "DeleteDraftIfEmpty")
	assert.ErrorIs(t, e.s.Send(bg, d.ID), ErrStaleDraft, "Send")
	assert.ErrorIs(t, e.s.SendRevision(bg, d.ID, d.Revision), ErrStaleDraft, "SendRevision")
	got, err := e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, got)
	e.onlyItem(t)
}

// With AutoSync on, the worker sends the message once the window has passed,
// with no SyncNow.
func TestWorkerSendsAfterUndoWindow(t *testing.T) {
	e, ch := newWorkerEnv(t)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject = []string{"bob@example.com"}, "hello"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	waitEvent(t, ch, events.SyncFinished, 5*time.Second) // the round Send asked for
	assert.Empty(t, e.smtp.Messages())
	it := e.onlyItem(t)
	e.clock.Advance(UndoSendWindow)
	require.Eventually(t, func() bool { return len(e.smtp.Messages()) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.ErrorIs(t, e.s.CancelOutbox(bg, it.ID), ErrInvalidState, "sent already")
}

func TestCancelOnceSendingStarted(t *testing.T) {
	e := newSendEnv(t, false)
	entered, release := e.smtp.BlockData()
	t.Cleanup(release)
	e.sendNew(t)
	done := make(chan error, 1)
	go func() { done <- e.s.SyncNow(bg, e.acc.ID) }()
	<-entered
	it := e.onlyItem(t)
	require.Equal(t, "sending", it.State)
	assert.ErrorIs(t, e.s.CancelOutbox(bg, it.ID), ErrInvalidState)
	_, err := e.s.GetDraft(bg, it.DraftID)
	require.NoError(t, err, "kept until it may have gone out")
	release()
	require.NoError(t, <-done)
	require.Len(t, e.smtp.Messages(), 1)
	assert.ErrorIs(t, e.s.CancelOutbox(bg, it.ID), ErrInvalidState)
	_, err = e.s.GetDraft(bg, it.DraftID)
	assert.Error(t, err, "gone once sent")
}

// A crash while sending leaves the message uncertain: its draft goes, and it
// cannot be taken back.
func TestCrashWhileSendingDropsDraft(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	it := e.onlyItem(t)
	_, err := e.s.db.Execute(bg, "UPDATE outbox SET state = 'sending'") // as a sync left it
	require.NoError(t, err)
	e.reopen(t)
	it2 := e.onlyItem(t)
	assert.Equal(t, "send_uncertain", it2.State)
	assert.Zero(t, it2.DraftID)
	_, err = e.s.GetDraft(bg, it.DraftID)
	assert.Error(t, err)
	assert.NoDirExists(t, e.s.draftDir(it.DraftID))
	assert.ErrorIs(t, e.s.CancelOutbox(bg, it.ID), ErrInvalidState)
}

// An item queued before undo send has no draft: neither cancel nor edit takes
// it back, and the other actions work as before.
func TestItemWithoutDraft(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	_, err := e.s.db.Execute(bg, "UPDATE outbox SET draft_id = NULL, send_after = NULL") // as the migration leaves it
	require.NoError(t, err)
	it := e.onlyItem(t)
	assert.Zero(t, it.DraftID)
	assert.True(t, it.SendAfter.IsZero())
	assert.ErrorIs(t, e.s.CancelOutbox(bg, it.ID), ErrNoOutboxDraft)
	e.rejectBob()
	e.sync(t)
	require.Equal(t, "failed", e.state(t))
	assert.ErrorIs(t, e.s.ResolveOutbox(bg, it.ID, "edit"), ErrNoOutboxDraft)
	require.NoError(t, e.s.ResolveOutbox(bg, it.ID, "discard"))
}

// rejectBob makes the server refuse bob@example.com for good, until cleared
// with Reply.
func (e *sendEnv) rejectBob() {
	e.smtp.Reply("bob@example.com", testmail.AtRcpt, &smtp.SMTPError{Code: 550, Message: "No such user"}, false)
}

// failNew sends a fresh draft to bob that the server refuses, and returns the
// failed item.
func (e *sendEnv) failNew(t *testing.T) OutboxItem {
	t.Helper()
	e.rejectBob()
	e.sendNew(t)
	e.sync(t)
	it := e.onlyItem(t)
	require.Equal(t, "failed", it.State)
	return it
}

func TestEditFailedItem(t *testing.T) {
	e := newSendEnv(t, false)
	it := e.failNew(t)
	require.NotZero(t, it.DraftID)
	assert.False(t, e.listed(t, it.DraftID))
	assert.ErrorIs(t, e.s.CancelOutbox(bg, it.ID), ErrInvalidState, "failed is not queued")
	require.NoError(t, e.s.ResolveOutbox(bg, it.ID, "edit"))
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.True(t, e.listed(t, it.DraftID))
	d, err := e.s.GetDraft(bg, it.DraftID)
	require.NoError(t, err)
	d.To = []string{"carol@example.com"}
	_, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)

	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	assert.Equal(t, []string{"carol@example.com"}, got[0].To)
}

func TestDiscardAndConfirmSentDropDraft(t *testing.T) {
	for _, action := range []string{"discard", "confirm_sent"} {
		t.Run(action, func(t *testing.T) {
			e := newSendEnv(t, false)
			it := e.failNew(t)
			require.NoError(t, e.s.ResolveOutbox(bg, it.ID, action))
			_, err := e.s.GetDraft(bg, it.DraftID)
			assert.Error(t, err)
			assert.NoDirExists(t, e.s.draftDir(it.DraftID))
		})
	}
}

// resend sends a failed item at once; on a queued one it keeps the window.
func TestResendKeepsOrSkipsWindow(t *testing.T) {
	e := newSendEnv(t, false)
	it := e.failNew(t)
	e.smtp.Reply("bob@example.com", testmail.AtRcpt, nil, false)
	require.NoError(t, e.s.ResolveOutbox(bg, it.ID, "resend"))
	assert.Equal(t, e.clock.Now().UnixMilli(), e.onlyItem(t).SendAfter.UnixMilli())
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To = []string{"bob@example.com"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	queued := out[len(out)-1]
	require.NoError(t, e.s.ResolveOutbox(bg, queued.ID, "resend"))
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1, "still within its window")
}

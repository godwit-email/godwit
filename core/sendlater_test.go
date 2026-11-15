package core

import (
	"bytes"
	"net/mail"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendLater schedules a fresh draft to bob for at and returns it.
func (e *sendEnv) sendLater(t *testing.T, at time.Time) Draft {
	t.Helper()
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "tomorrow", "<p>hi</p>"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	require.NoError(t, e.s.SendLater(bg, d.ID, d.Revision, at, ""))
	return d
}

// A scheduled message waits in the outbox until its time, then goes as any
// other, dated then; the audit row says it was scheduled, and for when.
func TestSendLaterWaitsForItsTime(t *testing.T) {
	e := newSendEnv(t, false)
	at := e.clock.Now().Add(time.Hour).Truncate(time.Second)
	d := e.sendLater(t, at)
	it := e.onlyItem(t)
	assert.Equal(t, "queued", it.State)
	assert.Equal(t, d.ID, it.DraftID)
	assert.True(t, it.SendAt.Equal(at), "SendAt %v", it.SendAt)
	assert.True(t, it.SendAfter.Equal(at))
	assert.False(t, e.listed(t, d.ID), "a scheduled draft is not listed")
	rows := e.rows(t, "SELECT source, tool, target_id, reason FROM audit_log WHERE tool = 'SendLater'")
	require.Len(t, rows, 1)
	assert.Equal(t, []any{AuditUI, "SendLater", d.ID, "scheduled for " + at.UTC().Format(time.RFC3339)},
		[]any{store.Str(rows[0][0]), store.Str(rows[0][1]), store.Int(rows[0][2]), store.Str(rows[0][3])})

	e.sync(t)
	e.clock.Advance(time.Hour - time.Second)
	e.sync(t)
	assert.Empty(t, e.smtp.Messages(), "not before its time")
	e.clock.Advance(time.Second)
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	m, err := mail.ReadMessage(bytes.NewReader(got[0].Data))
	require.NoError(t, err)
	date, err := m.Header.Date()
	require.NoError(t, err)
	assert.True(t, date.Equal(at), "dated when due: %v", date)
	assert.Equal(t, "done", e.state(t))
}

// A time already past sends once the undo window has passed.
func TestSendLaterInThePast(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendLater(t, e.clock.Now().Add(-time.Hour))
	e.sync(t)
	assert.Empty(t, e.smtp.Messages(), "the undo window still holds")
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
}

// Cancelling gives the draft back, writable, and nothing goes.
func TestCancelScheduled(t *testing.T) {
	e := newSendEnv(t, false)
	d := e.sendLater(t, e.clock.Now().Add(time.Hour))
	require.NoError(t, e.s.CancelOutbox(bg, e.onlyItem(t).ID))
	e.clock.Advance(2 * time.Hour)
	e.sync(t)
	assert.Empty(t, e.smtp.Messages())
	assert.True(t, e.listed(t, d.ID))
	got, err := e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	got.Subject = "edited"
	_, err = e.s.SaveDraft(bg, got)
	require.NoError(t, err, "writable again")
}

// Due while Godwit was closed, it goes on the next start; due more than
// OutboxExpiry before, it fails instead, for the user to decide.
func TestSendLaterAcrossRestart(t *testing.T) {
	e := newSendEnv(t, false)
	closedFor := func(d time.Duration) {
		require.NoError(t, e.s.Close())
		e.clock.Advance(d)
		s, err := New(bg, Options{DataDir: e.dir, ImageClient: loopbackImageClient()})
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		e.s = s
		e.sync(t)
	}
	e.sendLater(t, e.clock.Now().Add(time.Hour))
	closedFor(2 * time.Hour)
	require.Len(t, e.smtp.Messages(), 1)
	assert.Equal(t, "done", e.state(t))

	e.sendLater(t, e.clock.Now().Add(time.Hour))
	closedFor(time.Hour + OutboxExpiry + time.Second)
	assert.Len(t, e.smtp.Messages(), 1, "not sent days late")
	it := e.onlyItem(t)
	assert.Equal(t, "failed", it.State)
	assert.Equal(t, "expired", it.Failure.Reason)
}

// A refusal for now keeps it queued, still scheduled and cancellable, and
// retries it as any other send.
func TestSendLaterRetries(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.Reply("bob@example.com", testmail.AtData,
		&smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Try again later"}, true)
	at := e.clock.Now().Add(time.Hour)
	e.sendLater(t, at)
	e.clock.Advance(time.Hour)
	e.sync(t)
	it := e.onlyItem(t)
	require.Equal(t, "queued", it.State)
	assert.Equal(t, 1, it.Attempts)
	assert.True(t, it.SendAt.Equal(at.Truncate(time.Millisecond)))
	assert.NotZero(t, it.DraftID, "still cancellable")
	e.clock.Advance(time.Minute)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
	assert.Equal(t, "done", e.state(t))
}

// message.beforeSend checks a scheduled send when it is scheduled, as any
// send: a warning needs the user's go-ahead, which keeps the time.
func TestSendLaterRunsSendChecks(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "check " + d.subject })`))
	d := sendDraft(t, s, "report")
	at := s.clk().Now().Add(time.Hour)
	var we *SendWarningsError
	require.ErrorAs(t, s.SendLater(t.Context(), d.ID, d.Revision, at, ""), &we)
	assert.Zero(t, outboxCount(t, s))
	require.NoError(t, s.SendLater(t.Context(), d.ID, d.Revision, at, we.Check.Digest))
	assert.Equal(t, 1, calls(t, s, "warner"))
	out, err := s.ListOutbox(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, at.UnixMilli(), out[0].SendAt.UnixMilli())
}

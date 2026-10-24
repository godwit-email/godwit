package core

import (
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A recipient the server refuses for good fails the item at once, with the
// server's reply; edit brings the draft back to fix the address.
func TestPermanentRejectionFailsAtOnce(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.Reply("bob@example.com", testmail.AtRcpt,
		&smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such user"}, false)
	e.sendNew(t)
	e.sync(t)
	it := e.onlyItem(t)
	require.Equal(t, "failed", it.State)
	assert.Equal(t, &OutboxFailure{Reason: "rejected", Stage: "RCPT", Recipient: "bob@example.com",
		Code: 550, Enhanced: "5.1.1", Message: "No such user"}, it.Failure)
	assert.NotEmpty(t, it.LastError)
	assert.Empty(t, e.smtp.Messages())

	require.NoError(t, e.s.ResolveOutbox(bg, it.ID, "edit"))
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

// A refusal for now queues the item again for a minute later; then it goes,
// once.
func TestTemporaryRejectionRetriesLater(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.Reply("bob@example.com", testmail.AtData,
		&smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Try again later"}, true)
	e.sendNew(t)
	e.sync(t)
	it := e.onlyItem(t)
	require.Equal(t, "queued", it.State)
	assert.Equal(t, e.clock.Now().Add(time.Minute).UnixMilli(), it.NextAttempt.UnixMilli())
	assert.Equal(t, 1, it.Attempts)
	assert.Contains(t, it.LastError, "Try again later")
	assert.Nil(t, it.Failure)

	e.clock.Advance(time.Minute - time.Second)
	e.sync(t)
	assert.Equal(t, 1, e.onlyItem(t).Attempts, "not retried before its time")
	e.clock.Advance(time.Second)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
	assert.Equal(t, "done", e.state(t))
}

// Each refusal in a row doubles the wait, up to an hour; nothing gives up.
func TestRetryBackoffDoublesToAnHour(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.Reply("bob@example.com", testmail.AtRcpt, &smtp.SMTPError{Code: 421, Message: "Busy"}, false)
	e.sendNew(t)
	var waits []time.Duration
	for range 9 {
		e.sync(t)
		it := e.onlyItem(t)
		require.Equal(t, "queued", it.State)
		wait := it.NextAttempt.Sub(e.clock.Now())
		waits = append(waits, wait.Round(time.Second))
		e.clock.Advance(wait)
	}
	m := time.Minute
	assert.Equal(t, []time.Duration{m, 2 * m, 4 * m, 8 * m, 16 * m, 32 * m, 60 * m, 60 * m, 60 * m}, waits)
	assert.Empty(t, e.smtp.Messages())
}

// A message that waited in the queue over three days is not sent but fails,
// for the user to decide; resend restarts the three days.
func TestQueuedTooLongExpires(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	e.clock.Advance(OutboxExpiry + time.Second) // offline, say
	e.sync(t)
	it := e.onlyItem(t)
	require.Equal(t, "failed", it.State)
	assert.Equal(t, &OutboxFailure{Reason: "expired"}, it.Failure)
	assert.Empty(t, e.smtp.Messages())

	require.NoError(t, e.s.ResolveOutbox(bg, it.ID, "resend"))
	it = e.onlyItem(t)
	assert.Nil(t, it.Failure, "resend clears it")
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
}

// An item queued before undo send counts the three days from when it was
// queued.
func TestQueuedTooLongWithoutSendAfter(t *testing.T) {
	e := newSendEnv(t, false)
	e.smtp.Reply("bob@example.com", testmail.AtRcpt, &smtp.SMTPError{Code: 421, Message: "Busy"}, false)
	e.sendNew(t)
	_, err := e.s.db.Execute(bg, "UPDATE outbox SET draft_id = NULL, send_after = NULL") // as the migration leaves it
	require.NoError(t, err)
	e.sync(t)
	e.clock.Advance(OutboxExpiry - 2*time.Hour)
	e.sync(t)
	require.Equal(t, "queued", e.state(t))
	e.clock.Advance(2 * time.Hour)
	e.sync(t)
	it := e.onlyItem(t)
	require.Equal(t, "failed", it.State)
	assert.Equal(t, "expired", it.Failure.Reason)
}

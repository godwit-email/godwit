package mailsync_test

// The op queue on its own interface: what core queues, what the push takes
// and when, and which state changes it refuses.

import (
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exec runs stmts in one transaction and returns the last one's result.
func (e *env) exec(stmts ...sqlite.Statement) (changes, id int64) {
	e.t.Helper()
	res, err := e.db.Transaction(e.ctx, stmts)
	require.NoError(e.t, err)
	return res[len(res)-1].Changes, res[len(res)-1].LastInsertID
}

// applied records every write-back the push asks of the fake, then applies it.
func (e *env) applied() *[]backend.Op {
	var sent []backend.Op
	e.fake.OnApply = func(o backend.Op, apply func() (backend.Result, error)) (backend.Result, error) {
		sent = append(sent, o)
		return apply()
	}
	return &sent
}

// The state machine has no edge back from an ended op, nor into a state from
// one it is not entered from; an unknown state is no state.
func TestOpStatesMoveOnlyAlongTheirEdges(t *testing.T) {
	for _, edge := range [][2]string{
		{"done", "pending"}, {"conflict", "pending"}, {"failed", "pending"}, {"cancelled", "pending"},
		{"awaiting_claim", "failed"}, {"awaiting_claim", "cancelled"}, {"done", "failed"},
		{"pending", "sent"},
	} {
		assert.Panics(t, func() { mailsync.OpsTo(edge[1], []string{edge[0]}, "", "id = ?", 1) }, "%s → %s", edge[0], edge[1])
	}
	assert.NotPanics(t, func() { mailsync.OpsTo("done", []string{"cancelled"}, "", "id = ?", 1) }, "a rescued purge already under way")
	assert.NotPanics(t, func() { mailsync.OpsTo("conflict", []string{"pending", "awaiting_claim"}, "", "id = ?", 1) })
}

// Every transition checks the state it leaves: an op that ended meanwhile is
// left as it is.
func TestEndedOpsStayEnded(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "INBOX", raw("b@x"))
	e.sync()
	done := e.queueFlags("a@x", `\Seen`, `\Seen`)
	require.NoError(t, e.push())
	_, purge := e.exec(mailsync.QueuePurgeNow(acct, e.id("b@x"), e.folder("INBOX"), "trash", time.Now().Add(time.Hour)))

	n, _ := e.exec(mailsync.CancelPurges(e.id("b@x")))
	assert.EqualValues(t, 1, n)
	n, _ = e.exec(mailsync.CancelPurges(e.id("b@x")))
	assert.EqualValues(t, 0, n, "already cancelled")
	n, _ = e.exec(mailsync.ConflictAll(acct, "the server is gone"))
	assert.EqualValues(t, 0, n, "done and cancelled ops are no conflicts")

	state, _, _ := e.op(done)
	assert.Equal(t, "done", state)
	state, _, _ = e.op(purge)
	assert.Equal(t, "cancelled", state)
	ops, err := mailsync.ListOps(e.ctx, e.db, acct)
	require.NoError(t, err)
	assert.Empty(t, ops)
}

// What a change queues is what the push sends: the flags to add and remove,
// and the destination's baseline an attempt cut off after sending recorded.
func TestQueuedChangesReachTheBackend(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	sent := e.applied()

	e.exec(mailsync.QueueFlags(acct, e.id("a@x"), []string{`\Flagged`, "$Work"}, []string{`\Seen`}))
	require.NoError(t, e.push())
	require.Len(t, *sent, 1)
	assert.Equal(t, backend.SetFlags{At: backend.Location{Folder: "INBOX", Ref: "1/1"},
		Add: backend.Flags{Flagged: true, Keywords: []string{"$Work"}}, Remove: backend.Flags{Seen: true}}, (*sent)[0])

	op := e.queueMove("a@x", "INBOX", "Archive")
	before := backend.MoveBaseline{Cursor: "1/1", Refs: []backend.MessageRef{"1/1"}}
	require.NoError(t, mailsync.MarkSent(e.ctx, e.db, op, &before))
	_, _, attempts := e.op(op)
	assert.EqualValues(t, 1, attempts)
	require.NoError(t, e.push())
	require.Len(t, *sent, 2)
	move, ok := (*sent)[1].(backend.Move)
	require.True(t, ok)
	assert.Equal(t, &before, move.Before)
	state, _, attempts := e.op(op)
	assert.Equal(t, "done", state)
	assert.EqualValues(t, 2, attempts)
}

// A move queued with an undo window waits for its end: NextOpDue names it, a
// push before it sends nothing, one at it moves the message.
func TestQueuedMoveWaitsForItsDeadline(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	sent := e.applied()
	now := time.Now()
	due := now.Add(10 * time.Second)
	msg, inbox, archive := e.id("a@x"), e.folder("INBOX"), e.folder("Archive")
	_, op := e.exec(append(mailsync.ShowIn(msg, archive, []int64{inbox}), mailsync.QueueMove(acct, msg, inbox, archive, due))...)

	next, err := mailsync.NextOpDue(e.ctx, e.db, acct, now)
	require.NoError(t, err)
	assert.Equal(t, due.UnixMilli(), next.UnixMilli())
	require.NoError(t, mailsync.PushOps(e.ctx, e.db, e.recv, acct, due.Add(-time.Millisecond)))
	assert.Empty(t, *sent)
	state, _, attempts := e.op(op)
	assert.Equal(t, "pending", state)
	assert.EqualValues(t, 0, attempts)

	require.NoError(t, mailsync.PushOps(e.ctx, e.db, e.recv, acct, due))
	assert.Len(t, *sent, 1)
	state, _, _ = e.op(op)
	assert.Equal(t, "done", state)
	next, err = mailsync.NextOpDue(e.ctx, e.db, acct, now)
	require.NoError(t, err)
	assert.True(t, next.IsZero())
}

// Within its undo window a permanent delete is taken back: the message
// counts as deleted until CancelPurges, and the push sends nothing for it.
func TestPurgeTakenBackInItsWindow(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	sent := e.applied()
	now := time.Now()
	_, op := e.exec(mailsync.QueuePurgeNow(acct, e.id("a@x"), e.folder("INBOX"), "trash", now.Add(10*time.Second)))
	waits := func() int { return len(e.rows("SELECT 1 FROM messages m WHERE " + mailsync.PurgeWaits)) }
	assert.Equal(t, 1, waits())

	n, _ := e.exec(mailsync.CancelPurges(e.id("a@x")))
	assert.EqualValues(t, 1, n)
	assert.Equal(t, 0, waits())
	require.NoError(t, mailsync.PushOps(e.ctx, e.db, e.recv, acct, now.Add(time.Minute)))
	assert.Empty(t, *sent)
	state, _, _ := e.op(op)
	assert.Equal(t, "cancelled", state)
	assert.Empty(t, e.rows("SELECT 1 FROM messages m WHERE "+mailsync.Purged))
}

// Undoing an archive nothing has sent yet cancels it; once it may have
// reached the server, a move back is queued after it instead.
func TestRestoreTakesBackAnUnsentArchive(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	msg, inbox := e.id("a@x"), e.folder("INBOX")
	due := time.Now().Add(10 * time.Second)

	_, archived := e.exec(mailsync.QueueMove(acct, msg, inbox, 0, due))
	restore, err := mailsync.QueueRestore(e.ctx, e.db, acct, msg, inbox, true)
	require.NoError(t, err)
	e.exec(restore...)
	state, _, _ := e.op(archived)
	assert.Equal(t, "cancelled", state)
	ops, err := mailsync.ListOps(e.ctx, e.db, acct)
	require.NoError(t, err)
	assert.Empty(t, ops, "nothing to push")

	_, archived = e.exec(mailsync.QueueMove(acct, msg, inbox, 0, due))
	require.NoError(t, mailsync.MarkSent(e.ctx, e.db, archived, nil))
	restore, err = mailsync.QueueRestore(e.ctx, e.db, acct, msg, inbox, true)
	require.NoError(t, err)
	_, back := e.exec(restore...)
	ops, err = mailsync.ListOps(e.ctx, e.db, acct)
	require.NoError(t, err)
	assert.Equal(t, []mailsync.Op{
		{ID: archived, Kind: "move", State: "pending", Attempts: 1},
		{ID: back, Kind: "move", State: "pending"},
	}, ops)
	assert.Equal(t, [][]any{{archived, inbox}}, e.rows("SELECT depends_on, dst_folder_id FROM ops WHERE id = ?", back), "after the archive, into the folder")
}

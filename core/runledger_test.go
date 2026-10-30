package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// ledgerHost is the Service as the ledger's host, recording resumes instead
// of driving them and closing no history.
type ledgerHost struct {
	*Service
	resumed []int64
}

func (h *ledgerHost) resume(runID int64) error { h.resumed = append(h.resumed, runID); return nil }

func (h *ledgerHost) closedHistory(context.Context, int64, int64, snapshot, []ai.ToolResult) ([]sqlite.Statement, func(), error) {
	return nil, nil, nil
}

type ledgerEnv struct {
	s    *Service
	l    *runLedger
	host *ledgerHost
	conv Conversation
}

func newLedgerEnv(t *testing.T) *ledgerEnv {
	t.Helper()
	keyring.MockInit()
	s, err := New(t.Context(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	conv, err := s.NewConversation(t.Context(), "ask", 0)
	require.NoError(t, err)
	host := &ledgerHost{Service: s}
	return &ledgerEnv{s: s, l: &runLedger{db: s.db, host: host}, host: host, conv: conv}
}

func ok() error { return nil }

// run claims a run of the conversation, its drive left to the test.
func (e *ledgerEnv) run(t *testing.T) *runJob {
	t.Helper()
	var job *runJob
	require.NoError(t, e.l.claim(t.Context(), e.conv, "", nil, ok, func(j *runJob) error { job = j; return nil }))
	return job
}

// interrupted is a run stopped at n pending approvals.
func (e *ledgerEnv) interrupted(t *testing.T, n int) (*runJob, []approvalRow) {
	t.Helper()
	job := e.run(t)
	calls, ps := make([]ai.ToolCall, n), make([]prepared, n)
	for i := range calls {
		calls[i], ps[i] = ai.ToolCall{ID: string(rune('a' + i)), Name: "fake_draft"}, prepared{Args: json.RawMessage(`{}`)}
	}
	require.NoError(t, e.l.interrupt(t.Context(), job.id, 0, calls, ps))
	e.l.release(job)
	as, err := e.s.approvalRows(t.Context(), "a.run_id = ?", job.id)
	require.NoError(t, err)
	return job, as
}

func (e *ledgerEnv) state(t *testing.T, runID int64) string {
	t.Helper()
	st, err := e.l.state(t.Context(), runID)
	require.NoError(t, err)
	return st
}

func (e *ledgerEnv) approvalState(t *testing.T, id int64) string {
	t.Helper()
	a, err := e.s.approvalRow(t.Context(), id)
	require.NoError(t, err)
	return a.State
}

func TestLedgerClaim(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()

	refused := errors.New("refused")
	assert.ErrorIs(t, e.l.claim(ctx, e.conv, "", nil, func() error { return refused }, nil), refused)
	assert.Equal(t, "0", queryStr(t, e.s, "SELECT COUNT(*) FROM runs"), "a refused claim records nothing")

	job := e.run(t)
	assert.Equal(t, "running", e.state(t, job.id))
	assert.ErrorIs(t, e.l.claim(ctx, e.conv, "", nil, ok, nil), ErrRunActive, "one run not over per conversation")

	require.NoError(t, e.l.settle(ctx, job.id, "done"))
	e.l.release(job)
	err := e.l.claim(ctx, e.conv, "", nil, ok, func(*runJob) error { return refused })
	assert.ErrorIs(t, err, refused)
	assert.Equal(t, "failed", queryStr(t, e.s, "SELECT state FROM runs ORDER BY id DESC LIMIT 1"), "a start that fails fails the run")
	assert.Empty(t, e.l.active, "and stops tracking it")
}

func TestLedgerSettleOnlyInFlight(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()
	job := e.run(t)
	title := sqlite.Statement{SQL: "UPDATE conversations SET title = 'saved' WHERE id = ?", Args: []any{e.conv.ID}}
	require.NoError(t, e.l.settle(ctx, job.id, "done", title))
	assert.Equal(t, "done", e.state(t, job.id))

	_, err := e.s.db.Execute(ctx, "UPDATE conversations SET title = 'kept' WHERE id = ?", e.conv.ID)
	require.NoError(t, err)
	assert.Error(t, e.l.settle(ctx, job.id, "failed", title), "a run over is not settled again")
	assert.Equal(t, "done", e.state(t, job.id))
	assert.Equal(t, "kept", queryStr(t, e.s, "SELECT title FROM conversations WHERE id = ?", e.conv.ID), "nor is what it would have saved")
	assert.Error(t, e.l.interrupt(ctx, job.id, 0, []ai.ToolCall{{ID: "x", Name: "fake_draft"}}, []prepared{{Args: json.RawMessage(`{}`)}}))
	assert.Equal(t, "0", queryStr(t, e.s, "SELECT COUNT(*) FROM approvals"), "nor interrupted")
}

func TestLedgerExecuteAtMostOnce(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()
	job, as := e.interrupted(t, 1)
	a := as[0]
	runs := 0
	apply := func(ctx context.Context) error {
		runs++
		assert.Equal(t, "executing", e.approvalState(t, a.ID))
		return e.l.finish(ctx, a, "done", `"ok"`, nil, nil, "executed")
	}
	require.NoError(t, e.l.execute(ctx, a.ID, job.id, apply, true))
	assert.Equal(t, "done", e.approvalState(t, a.ID))
	assert.Equal(t, "resuming", e.state(t, job.id))
	assert.Equal(t, []int64{job.id}, e.host.resumed)

	assert.ErrorIs(t, e.l.execute(ctx, a.ID, job.id, apply, true), ErrInvalidState)
	assert.Equal(t, 1, runs, "a decided approval never runs again")
	assert.Error(t, e.l.finish(ctx, a, "done", `"twice"`, nil, nil, "executed"), "nor ends again")
	assert.Equal(t, `"ok"`, queryStr(t, e.s, "SELECT result FROM approvals WHERE id = ?", a.ID))
}

func TestLedgerResumeWaitsForEveryApproval(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()
	job, as := e.interrupted(t, 2)
	reject := func(a approvalRow) func(context.Context) error {
		return func(ctx context.Context) error { return e.l.finish(ctx, a, "rejected", msgRejected, nil, nil, "rejected") }
	}
	require.NoError(t, e.l.execute(ctx, as[0].ID, job.id, reject(as[0]), true))
	assert.Equal(t, "interrupted", e.state(t, job.id), "one still pending")
	assert.Empty(t, e.host.resumed)

	ended, err := e.l.claimExternal(ctx, as[1])
	require.NoError(t, err)
	require.NoError(t, e.l.resume(ctx, job.id))
	assert.Equal(t, "interrupted", e.state(t, job.id), "one still executing")
	require.NoError(t, e.l.finish(ctx, as[1], "done", "{}", nil, nil, "executed"))
	require.NoError(t, ended(ctx, true))
	require.NoError(t, e.l.resume(ctx, job.id))
	assert.Equal(t, []int64{job.id}, e.host.resumed, "resumed once")

	require.NoError(t, e.l.unclaim(ctx, job.id))
	assert.Equal(t, "interrupted", e.state(t, job.id), "a resume that never started is put back")
}

func TestLedgerReopenAndStale(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()
	job, as := e.interrupted(t, 2)
	_, err := e.l.claimExternal(ctx, as[0])
	require.NoError(t, err)
	require.NoError(t, e.l.reopen(ctx, as[0], `{"round":1}`))
	assert.Equal(t, "pending", e.approvalState(t, as[0].ID), "asked again")
	assert.Error(t, e.l.reopen(ctx, as[1], "{}"), "only an executing approval is asked again")

	require.NoError(t, e.l.stale(ctx, as))
	assert.Equal(t, "stale", e.approvalState(t, as[0].ID))
	assert.Equal(t, "stale", e.approvalState(t, as[1].ID))
	assert.Equal(t, "resuming", e.state(t, job.id), "nothing left open")
}

func TestLedgerCloseOnlyHeld(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()
	job, as := e.interrupted(t, 1)
	_, err := e.l.close(ctx, job.id, "cancelled", msgCancelled)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", e.state(t, job.id))
	assert.Equal(t, "rejected", e.approvalState(t, as[0].ID))

	running := e.run(t)
	_, err = e.l.close(ctx, running.id, "cancelled", msgCancelled)
	assert.Error(t, err, "a run in flight is cancelled, not closed")
	assert.Equal(t, "running", e.state(t, running.id))
	_, err = e.l.fail(ctx, running.id, errors.New("cause"))
	require.NoError(t, err)
	assert.Equal(t, "failed", e.state(t, running.id), "a failed run never blocks the conversation")
}

func TestLedgerHold(t *testing.T) {
	e := newLedgerEnv(t)
	job := e.run(t)
	go func() {
		<-job.ctx.Done() // cancelled by hold
		e.l.release(job)
	}()
	release, err := e.l.hold(t.Context(), func() ([]int64, error) { return []int64{job.id}, nil })
	require.NoError(t, err)
	assert.False(t, e.l.mu.TryLock(), "held")
	assert.NotContains(t, e.l.active, job.id, "the run it stopped is over")
	release()
	assert.True(t, e.l.mu.TryLock())
	e.l.mu.Unlock()
}

func TestLedgerRecover(t *testing.T) {
	e := newLedgerEnv(t)
	ctx := t.Context()
	job, as := e.interrupted(t, 1)
	_, err := e.s.db.Execute(ctx, "UPDATE approvals SET state = 'executing' WHERE id = ?", as[0].ID) // cut off mid-execution
	require.NoError(t, err)
	other, err := e.s.NewConversation(ctx, "ask", 0)
	require.NoError(t, err)
	var running *runJob
	require.NoError(t, e.l.claim(ctx, other, "", nil, ok, func(j *runJob) error { running = j; return nil }))

	require.NoError(t, e.l.recover(ctx))
	assert.Equal(t, "failed", e.approvalState(t, as[0].ID), "never run again")
	assert.Equal(t, msgQuit, queryStr(t, e.s, "SELECT result FROM approvals WHERE id = ?", as[0].ID))
	assert.Equal(t, "resuming", e.state(t, job.id), "nothing left open: it resumes")
	assert.Equal(t, []int64{job.id}, e.host.resumed)
	assert.Equal(t, "failed", e.state(t, running.id), "cut off while running")
}

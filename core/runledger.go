package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/store"
)

// runLedger is the only writer of runs.state and approvals.state. Every
// change checks the state it leaves, so an approval runs at most once (ADR
// 0004, 0006) and its effects commit with its end (ADR 0021):
//
//	runs:      → running; running | resuming → done | failed | cancelled | interrupted;
//	           interrupted → resuming (nothing left open) → interrupted (closing first);
//	           interrupted | resuming → done | failed | cancelled (closed without the model)
//	approvals: → pending → executing → done | failed | stale | rejected | pending (asked again);
//	           pending → rejected | stale
//
// Its lock orders run starts, approval claims and execution, resume claims
// and cancels; it is taken before editMu. Callbacks it is given (claim's and
// execute's run, the host's resume) run holding it.
type runLedger struct {
	db   *store.DB
	host runHost

	// ponytail: one lock for every run's approvals, starts and cancels; per run if it ever contends
	mu     sync.Mutex
	active map[int64]*runJob // runs in flight
	ext    map[int64]extExec // approvals executing outside mu (external mode) by ID

	// Test seams, nil in production: beforeClaim runs before a run is
	// claimed, with the call's context; beforeUntrack when a finished run is
	// about to stop being tracked; afterApprovalClaim and beforeResumeClaim
	// returning true simulate a crash there.
	beforeClaim                           func(ctx context.Context)
	beforeUntrack                         func(runID int64)
	afterApprovalClaim, beforeResumeClaim func() bool
}

// runHost is what the ledger needs of the conversations (the Service).
type runHost interface {
	approvalRows(ctx context.Context, where string, args ...any) ([]approvalRow, error)
	auditRow(a approvalRow, decision string) sqlite.Statement
	// closedHistory is what closes runID's calls in conversation convID with
	// results; after, if set, runs once that committed.
	closedHistory(ctx context.Context, convID, runID int64, snap snapshot, results []ai.ToolResult) (stmts []sqlite.Statement, after func(), err error)
	// resume continues runID, claimed (resuming), in the background.
	resume(runID int64) error
}

// The runs.state sets a transition leaves.
const (
	inFlight = "'running', 'resuming'"                // a drive of it is under way
	held     = "'interrupted', 'resuming'"            // stopped at its approvals, or claimed to go on
	notOver  = "'running', 'interrupted', 'resuming'" // it blocks its conversation
)

// moveRun moves runID to state to from one of from; from any other state it
// fails its transaction (runs.state is NOT NULL).
func moveRun(runID int64, to, from string) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE runs SET state = CASE WHEN state IN (" + from + ") THEN ? END, updated_at = ? WHERE id = ?",
		Args: []any{to, time.Now().Unix(), runID}}
}

func (l *runLedger) exec(ctx context.Context, stmts ...sqlite.Statement) error {
	_, err := l.db.Transaction(ctx, stmts)
	return err
}

// state is runID's state ("" for no such run).
func (l *runLedger) state(ctx context.Context, runID int64) (string, error) {
	rows, err := l.db.Query(ctx, "SELECT state FROM runs WHERE id = ?", runID)
	if err != nil || len(rows.Values) == 0 {
		return "", err
	}
	return store.Str(rows.Values[0][0]), nil
}

// claim records a running run of conv unless the conversation has one not
// over (ErrRunActive), once check passes, and tracks it; start, holding the
// lock, sets it off. A start that fails fails the run.
func (l *runLedger) claim(ctx context.Context, conv Conversation, ref string, tools []string, check func() error, start func(job *runJob) error) error {
	if l.beforeClaim != nil {
		l.beforeClaim(ctx)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := check(); err != nil {
		return err
	}
	toolsJSON, _ := json.Marshal(tools)
	snap, _ := json.Marshal(snapshot{MailIDs: true})
	now := time.Now().Unix()
	r, err := l.db.Execute(ctx, `INSERT INTO runs(conversation_id, state, model, tools, snapshot, created_at, updated_at)
		SELECT ?, 'running', ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id = ?
		AND state IN (`+notOver+`))`, conv.ID, ref, string(toolsJSON), snap, now, now, conv.ID)
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return ErrRunActive
	}
	job := l.track(ctx, r.LastInsertID, conv, 0)
	if err := start(job); err != nil {
		l.untrack(job)
		return errors.Join(err, l.exec(context.WithoutCancel(ctx), moveRun(job.id, "failed", inFlight)))
	}
	return nil
}

// track registers a run in flight; the lock is held (claim's start, or the
// host's resume).
func (l *runLedger) track(ctx context.Context, id int64, conv Conversation, round int) *runJob {
	ctx, cancel := context.WithCancel(ctx)
	job := &runJob{id: id, conv: conv, round: round, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if l.active == nil {
		l.active = map[int64]*runJob{}
	}
	l.active[id] = job
	return job
}

// untrack ends tracking job; the lock is held.
func (l *runLedger) untrack(job *runJob) {
	if l.active[job.id] == job { // a resume may already track this run ID
		delete(l.active, job.id)
	}
	job.cancel()
	close(job.done)
}

// release ends tracking job once its drive is over.
func (l *runLedger) release(job *runJob) {
	if l.beforeUntrack != nil {
		l.beforeUntrack(job.id)
	}
	l.mu.Lock()
	l.untrack(job)
	l.mu.Unlock()
}

// settle ends a drive of runID as state (done, failed or cancelled) with
// stmts, in one transaction.
func (l *runLedger) settle(ctx context.Context, runID int64, state string, stmts ...sqlite.Statement) error {
	return l.exec(ctx, append(stmts, moveRun(runID, state, inFlight))...)
}

// abort fails runID if a drive of it is still under way: its outcome could
// not be saved.
func (l *runLedger) abort(ctx context.Context, runID int64) error {
	_, err := l.db.Execute(ctx, "UPDATE runs SET state = 'failed', updated_at = ? WHERE id = ? AND state IN ("+inFlight+")", time.Now().Unix(), runID)
	return err
}

// interrupt ends a drive of runID at calls, each recorded as a pending
// approval with its prepared ps[i] from seq round on, with stmts, in one
// transaction.
func (l *runLedger) interrupt(ctx context.Context, runID int64, round int, calls []ai.ToolCall, ps []prepared, stmts ...sqlite.Statement) error {
	now := time.Now().Unix()
	stmts = append(stmts, moveRun(runID, "interrupted", inFlight))
	for i, c := range calls {
		p := ps[i]
		dests := []byte("[]")
		if len(p.Destinations) > 0 {
			dests, _ = json.Marshal(p.Destinations)
		}
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO approvals(run_id, seq, call_id, tool, args, summary, target_kind,
			target_id, target_version, state, destinations, binding, source, extension, input, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{runID, round + i, c.ID, c.Name, string(p.Args), p.Summary, p.TargetKind, nullID(p.TargetID), p.TargetVersion,
				string(dests), p.Binding, cmp.Or(p.source, gateway.SourceWit), p.extension, nullText(p.Input), now, now}})
	}
	return l.exec(ctx, stmts...)
}

// execute claims pending approval id of runID (→ executing) and ends it with
// run, holding the lock throughout; with resume, the run then resumes once
// nothing of it is left open.
func (l *runLedger) execute(ctx context.Context, id, runID int64, run func(ctx context.Context) error, resume bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, err := l.db.Execute(ctx, "UPDATE approvals SET state = 'executing', updated_at = ? WHERE id = ? AND state = 'pending'", time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return ErrInvalidState
	}
	if l.afterApprovalClaim != nil && l.afterApprovalClaim() {
		return errors.New("simulated crash")
	}
	ctx = context.WithoutCancel(ctx) // claimed: always reach an end state
	err = run(ctx)
	if resume {
		err = errors.Join(err, l.resumeLocked(ctx, runID))
	}
	return err
}

// claimExternal claims pending approval a to execute outside the lock
// (→ executing, exec_mode external). Once its outcome is recorded (finish),
// end stops waiting on it and, with resume, resumes the run once nothing of
// it is left open.
func (l *runLedger) claimExternal(ctx context.Context, a approvalRow) (end func(ctx context.Context, resume bool) error, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, err := l.db.Execute(ctx, "UPDATE approvals SET state = 'executing', exec_mode = 'external', updated_at = ? WHERE id = ? AND state = 'pending'",
		time.Now().Unix(), a.ID)
	switch {
	case err != nil:
		return nil, err
	case r.Changes == 0:
		return nil, ErrInvalidState
	case l.afterApprovalClaim != nil && l.afterApprovalClaim():
		return nil, errors.New("simulated crash")
	}
	x := extExec{runID: a.RunID, done: make(chan struct{})}
	if l.ext == nil {
		l.ext = map[int64]extExec{}
	}
	l.ext[a.ID] = x
	return func(ctx context.Context, resume bool) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.ext, a.ID)
		close(x.done)
		if resume {
			return l.resumeLocked(ctx, a.RunID)
		}
		return nil
	}, nil
}

// finish ends executing approval a as state with result, audited as
// decision, with stmts, in one transaction, which fails unless a is
// executing (approvals.state is NOT NULL). edited, unless nil, replaces its
// recorded arguments; undo is its undo (nil for none).
func (l *runLedger) finish(ctx context.Context, a approvalRow, state, result string, edited, undo any, decision string, stmts ...sqlite.Statement) error {
	return l.exec(ctx, append(stmts, sqlite.Statement{SQL: "UPDATE approvals SET state = CASE state WHEN 'executing' THEN ? END, args = COALESCE(?, args), result = ?, undo = ?, updated_at = ? WHERE id = ?",
		Args: []any{state, edited, result, undo, time.Now().Unix(), a.ID}}, l.host.auditRow(a, decision))...)
}

// reopen puts executing approval a back to pending with input, the form it
// asks the user to fill in (audited "input_required"): nothing of it ran.
func (l *runLedger) reopen(ctx context.Context, a approvalRow, input string) error {
	return l.exec(ctx, sqlite.Statement{SQL: "UPDATE approvals SET state = CASE state WHEN 'executing' THEN 'pending' END, input = ?, updated_at = ? WHERE id = ?",
		Args: []any{input, time.Now().Unix(), a.ID}}, l.host.auditRow(a, "input_required"))
}

// stale ends the pending approvals as as stale and resumes the runs left
// with nothing open.
func (l *runLedger) stale(ctx context.Context, as []approvalRow) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	runs := map[int64]bool{}
	for _, a := range as {
		errs = append(errs, l.exec(ctx, sqlite.Statement{SQL: "UPDATE approvals SET state = 'stale', result = ?, updated_at = ? WHERE id = ? AND state = 'pending'",
			Args: []any{msgStale, time.Now().Unix(), a.ID}}, l.host.auditRow(a, "stale")))
		runs[a.RunID] = true
	}
	for run := range runs {
		errs = append(errs, l.resumeLocked(ctx, run))
	}
	return errors.Join(errs...)
}

// resume resumes runID once nothing of it is left open (resumeLocked).
func (l *runLedger) resume(ctx context.Context, runID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.resumeLocked(ctx, runID)
}

// resumeLocked resumes runID if it is interrupted with no approval left
// open: only the caller whose update changes the row resumes it. A run that
// cannot go on is failed, so it does not block the conversation. The lock is
// held.
func (l *runLedger) resumeLocked(ctx context.Context, runID int64) error {
	cerr := l.claimResume(ctx, runID)
	if cerr == nil {
		return nil
	}
	slog.Default().Warn("could not resume a run", "run", runID, "err", cerr)
	_, ferr := l.fail(ctx, runID, cerr)
	return errors.Join(fmt.Errorf("resuming the run: %w", cerr), ferr)
}

func (l *runLedger) claimResume(ctx context.Context, runID int64) error {
	if l.beforeResumeClaim != nil && l.beforeResumeClaim() {
		return nil
	}
	r, err := l.db.Execute(ctx, `UPDATE runs SET state = 'resuming', updated_at = ? WHERE id = ? AND state = 'interrupted'
		AND NOT EXISTS (SELECT 1 FROM approvals WHERE run_id = ? AND state IN ('pending', 'executing'))`, time.Now().Unix(), runID, runID)
	if err != nil {
		return err
	}
	if r.Changes == 1 {
		return l.host.resume(runID)
	}
	return nil
}

// unclaim puts a resume that never started back (resuming → interrupted):
// the next start resumes it.
func (l *runLedger) unclaim(ctx context.Context, runID int64) error {
	_, err := l.db.Execute(ctx, "UPDATE runs SET state = 'interrupted', updated_at = ? WHERE id = ? AND state = 'resuming'", time.Now().Unix(), runID)
	return err
}

// fail ends runID as failed after cause stopped it: closed like a cancel
// (pending approvals rejected with "recovery failed"), or, if that is not
// possible, just marked failed with its pending approvals rejected, so it
// never blocks the conversation. The error is only a database error from that.
func (l *runLedger) fail(ctx context.Context, runID int64, cause error) (int64, error) {
	convID, err := l.close(ctx, runID, "failed", msgRecovery)
	if err == nil {
		return convID, nil
	}
	slog.Default().Warn("could not close a failed run", "run", runID, "cause", cause, "err", err)
	return convID, l.exec(ctx, sqlite.Statement{SQL: "UPDATE approvals SET state = 'rejected', result = ?, updated_at = ? WHERE run_id = ? AND state = 'pending'",
		Args: []any{msgRecovery, time.Now().Unix(), runID}}, moveRun(runID, "failed", notOver))
}

// close ends runID as final without asking the model, in one transaction:
// its pending approvals become rejected with pendingMsg, and every call of
// its current interrupt gets its approval's result in the conversation
// (closedHistory). If that cannot be done nothing is written. No approval may
// be executing: the lock is held and no external execution of the run is in
// flight (hold waits for them), or none is left open. One left executing
// anyway is an external run whose outcome could not be recorded.
func (l *runLedger) close(ctx context.Context, runID int64, final, pendingMsg string) (int64, error) {
	rows, err := l.db.Query(ctx, "SELECT conversation_id, snapshot FROM runs WHERE id = ?", runID)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, fmt.Errorf("run %d not found", runID)
	}
	convID := store.Int(rows.Values[0][0])
	var snap snapshot
	if err := json.Unmarshal([]byte(store.Str(rows.Values[0][1])), &snap); err != nil {
		return convID, err
	}
	as, err := l.host.approvalRows(ctx, "a.run_id = ? AND a.seq >= ?", runID, snap.Round)
	if err != nil {
		return convID, err
	}
	var stmts []sqlite.Statement
	results := make([]ai.ToolResult, 0, len(as))
	now := time.Now().Unix()
	for _, a := range as {
		switch a.State {
		case "pending":
			a.State, a.result = "rejected", pendingMsg
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE approvals SET state = 'rejected', result = ?, updated_at = ? WHERE id = ? AND state = 'pending'",
				Args: []any{pendingMsg, now, a.ID}}, l.host.auditRow(a, "rejected"))
		case "executing": // an external run whose outcome could not be recorded
			a.result = msgUncertain
		}
		results = append(results, a.toolResult())
	}
	hist, after, err := l.host.closedHistory(ctx, convID, runID, snap, results)
	if err != nil {
		return convID, err
	}
	stmts = append(append(stmts, hist...), moveRun(runID, final, held))
	if err := l.exec(ctx, stmts...); err != nil {
		return convID, err
	}
	if after != nil {
		after()
	}
	return convID, nil
}

// hold cancels the runs pick names that are in flight and waits for them,
// and for their approvals executing outside the lock, to end, then returns
// holding the lock: none starts, resumes or has an approval claimed until
// release. pick (nil: none) is asked again after each wait, as a run may
// resume. Take it before editMu.
func (l *runLedger) hold(ctx context.Context, pick func() ([]int64, error)) (release func(), err error) {
	for {
		l.mu.Lock()
		var ids []int64
		if pick != nil {
			if ids, err = pick(); err != nil {
				l.mu.Unlock()
				return nil, err
			}
		}
		var wait <-chan struct{}
		for _, id := range ids {
			if job := l.active[id]; job != nil {
				job.cancel()
				wait = job.done // it may end interrupted
			} else if x := l.externalOf(id); x != nil {
				wait = x // an approved call still running: its real result goes into the history
			}
		}
		if wait == nil {
			return l.mu.Unlock, nil
		}
		l.mu.Unlock()
		select {
		case <-wait: // look again
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// externalOf is a done channel of one of runID's external executions in
// flight, or nil; the lock is held.
func (l *runLedger) externalOf(runID int64) <-chan struct{} {
	for _, x := range l.ext {
		if x.runID == runID {
			return x.done
		}
	}
	return nil
}

// recover runs at startup. A tx approval left executing never took effect
// (its writes commit with done), so it failed; an external one may have run,
// so it failed with an uncertain result (audited "uncertain") and is never
// run again. A run cut off while running failed and keeps its last saved
// state; one cut off while resuming failed with its calls answered. An
// interrupted run with nothing left open resumes. A run that cannot be
// recovered fails (fail) so the app still starts; only a database error
// while recording that is returned.
func (l *runLedger) recover(ctx context.Context) error {
	cut, err := l.host.approvalRows(ctx, "a.state = 'executing' AND a.exec_mode = 'external'")
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{{SQL: `UPDATE approvals SET state = 'failed', result = CASE exec_mode WHEN 'external' THEN ? ELSE ? END,
		updated_at = ? WHERE state = 'executing'`, Args: []any{msgUncertain, msgQuit, time.Now().Unix()}}}
	for _, a := range cut {
		stmts = append(stmts, l.host.auditRow(a, "uncertain"))
	}
	if err := l.exec(ctx, stmts...); err != nil {
		return err
	}
	rows, err := l.db.Query(ctx, "SELECT id, state FROM runs WHERE state IN ("+notOver+") ORDER BY id")
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range rows.Values {
		id := store.Int(r[0])
		switch store.Str(r[1]) {
		case "running":
			err = l.exec(ctx, moveRun(id, "failed", inFlight))
		case "resuming":
			_, err = l.close(ctx, id, "failed", msgRecovery)
		default:
			err = l.claimResume(ctx, id)
		}
		if err != nil {
			slog.Default().Warn("could not recover a run", "run", id, "err", err)
			if _, err := l.fail(ctx, id, err); err != nil {
				return err
			}
		}
	}
	return nil
}

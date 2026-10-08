package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/store"
)

// prepared is a write call's final arguments, fixed before the approval is recorded.
type prepared struct {
	Args                    json.RawMessage
	Summary, TargetKind     string
	TargetID, TargetVersion int64
}

// executor runs a write tool's approved calls.
type executor struct {
	// Prepare turns the model's arguments into the final ones before the
	// approval is recorded: the default account resolved to an explicit ID,
	// HTML sanitized, the conversation's draft ID and current revision bound.
	// A call it fails goes back to the model as an error, with no approval.
	Prepare func(ctx context.Context, conv Conversation, args json.RawMessage) (prepared, error)
	// Apply makes the statements for an approval from its recorded arguments
	// only, without committing them; result goes to the model, undo is stored.
	Apply func(ctx context.Context, a Approval) (stmts []sqlite.Statement, result, undo any, err error)
}

// Approval is a write call waiting for the user or decided. State goes
// pending → executing → done or failed, or pending → rejected or stale. Error
// is what the model was told for any end state but done.
type Approval struct {
	ID, RunID, ConversationID int64
	Seq                       int
	Tool                      string
	Args                      json.RawMessage
	Summary, State, Error     string
}

// What the model is told for an approval that did not run.
const (
	msgRejected  = "rejected by the user"
	msgStale     = "the target changed after this was proposed"
	msgCancelled = "cancelled"
	msgQuit      = "the app quit before this finished"
	msgNotSaved  = "the change could not be saved"
)

// Test hooks, nil in production; true simulates a crash at that point.
var afterApprovalClaim, beforeResumeClaim func() bool

// approvalRow is an approval with the columns only the Service uses.
type approvalRow struct {
	Approval
	callID, result, targetKind string
	targetID, targetVersion    int64
}

func (a approvalRow) toolResult() ai.ToolResult {
	r := ai.ToolResult{CallID: a.callID, Name: a.Tool}
	if a.State == "done" {
		r.Output = json.RawMessage(a.result)
	} else {
		r.Error = a.result
	}
	return r
}

func (s *Service) approvalRows(ctx context.Context, where string, args ...any) ([]approvalRow, error) {
	rows, err := s.db.Query(ctx, `SELECT a.id, a.run_id, r.conversation_id, a.seq, a.tool, a.args, a.summary, a.state, a.result,
		a.call_id, a.target_kind, a.target_id, a.target_version FROM approvals a JOIN runs r ON r.id = a.run_id
		WHERE `+where+" ORDER BY a.run_id, a.seq", args...)
	if err != nil {
		return nil, err
	}
	out := make([]approvalRow, 0, len(rows.Values))
	for _, r := range rows.Values {
		a := approvalRow{Approval: Approval{ID: store.Int(r[0]), RunID: store.Int(r[1]), ConversationID: store.Int(r[2]),
			Seq: int(store.Int(r[3])), Tool: store.Str(r[4]), Args: json.RawMessage(store.Str(r[5])), Summary: store.Str(r[6]),
			State: store.Str(r[7])}, result: store.Str(r[8]), callID: store.Str(r[9]), targetKind: store.Str(r[10]),
			targetID: store.Int(r[11]), targetVersion: store.Int(r[12])}
		if a.State != "done" {
			a.Error = a.result
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Service) approvals(ctx context.Context, where string, args ...any) ([]Approval, error) {
	rows, err := s.approvalRows(ctx, where, args...)
	out := make([]Approval, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Approval)
	}
	return out, err
}

func (s *Service) approvalRow(ctx context.Context, id int64) (approvalRow, error) {
	as, err := s.approvalRows(ctx, "a.id = ?", id)
	if err != nil {
		return approvalRow{}, err
	}
	if len(as) == 0 {
		return approvalRow{}, fmt.Errorf("approval %d not found", id)
	}
	return as[0], nil
}

func (s *Service) getApproval(ctx context.Context, id int64) (Approval, error) {
	a, err := s.approvalRow(ctx, id)
	return a.Approval, err
}

// ListApprovals returns the conversation's approvals by run and seq, so the
// cards survive a restart.
func (s *Service) ListApprovals(ctx context.Context, conversationID int64) ([]Approval, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	return s.approvals(ctx, "r.conversation_id = ?", conversationID)
}

// ResolveApproval accepts or rejects a pending approval, exactly once: the
// call that claims it (pending → executing) decides it; any other gets
// ErrInvalidState. Accepting applies the recorded arguments and marks it done
// in one transaction, after checking the target draft's revision (changed →
// stale, ErrStaleDraft). The call that ends the run's last open approval
// resumes the run in the background; SubscribeConversation streams it.
func (s *Service) ResolveApproval(ctx context.Context, id int64, accept bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	return s.resolveApproval(ctx, id, accept)
}

func (s *Service) resolveApproval(ctx context.Context, id int64, accept bool) error {
	a, err := s.approvalRow(ctx, id)
	if err != nil {
		return err
	}
	var ex *executor
	if accept {
		if ex = s.executor(a.Tool); ex == nil {
			return fmt.Errorf("tool %s is not available", a.Tool)
		}
	}
	// ponytail: one lock for every run's approvals, starts and cancels; per run if it ever contends
	s.runMu.Lock()
	defer s.runMu.Unlock()
	r, err := s.db.Execute(ctx, "UPDATE approvals SET state = 'executing', updated_at = ? WHERE id = ? AND state = 'pending'", time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return ErrInvalidState
	}
	if afterApprovalClaim != nil && afterApprovalClaim() {
		return errors.New("simulated crash")
	}
	ctx = context.WithoutCancel(ctx) // claimed: always reach an end state
	err = s.execute(ctx, a, ex)
	s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: a.ConversationID})
	s.claimResume(ctx, a.RunID)
	return err
}

// execute ends a claimed approval: rejected when ex is nil, else applied.
func (s *Service) execute(ctx context.Context, a approvalRow, ex *executor) error {
	finish := func(state, result string, undo any, decision string, stmts ...sqlite.Statement) error {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE approvals SET state = ?, result = ?, undo = ?, updated_at = ? WHERE id = ? AND state = 'executing'",
			Args: []any{state, result, undo, time.Now().Unix(), a.ID}},
			gateway.Audit(gateway.SourceWit, a.Tool, a.targetKind, a.targetID, decision))
		_, err := s.db.Transaction(ctx, stmts)
		return err
	}
	if ex == nil {
		return finish("rejected", msgRejected, nil, "rejected")
	}
	stmts, result, undo, err := ex.Apply(ctx, a.Approval)
	var res, und []byte
	if err == nil {
		res, err = json.Marshal(result)
	}
	if err == nil && undo != nil {
		und, err = json.Marshal(undo)
	}
	if err != nil {
		return errors.Join(err, finish("failed", err.Error(), nil, "failed"))
	}
	var undoText any // NULL without undo
	if und != nil {
		undoText = string(und)
	}
	if a.targetKind == "draft" {
		// Every draft write holds editMu, so the revision read here holds until
		// the commit; the guard statement checks it again inside the transaction.
		s.editMu.Lock()
		defer s.editMu.Unlock()
		rows, err := s.db.Query(ctx, "SELECT 1 FROM drafts WHERE id = ? AND revision = ?", a.targetID, a.targetVersion)
		if err != nil {
			return errors.Join(err, finish("failed", msgNotSaved, nil, "failed"))
		}
		if len(rows.Values) == 0 {
			return errors.Join(ErrStaleDraft, finish("stale", msgStale, nil, "stale"))
		}
		// approvals.state is NOT NULL: a changed revision fails the transaction.
		stmts = append([]sqlite.Statement{{SQL: "UPDATE approvals SET state = NULL WHERE id = ? AND (SELECT revision FROM drafts WHERE id = ?) IS NOT ?",
			Args: []any{a.ID, a.targetID, a.targetVersion}}}, stmts...)
	}
	if err := finish("done", string(res), undoText, "executed", stmts...); err != nil {
		return errors.Join(err, finish("failed", msgNotSaved, nil, "failed"))
	}
	return nil
}

// claimResume resumes runID if it is interrupted with no approval left open.
// Only the caller whose update changes the row resumes it. The caller holds runMu.
func (s *Service) claimResume(ctx context.Context, runID int64) {
	if beforeResumeClaim != nil && beforeResumeClaim() {
		return
	}
	r, err := s.db.Execute(ctx, `UPDATE runs SET state = 'resuming', updated_at = ? WHERE id = ? AND state = 'interrupted'
		AND NOT EXISTS (SELECT 1 FROM approvals WHERE run_id = ? AND state IN ('pending', 'executing'))`, time.Now().Unix(), runID, runID)
	if err != nil { // it stays interrupted; the next start resumes it
		slog.Default().Warn("could not claim a run to resume", "run", runID, "err", err)
		return
	}
	if r.Changes == 1 {
		s.resume(runID)
	}
}

// ResolveAll resolves the run's pending approvals one by one in seq order.
func (s *Service) ResolveAll(ctx context.Context, runID int64, accept bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	as, err := s.approvalRows(ctx, "a.run_id = ? AND a.state = 'pending'", runID)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range as {
		if err := s.resolveApproval(ctx, a.ID, accept); !errors.Is(err, ErrInvalidState) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CancelRun ends a run when its panel closes. A run in flight is cancelled;
// an interrupted one has its pending approvals rejected ("cancelled") and those
// results added to the conversation state without asking the model, so the
// conversation can take a new run. A run already over is left as it is.
func (s *Service) CancelRun(ctx context.Context, runID int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	for {
		s.runMu.Lock()
		job := s.active[runID]
		if job == nil {
			break
		}
		s.runMu.Unlock()
		job.cancel()
		select {
		case <-job.done: // it may have ended interrupted: look again
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer s.runMu.Unlock()
	rows, err := s.db.Query(ctx, "SELECT state FROM runs WHERE id = ?", runID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("run %d not found", runID)
	}
	if store.Str(rows.Values[0][0]) != "interrupted" {
		return nil
	}
	convID, err := s.closeRun(ctx, runID, "cancelled")
	s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: convID})
	return err
}

// closeRun ends runID as final without asking the model: its pending
// approvals become rejected ("cancelled"), and every call of its current
// interrupt gets its approval's result in the conversation state. No approval
// may be executing: the caller holds runMu, or none is left open.
func (s *Service) closeRun(ctx context.Context, runID int64, final string) (int64, error) {
	rows, err := s.db.Query(ctx, "SELECT r.conversation_id, r.snapshot, c.state FROM runs r JOIN conversations c ON c.id = r.conversation_id WHERE r.id = ?", runID)
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
	state, _ := rows.Values[0][2].([]byte)
	as, err := s.approvalRows(ctx, "a.run_id = ? AND a.seq >= ?", runID, snap.Round)
	if err != nil {
		return convID, err
	}
	var stmts []sqlite.Statement
	results := make([]ai.ToolResult, 0, len(as))
	now := time.Now().Unix()
	for _, a := range as {
		if a.State == "pending" {
			a.State, a.result = "rejected", msgCancelled
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE approvals SET state = 'rejected', result = ?, updated_at = ? WHERE id = ? AND state = 'pending'",
				Args: []any{msgCancelled, now, a.ID}}, gateway.Audit(gateway.SourceWit, a.Tool, a.targetKind, a.targetID, "rejected"))
		}
		results = append(results, a.toolResult())
	}
	closed, err := ai.CloseState(state, results)
	if err == nil {
		stmts = append(stmts, saveState(convID, closed))
	}
	stmts = append(stmts, runState(runID, final))
	_, terr := s.db.Transaction(ctx, stmts)
	return convID, errors.Join(err, terr)
}

// recoverRuns runs at startup. An approval left executing never took effect
// (its writes commit with done), so it failed. A run cut off while running
// failed and keeps its last saved state; one cut off while resuming failed
// with its calls answered. An interrupted run with nothing left open resumes.
func (s *Service) recoverRuns(ctx context.Context) error {
	if _, err := s.db.Execute(ctx, "UPDATE approvals SET state = 'failed', result = ?, updated_at = ? WHERE state = 'executing'",
		msgQuit, time.Now().Unix()); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, "SELECT id, state FROM runs WHERE state IN ('running', 'resuming', 'interrupted') ORDER BY id")
	if err != nil {
		return err
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	for _, r := range rows.Values {
		id := store.Int(r[0])
		err = nil
		switch store.Str(r[1]) {
		case "running":
			_, err = s.db.Execute(ctx, runState(id, "failed").SQL, runState(id, "failed").Args...)
		case "resuming":
			_, err = s.closeRun(ctx, id, "failed")
		default:
			s.claimResume(ctx, id)
		}
		if err != nil {
			slog.Default().Warn("could not recover a run", "run", id, "err", err)
		}
	}
	return nil
}

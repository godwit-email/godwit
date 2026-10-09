package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
)

// externalTimeout bounds an external Run whose tool sets no Timeout.
const externalTimeout = 60 * time.Second

// extExec is an approval executing outside runMu; done closes when it ended.
type extExec struct {
	runID int64
	done  chan struct{}
}

// externalOf is a done channel of one of runID's external executions in
// flight, or nil. The caller holds runMu.
func (s *Service) externalOf(runID int64) <-chan struct{} {
	for _, x := range s.extRun {
		if x.runID == runID {
			return x.done
		}
	}
	return nil
}

// decideExternal accepts a pending approval of an external tool: it claims it
// (pending → executing, exec_mode external) under runMu, then runs it without
// the lock, so other runs and approvals go on meanwhile, and records the
// outcome with its audit row in one transaction. Run's context ends when the
// Service closes or after the tool's timeout, not with the caller's. If the
// outcome cannot be recorded the approval stays executing, the error is
// returned, and the next start fails it as uncertain: it is never run again.
// With resume, the run resumes once nothing of it is left open.
func (s *Service) decideExternal(ctx context.Context, a approvalRow, d toolDesc, resume bool) error {
	rctx, end, err := s.begin(context.Background(), 0) // Close cancels Run and waits for its outcome
	if err != nil {
		return err
	}
	defer end()
	s.runMu.Lock()
	r, err := s.db.Execute(ctx, "UPDATE approvals SET state = 'executing', exec_mode = 'external', updated_at = ? WHERE id = ? AND state = 'pending'",
		time.Now().Unix(), a.ID)
	switch {
	case err == nil && r.Changes == 0:
		err = ErrInvalidState
	case err == nil && afterApprovalClaim != nil && afterApprovalClaim():
		err = errors.New("simulated crash")
	}
	if err != nil {
		s.runMu.Unlock()
		return err
	}
	x := extExec{runID: a.RunID, done: make(chan struct{})}
	if s.extRun == nil {
		s.extRun = map[int64]extExec{}
	}
	s.extRun[a.ID] = x
	s.runMu.Unlock()
	s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: a.ConversationID})

	err = s.runExternal(rctx, a, d)
	s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: a.ConversationID})
	s.runMu.Lock()
	defer s.runMu.Unlock()
	delete(s.extRun, a.ID)
	close(x.done)
	if resume {
		err = errors.Join(err, s.resumeRun(context.WithoutCancel(rctx), a.RunID))
	}
	return err
}

// runExternal checks the binding, runs the claimed approval and records the outcome.
func (s *Service) runExternal(ctx context.Context, a approvalRow, d toolDesc) error {
	db := context.WithoutCancel(ctx) // claimed: always try to record the outcome
	record := func(state, result, decision string) error {
		_, err := s.db.Transaction(db, []sqlite.Statement{{SQL: "UPDATE approvals SET state = ?, result = ?, updated_at = ? WHERE id = ? AND state = 'executing'",
			Args: []any{state, result, time.Now().Unix(), a.ID}}, a.audit(decision)})
		if err != nil {
			slog.Default().Warn("could not record an external approval's outcome", "approval", a.ID, "state", state, "err", err)
		}
		return err
	}
	if d.Binding != nil {
		if cur, err := d.Binding(db); err != nil || cur != a.binding {
			return errors.Join(ErrStaleApproval, record("stale", msgStale, "stale"))
		}
	}
	rc, cancel := context.WithTimeout(ctx, cmp.Or(d.Timeout, externalTimeout))
	res, err := d.Run(rc, a.Approval)
	cancel()
	var out []byte
	if err == nil {
		out, err = json.Marshal(res)
	}
	if err != nil {
		result := err.Error()
		if rc.Err() != nil { // cut off by the timeout or Close: it may have done part of its work
			result = msgUncertain
		}
		return errors.Join(err, record("failed", result, "failed"))
	}
	return record("done", string(out), "executed")
}

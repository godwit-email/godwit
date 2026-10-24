package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
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
			Args: []any{state, result, time.Now().Unix(), a.ID}}, s.auditRow(a, decision)})
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
	res, err := d.Run(rc, a)
	// Before cancel: only the timeout or Close ended it, the plugin's own
	// limits or stop interrupted it mid-call, or an MCP call's answer was lost.
	cut := rc.Err() != nil || errors.Is(err, plugins.ErrTimeout) || errors.Is(err, plugins.ErrMemoryLimit) ||
		errors.Is(err, plugins.ErrClosed) || errors.Is(err, errMCPMaybeRan)
	cancel()
	if ni := (*needsInput)(nil); errors.As(err, &ni) { // nothing ran: it waits for the user's answer
		if ok, err := s.askAgain(db, a, ni.in); ok || err != nil {
			return err
		}
		return errors.Join(errNoFormHost, record("failed", errNoFormHost.Error(), "failed"))
	}
	if errors.Is(err, ErrStaleApproval) { // the tool refused: what it was bound to changed, nothing ran
		return errors.Join(ErrStaleApproval, record("stale", msgStale, "stale"))
	}
	if err != nil {
		result := s.clean(err.Error())
		if cut { // it may have done part of its work
			result = msgUncertain
		}
		return errors.Join(err, record("failed", result, "failed"))
	}
	out, err := s.cleanJSON(res)
	if err != nil {
		return errors.Join(err, record("failed", s.clean(err.Error()), "failed"))
	}
	return record("done", out, "executed")
}

// cleanJSON is v as JSON, cleaned as decoded data and then encoded: the
// encoding escapes characters a secret may hold (<, >, &), so the encoded
// text would hide it from Redact.
func (s *Service) cleanJSON(v any) (string, error) {
	out, err := json.Marshal(v)
	var plain any
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(out))
		dec.UseNumber() // numbers stay exact (json.Number passes cleanValue untouched)
		err = dec.Decode(&plain)
	}
	if err == nil {
		out, err = json.Marshal(s.cleanValue(plain))
	}
	return string(out), err
}

// cleanValue cleans every string, map keys included, of decoded JSON.
func (s *Service) cleanValue(v any) any {
	switch v := v.(type) {
	case string:
		return s.clean(v)
	case []any:
		for i := range v {
			v[i] = s.cleanValue(v[i])
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[s.clean(k)] = s.cleanValue(x)
		}
		return out
	}
	return v
}

// clean makes text from or about an extension safe to keep or return:
// secrets of the Service's secret set redacted, absolute paths scrubbed.
func (s *Service) clean(text string) string { return plugins.ScrubPaths(s.secrets.Redact(text)) }

// cleanErr is an error whose text was cleaned; errors.Is still matches the
// original, but nothing hands out its text.
type cleanErr struct {
	msg string
	err error
}

func (e cleanErr) Error() string        { return e.msg }
func (e cleanErr) Is(target error) bool { return errors.Is(e.err, target) }

// cleanError is err with its text cleaned, for errors leaving the Service
// from approvals, which may carry an extension's words.
func (s *Service) cleanError(err error) error {
	if err == nil {
		return nil
	}
	return cleanErr{s.clean(err.Error()), err}
}

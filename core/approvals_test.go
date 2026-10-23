package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// approvalEnv is a skill env with two test write tools: fake_draft creates a
// draft for the "default account" acct, fake_edit renames the conversation's draft.
type approvalEnv struct {
	skillEnv
	applied atomic.Int32
	acct    atomic.Int64
}

func newApprovalEnv(t *testing.T) *approvalEnv {
	t.Helper()
	e := &approvalEnv{skillEnv: newSkillEnv(t)}
	e.acct.Store(1)
	e.register(e.s)
	return e
}

type subjectArgs struct {
	AccountID int64 `json:"account_id"`
	DraftID   int64 `json:"draft_id"`
	Subject   string
}

func (e *approvalEnv) register(s *Service) {
	s.registerExecutorForTest("fake_draft", executor{
		Prepare: func(ctx context.Context, conv Conversation, args json.RawMessage) (prepared, error) {
			var in subjectArgs
			if err := json.Unmarshal(args, &in); err != nil || in.Subject == "" {
				return prepared{}, errors.New("subject is required")
			}
			in.AccountID = e.acct.Load() // the default account, resolved now
			b, _ := json.Marshal(in)
			return prepared{Args: b, Summary: "New draft: " + in.Subject}, nil
		},
		Apply: func(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
			e.applied.Add(1)
			var in subjectArgs
			if err := json.Unmarshal(a.Args, &in); err != nil {
				return nil, nil, nil, err
			}
			return []sqlite.Statement{{SQL: `INSERT INTO drafts(account_id, mode, subject, body_html, updated_at, ai_approval_id)
				VALUES (?, 'new', ?, '', 0, ?)`, Args: []any{in.AccountID, in.Subject, a.ID}}}, map[string]bool{"created": true}, nil, nil
		},
	})
	s.registerExecutorForTest("fake_edit", executor{
		Prepare: func(ctx context.Context, conv Conversation, args json.RawMessage) (prepared, error) {
			var in subjectArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return prepared{}, err
			}
			d, err := s.GetDraft(ctx, conv.DraftID)
			if err != nil {
				return prepared{}, err
			}
			in.DraftID = d.ID
			b, _ := json.Marshal(in)
			return prepared{Args: b, Summary: "Rename", TargetKind: "draft", TargetID: d.ID, TargetVersion: d.Revision}, nil
		},
		Apply: func(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
			e.applied.Add(1)
			var in subjectArgs
			if err := json.Unmarshal(a.Args, &in); err != nil {
				return nil, nil, nil, err
			}
			return []sqlite.Statement{{SQL: "UPDATE drafts SET subject = ?, revision = revision + 1 WHERE id = ?",
				Args: []any{in.Subject, in.DraftID}}}, "renamed", map[string]string{"subject": "old"}, nil
		},
	})
}

func (e *approvalEnv) newConv(t *testing.T, kind string, draftID int64) Conversation {
	t.Helper()
	c, err := e.s.NewConversation(t.Context(), kind, draftID)
	require.NoError(t, err)
	return c
}

func drainWit(t *testing.T, ch <-chan WitEvent) []WitEvent {
	t.Helper()
	var evs []WitEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("run did not end")
		}
	}
}

// start runs prompt and returns its events; the last one ends the run.
func (e *approvalEnv) start(t *testing.T, conv Conversation, prompt string) []WitEvent {
	t.Helper()
	ch, err := e.s.startRun(t.Context(), conv, prompt, e.s.registered("fake_draft", "fake_edit"))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.NotEmpty(t, evs)
	return evs
}

// interrupt scripts one model turn calling the tools, then "Finished.", and
// returns the interrupted event.
func (e *approvalEnv) interrupt(t *testing.T, conv Conversation, calls ...testllm.Call) WitEvent {
	t.Helper()
	e.srv.Script(testllm.Turn{Calls: calls}, testllm.Turn{Text: []string{"Finished."}})
	evs := e.start(t, conv, "do it")
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, len(calls))
	return last
}

func (e *approvalEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	rows, err := e.s.db.Query(t.Context(), q, args...)
	require.NoError(t, err)
	return int(store.Int(rows.Values[0][0]))
}

func (e *approvalEnv) approval(t *testing.T, id int64) Approval {
	t.Helper()
	a, err := e.s.getApproval(t.Context(), id)
	require.NoError(t, err)
	return a
}

func (e *approvalEnv) waitRun(t *testing.T, runID int64, state string) {
	t.Helper()
	require.Eventually(t, func() bool {
		rows, err := e.s.db.Query(context.Background(), "SELECT state FROM runs WHERE id = ?", runID)
		return err == nil && store.Str(rows.Values[0][0]) == state
	}, 10*time.Second, 5*time.Millisecond)
}

// restart closes the service and opens a new one on the same data directory.
func (e *approvalEnv) restart(t *testing.T) {
	t.Helper()
	dir := e.s.dataDir
	require.NoError(t, e.s.Close())
	s, err := New(t.Context(), Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	e.register(s)
	e.s = s
}

func waitTerminal(t *testing.T, ch <-chan WitEvent) WitEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == "done" || ev.Kind == "error" || ev.Kind == "interrupted" {
				return ev
			}
		case <-timeout:
			t.Fatal("no terminal event")
		}
	}
}

// toolPairs returns the tool call IDs and tool result IDs of an openai request.
func toolPairs(t *testing.T, req testllm.Request) (calls, results []string) {
	t.Helper()
	var msgs []map[string]any
	require.NoError(t, json.Unmarshal(req.Messages, &msgs))
	for _, m := range msgs {
		if id, ok := m["tool_call_id"].(string); ok {
			results = append(results, id)
		}
		tcs, _ := m["tool_calls"].([]any)
		for _, tc := range tcs {
			calls = append(calls, tc.(map[string]any)["id"].(string))
		}
	}
	slices.Sort(calls)
	slices.Sort(results)
	return calls, results
}

func TestApproveExecutesOnce(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	a := in.Approvals[0]
	assert.Equal(t, "pending", a.State)
	assert.Equal(t, "fake_draft", a.Tool)
	assert.Equal(t, "New draft: Hi", a.Summary)
	assert.Equal(t, conv.ID, a.ConversationID)
	assert.Equal(t, in.RunID, a.RunID)
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM drafts"), "nothing is written before approval")
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'fake_draft' AND decision = 'approve'"))

	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrInvalidState)
	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, false), ErrInvalidState)
	assert.Equal(t, "done", waitTerminal(t, sub).Kind)
	e.waitRun(t, in.RunID, "done")

	assert.Equal(t, int32(1), e.applied.Load())
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM drafts WHERE ai_approval_id = ? AND subject = 'Hi'", a.ID))
	assert.Equal(t, "done", e.approval(t, a.ID).State)
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'fake_draft' AND decision = 'executed'"))
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	calls, results := toolPairs(t, reqs[1])
	assert.Equal(t, calls, results)
	assert.Len(t, results, 1)

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.NotEmpty(t, savedMessages(t, e.s, c.ID))
	assert.Equal(t, "Finished.", savedMessages(t, e.s, c.ID)[len(savedMessages(t, e.s, c.ID))-1].Text)
}

func TestConcurrentApproveSameCard(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	a := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`}).Approvals[0]
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range errs {
		wg.Go(func() { <-start; errs[i] = e.s.ResolveApproval(t.Context(), a.ID, true) })
	}
	close(start)
	wg.Wait()
	ok, invalid := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrInvalidState):
			invalid++
		}
	}
	assert.Equal(t, 1, ok, "%v", errs)
	assert.Equal(t, 1, invalid, "%v", errs)
	assert.Equal(t, int32(1), e.applied.Load())
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM drafts"))
}

func TestRejectNeverExecutes(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	a := in.Approvals[0]
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, false))
	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrInvalidState)
	e.waitRun(t, in.RunID, "done")
	assert.Zero(t, e.applied.Load())
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM drafts"))
	got := e.approval(t, a.ID)
	assert.Equal(t, "rejected", got.State)
	assert.Equal(t, msgRejected, got.Error)
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'fake_draft' AND decision = 'rejected'"))
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[1].Messages), msgRejected)
}

func TestStaleTargetRejected(t *testing.T) {
	e := newApprovalEnv(t)
	d, err := e.s.SaveDraft(t.Context(), Draft{AccountID: 1, Mode: "new", Subject: "Draft"})
	require.NoError(t, err)
	conv := e.newConv(t, "assist", d.ID)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_edit", Args: `{"subject":"From the model"}`})
	a := in.Approvals[0]
	// The user edits within the same second: only the revision tells.
	d.Subject = "From the user"
	_, err = e.s.SaveDraft(t.Context(), d)
	require.NoError(t, err)

	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrStaleDraft)
	e.waitRun(t, in.RunID, "done")
	got, err := e.s.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, "From the user", got.Subject)
	assert.Equal(t, d.Revision+1, got.Revision)
	ap := e.approval(t, a.ID)
	assert.Equal(t, "stale", ap.State)
	assert.Equal(t, msgStale, ap.Error)
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'fake_edit' AND decision = 'stale'"))
	assert.Contains(t, string(e.srv.Requests()[1].Messages), msgStale)
}

func TestFinalArgsSnapshot(t *testing.T) {
	e := newApprovalEnv(t)
	_, err := e.s.db.Execute(t.Context(), "INSERT INTO accounts(id, email) VALUES (2, 'other@x')")
	require.NoError(t, err)
	conv := e.newConv(t, "ask", 0)
	a := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`}).Approvals[0]
	assert.JSONEq(t, `{"account_id":1,"draft_id":0,"Subject":"Hi"}`, string(a.Args))
	e.acct.Store(2) // the default account changes before the user approves
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM drafts WHERE account_id = 1"))
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM drafts WHERE account_id = 2"))
}

func TestTwoPendingResumeOnce(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`}, testllm.Call{Name: "fake_draft", Args: `{"subject":"B"}`})
	assert.Equal(t, 0, in.Approvals[0].Seq)
	assert.Equal(t, 1, in.Approvals[1].Seq)
	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, a := range in.Approvals {
		wg.Go(func() { <-start; assert.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true)) })
	}
	close(start)
	wg.Wait()
	assert.Equal(t, "done", waitTerminal(t, sub).Kind)
	e.waitRun(t, in.RunID, "done")
	time.Sleep(50 * time.Millisecond) // a second resume would have asked by now
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2, "resumed exactly once")
	calls, results := toolPairs(t, reqs[1])
	assert.Len(t, results, 2)
	assert.Equal(t, calls, results)
	assert.Equal(t, int32(2), e.applied.Load())
	select {
	case ev := <-sub:
		t.Fatalf("unexpected event after the resume: %+v", ev)
	default:
	}
}

func TestResolveAllInOrder(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`}, testllm.Call{Name: "fake_draft", Args: `{"subject":"B"}`})
	require.NoError(t, e.s.ResolveAll(t.Context(), in.RunID, true))
	e.waitRun(t, in.RunID, "done")
	rows, err := e.s.db.Query(t.Context(), "SELECT subject FROM drafts ORDER BY id")
	require.NoError(t, err)
	require.Len(t, rows.Values, 2)
	assert.Equal(t, "A", store.Str(rows.Values[0][0]))
	assert.Equal(t, "B", store.Str(rows.Values[1][0]))
}

func TestApprovalSurvivesRestart(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	e.restart(t)
	as, err := e.s.ListApprovals(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, as, 1)
	assert.Equal(t, "pending", as[0].State)
	time.Sleep(20 * time.Millisecond)
	require.Len(t, e.srv.Requests(), 1, "a pending run does not resume on start")

	require.NoError(t, e.s.ResolveApproval(t.Context(), as[0].ID, true))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, int32(1), e.applied.Load())
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM drafts"))
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	_, results := toolPairs(t, reqs[1])
	assert.Len(t, results, 1)
}

func TestCrashWhileExecuting(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	a := in.Approvals[0]
	afterApprovalClaim = func() bool { return true }
	t.Cleanup(func() { afterApprovalClaim = nil })
	require.Error(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	afterApprovalClaim = nil
	assert.Equal(t, "executing", e.approval(t, a.ID).State)

	e.restart(t)
	e.waitRun(t, in.RunID, "done") // nothing is pending any more: the run resumes once
	got := e.approval(t, a.ID)
	assert.Equal(t, "failed", got.State)
	assert.Equal(t, msgQuit, got.Error)
	assert.Zero(t, e.applied.Load())
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM drafts"))
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[1].Messages), msgQuit)
}

func TestCrashBeforeResume(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	beforeResumeClaim = func() bool { return true }
	t.Cleanup(func() { beforeResumeClaim = nil })
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	beforeResumeClaim = nil
	assert.Equal(t, "done", e.approval(t, in.Approvals[0].ID).State)
	e.waitRun(t, in.RunID, "interrupted")

	e.restart(t)
	e.waitRun(t, in.RunID, "done")
	time.Sleep(50 * time.Millisecond)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2, "resumed exactly once")
	_, results := toolPairs(t, reqs[1])
	assert.Len(t, results, 1)
	assert.Equal(t, int32(1), e.applied.Load(), "not executed again")
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM drafts"))
}

func TestCancelRunClosesHistory(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`}, testllm.Call{Name: "fake_draft", Args: `{"subject":"B"}`})
	evs, unsub := e.s.Subscribe()
	defer unsub()
	require.NoError(t, e.s.CancelRun(t.Context(), in.RunID))
	e.waitRun(t, in.RunID, "cancelled")
	for _, a := range in.Approvals {
		got := e.approval(t, a.ID)
		assert.Equal(t, "rejected", got.State)
		assert.Equal(t, msgCancelled, got.Error)
	}
	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true), ErrInvalidState)
	assert.Zero(t, e.applied.Load())
	assert.Len(t, e.srv.Requests(), 1, "cancelling does not ask the model")
	select {
	case ev := <-evs:
		assert.Equal(t, "approvals.changed", ev.Type)
		assert.Equal(t, conv.ID, ev.ConversationID)
	case <-time.After(time.Second):
		t.Fatal("no approvals.changed")
	}

	evs2 := e.start(t, conv, "something else")
	assert.Equal(t, "done", evs2[len(evs2)-1].Kind)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	calls, results := toolPairs(t, reqs[1])
	assert.Len(t, calls, 2)
	assert.Equal(t, calls, results, "every tool request in the history has its result")
}

func TestOneActiveRun(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)

	// running
	e.srv.Script(testllm.Turn{Text: []string{"..."}, Hang: true})
	ch, err := e.s.startRun(t.Context(), conv, "first", nil)
	require.NoError(t, err)
	<-ch // the first text: the run is in flight
	_, err = e.s.startRun(t.Context(), conv, "second", nil)
	require.ErrorIs(t, err, ErrRunActive)
	e.srv.Release()
	evs := drainWit(t, ch)
	assert.Equal(t, "done", evs[len(evs)-1].Kind)

	// interrupted
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`})
	_, err = e.s.startRun(t.Context(), conv, "third", nil)
	require.ErrorIs(t, err, ErrRunActive)
	require.NoError(t, e.s.CancelRun(t.Context(), in.RunID))
	evs = e.start(t, conv, "fourth")
	assert.Equal(t, "done", evs[len(evs)-1].Kind)

	// another conversation is independent
	other := e.newConv(t, "ask", 0)
	e.interrupt(t, other, testllm.Call{Name: "fake_draft", Args: `{"subject":"B"}`})
	evs = e.start(t, conv, "fifth")
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
}

func TestCloseWaitsForResume(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"Hi"}`}}}, testllm.Turn{Text: []string{"..."}, Hang: true})
	evs := e.start(t, conv, "do it")
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind)
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	require.Eventually(t, func() bool { return len(e.srv.Requests()) == 2 }, 10*time.Second, 5*time.Millisecond)

	require.NoError(t, e.s.Close())
	// Close returned after the resume recorded its outcome: cancelled, not left
	// resuming (which the next start would turn into failed).

	e.restart(t)
	rows, err := e.s.db.Query(t.Context(), "SELECT state FROM runs WHERE id = ?", in.RunID)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", store.Str(rows.Values[0][0]))
	// The conversation can go on: the approved call has its result.
	e.srv.Release()
	evs = e.start(t, conv, "next")
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	calls, results := toolPairs(t, e.srv.Requests()[2])
	assert.Equal(t, calls, results)
	assert.Equal(t, int32(1), e.applied.Load())
}

func TestPrepareFailureNeedsNoApproval(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{}`}}}, testllm.Turn{Text: []string{"Sorry."}})
	evs := e.start(t, conv, "do it")
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM approvals"))
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[1].Messages), "subject is required")
}

// collectStream closes w and reads everything it delivers.
func collectStream(w *witStream) []WitEvent {
	w.close()
	var evs []WitEvent
	for ev := range w.ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestWitStreamKeepsTerminal(t *testing.T) {
	w := newWitStream()
	for i := range witBuffer * 2 {
		w.push(WitEvent{Event: ai.Event{Kind: "text", Text: fmt.Sprint(i)}})
		if i == 3 {
			w.push(WitEvent{Event: ai.Event{Kind: "done"}})
		}
	}
	evs := collectStream(w)
	var texts []string
	done := 0
	for _, ev := range evs {
		if ev.Kind == "done" {
			done++
		} else {
			texts = append(texts, ev.Text)
		}
	}
	assert.Equal(t, 1, done, "the terminal event survives")
	assert.LessOrEqual(t, len(texts), witBuffer+1, "older text was dropped")
	assert.Equal(t, fmt.Sprint(witBuffer*2-1), texts[len(texts)-1], "the newest text is kept")
}

func TestWitStreamKeepsAllTerminals(t *testing.T) {
	w := newWitStream()
	for i := range 3 * witBuffer {
		w.push(WitEvent{Event: ai.Event{Kind: "text"}})
		w.push(WitEvent{RunID: int64(i), Event: ai.Event{Kind: "done"}})
	}
	var runs []int64
	for _, ev := range collectStream(w) {
		if ev.Kind == "done" {
			runs = append(runs, ev.RunID)
		}
	}
	require.Len(t, runs, 3*witBuffer, "a slow reader still gets every terminal event")
	assert.True(t, slices.IsSorted(runs))

	w = newWitStream()
	for range witBuffer {
		w.push(WitEvent{Event: ai.Event{Kind: "interrupted"}})
	}
	w.push(WitEvent{Event: ai.Event{Kind: "text"}})
	n := 0
	for _, ev := range collectStream(w) {
		if ev.Kind == "interrupted" {
			n++
		}
	}
	assert.Equal(t, witBuffer, n, "text never pushes out a terminal event")
}

func TestSubscriptionsCloseWithService(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	stalled, unsub := e.s.SubscribeConversation(conv.ID) // never read
	defer unsub()
	chunks := make([]string, 3*witBuffer)
	for i := range chunks {
		chunks[i] = "x"
	}
	e.srv.Script(testllm.Turn{Text: chunks})
	evs := e.start(t, conv, "hello")
	require.Equal(t, "done", evs[len(evs)-1].Kind)

	closed := make(chan error, 1)
	go func() { closed <- e.s.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close waits on a stalled consumer")
	}
	select {
	case _, ok := <-stalled:
		assert.False(t, ok, "Close ends delivery: queued events are discarded and the channel is closed")
	case <-time.After(time.Second):
		t.Fatal("the subscription stays open after Close")
	}

	after, unsub2 := e.s.SubscribeConversation(conv.ID)
	defer unsub2()
	select {
	case _, ok := <-after:
		assert.False(t, ok, "a subscription after Close is closed")
	case <-time.After(time.Second):
		t.Fatal("subscription after Close stays open")
	}
}

func TestCancelStillDeliversTerminal(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
	ctx, cancel := context.WithCancel(t.Context())
	ch, err := e.s.startRun(ctx, conv, "hello", nil)
	require.NoError(t, err)
	require.Equal(t, "start", (<-ch).Kind)
	assert.Equal(t, "text", (<-ch).Kind)
	cancel()
	time.Sleep(50 * time.Millisecond) // a slow reader
	evs := drainWit(t, ch)
	require.NotEmpty(t, evs, "the stream delivers until the run's terminal event")
	last := evs[len(evs)-1]
	require.Equal(t, "error", last.Kind)
	assert.Equal(t, ai.CodeCanceled, last.Err.Code)
}

func TestStartupResumeFailureErrorsNew(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"A"}`}}})
	evs := e.start(t, conv, "do it")
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind)
	beforeResumeClaim = func() bool { return true }
	t.Cleanup(func() { beforeResumeClaim = nil })
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	beforeResumeClaim = nil
	// The resume cannot start (its model is gone), and recording its failure fails too.
	_, err := e.s.db.Execute(t.Context(), "UPDATE runs SET model = 'gone/m' WHERE id = ?", in.RunID)
	require.NoError(t, err)
	_, err = e.s.db.Execute(t.Context(), `CREATE TRIGGER fail_failed BEFORE UPDATE OF state ON runs WHEN NEW.state = 'failed'
		BEGIN SELECT RAISE(ABORT, 'injected fail-run failure'); END`)
	require.NoError(t, err)
	dir := e.s.dataDir
	require.NoError(t, e.s.Close())
	s, err := New(t.Context(), Options{DataDir: dir})
	if err == nil {
		s.Close()
	}
	require.ErrorContains(t, err, "injected fail-run failure")
}

func TestCancelAfterRunHandOff(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	hold := make(chan struct{})
	var first atomic.Bool
	beforeUntrack = func(int64) {
		if first.CompareAndSwap(false, true) {
			<-hold // the interrupted run's cleanup waits until its resume is running
		}
	}
	t.Cleanup(func() { beforeUntrack = nil })
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"Hi"}`}}}, testllm.Turn{Text: []string{"..."}, Hang: true})
	evs := e.start(t, conv, "do it")
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind)
	e.s.runMu.Lock()
	old := e.s.active[in.RunID] // the interrupted run, its cleanup held
	e.s.runMu.Unlock()
	require.NotNil(t, old)
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	require.Eventually(t, func() bool { return len(e.srv.Requests()) == 2 }, 10*time.Second, 5*time.Millisecond)
	close(hold) // now the old run's cleanup runs
	<-old.done

	require.NoError(t, e.s.CancelRun(t.Context(), in.RunID))
	e.waitRun(t, in.RunID, "cancelled")
}

func TestNewRunSeesFinishedHistory(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"Alpha answer"}, Hang: true})
	ch, err := e.s.startRun(t.Context(), conv, "first", nil)
	require.NoError(t, err)
	<-ch // the first run is in flight
	var once sync.Once
	beforeRunClaim = func(context.Context) {
		once.Do(func() { // the first run finishes after the second read the conversation
			e.srv.Release()
			drainWit(t, ch)
		})
	}
	t.Cleanup(func() { beforeRunClaim = nil })
	evs := e.start(t, conv, "second")
	require.Equal(t, "done", evs[len(evs)-1].Kind)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[1].Messages), "Alpha answer", "the second run continues from the first one's final turn")
}

func TestCancelRunKeepsStateOnCloseFailure(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`})
	_, err := e.s.db.Execute(t.Context(), "UPDATE conversations SET state = ? WHERE id = ?", []byte("not json"), conv.ID)
	require.NoError(t, err)
	require.Error(t, e.s.CancelRun(t.Context(), in.RunID))
	e.waitRun(t, in.RunID, "interrupted")
	assert.Equal(t, "pending", e.approval(t, in.Approvals[0].ID).State, "nothing committed: the user can retry")
}

// failClaims makes every resume claim fail with a database error.
func failClaims(t *testing.T, s *Service) {
	t.Helper()
	_, err := s.db.Execute(t.Context(), `CREATE TRIGGER fail_claim BEFORE UPDATE OF state ON runs WHEN NEW.state = 'resuming'
		BEGIN SELECT RAISE(ABORT, 'injected claim failure'); END`)
	require.NoError(t, err)
}

func TestResumeClaimFailureFailsRun(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`})
	failClaims(t, e.s)
	err := e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true)
	require.ErrorContains(t, err, "injected claim failure")
	assert.Equal(t, "done", e.approval(t, in.Approvals[0].ID).State)
	e.waitRun(t, in.RunID, "failed")
	evs := e.start(t, conv, "again") // the failed run blocks nothing and left no open calls
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
}

func TestRecoveryFailureFailsRun(t *testing.T) {
	e := newApprovalEnv(t)
	// A run left interrupted with nothing open, whose resume claim will fail at startup.
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"A"}`}}})
	evs := e.start(t, conv, "do it")
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind)
	beforeResumeClaim = func() bool { return true }
	t.Cleanup(func() { beforeResumeClaim = nil })
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	beforeResumeClaim = nil
	// A run left resuming whose state cannot be closed, with an approval still pending.
	other := e.newConv(t, "ask", 0)
	in2 := e.interrupt(t, other, testllm.Call{Name: "fake_draft", Args: `{"subject":"C"}`})
	_, err := e.s.db.Execute(t.Context(), "UPDATE runs SET state = 'resuming' WHERE id = ?", in2.RunID)
	require.NoError(t, err)
	_, err = e.s.db.Execute(t.Context(), "UPDATE conversations SET state = ? WHERE id = ?", []byte("not json"), other.ID)
	require.NoError(t, err)
	failClaims(t, e.s)

	e.restart(t) // New succeeds
	e.waitRun(t, in.RunID, "failed")
	e.waitRun(t, in2.RunID, "failed")
	got := e.approval(t, in2.Approvals[0].ID)
	assert.Equal(t, "rejected", got.State)
	assert.Equal(t, msgRecovery, got.Error)
	_, err = e.s.db.Execute(t.Context(), "DROP TRIGGER fail_claim")
	require.NoError(t, err)
	evs = e.start(t, conv, "again")
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	_, err = e.s.startRun(t.Context(), other, "again", nil)
	assert.NotErrorIs(t, err, ErrRunActive)
}

func TestSecondInterruptAfterResume(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"A"}`}}},
		testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"B"}`}}}, testllm.Turn{Text: []string{"Both."}})
	evs := e.start(t, conv, "do it")
	first := evs[len(evs)-1]
	require.Equal(t, "interrupted", first.Kind)
	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	require.NoError(t, e.s.ResolveApproval(t.Context(), first.Approvals[0].ID, true))
	second := waitTerminal(t, sub)
	require.Equal(t, "interrupted", second.Kind)
	require.Len(t, second.Approvals, 1)
	assert.Equal(t, 1, second.Approvals[0].Seq)
	assert.Equal(t, first.RunID, second.RunID)
	require.NoError(t, e.s.ResolveApproval(t.Context(), second.Approvals[0].ID, true))
	assert.Equal(t, "done", waitTerminal(t, sub).Kind)
	e.waitRun(t, first.RunID, "done")
	reqs := e.srv.Requests()
	require.Len(t, reqs, 3)
	calls, results := toolPairs(t, reqs[2])
	assert.Len(t, results, 2)
	assert.Equal(t, calls, results)
}

func TestConversations(t *testing.T) {
	e := newApprovalEnv(t)
	_, err := e.s.NewConversation(t.Context(), "assist", 0)
	require.Error(t, err)
	conv := e.newConv(t, "ask", 0)
	e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"A"}`})
	cs, err := e.s.ListConversations(t.Context(), "ask")
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, conv.ID, cs[0].ID)
	require.NoError(t, e.s.DeleteConversation(t.Context(), conv.ID))
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM approvals"))
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM runs"))
	_, err = e.s.GetConversation(t.Context(), conv.ID)
	require.Error(t, err)
}

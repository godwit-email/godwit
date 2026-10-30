package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Approvals and questions of Ask runs on pi (Labs, #233) work as on Genkit;
// these mirror the Genkit tests in approvals_test.go, question_test.go and
// session_test.go.

func newPiApprovalEnv(t *testing.T) *approvalEnv {
	t.Helper()
	e := newApprovalEnv(t)
	return e
}

// onPi reports whether pi-ai sent every model request so far but the first n.
func (e *approvalEnv) onPi(t *testing.T, n int) bool {
	t.Helper()
	reqs := e.srv.Requests()
	require.Greater(t, len(reqs), n)
	for _, r := range reqs[n:] {
		if r.Header.Get("User-Agent") != "Godwit" {
			return false
		}
	}
	return true
}

func TestPiMixedRoundWaitsForTheApproval(t *testing.T) {
	e := newPiApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "search_mail", Args: `{"query":"harbour"}`}, {Name: "fake_draft", Args: `{"subject":"Hi"}`}}}, finished)
	ch, err := e.s.startRun(t.Context(), conv, "do it", e.s.registered("search_mail", "fake_draft"))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)
	require.Len(t, in.Approvals, 1)
	assert.Empty(t, witResults(evs, "search_mail"), "nothing runs before the approval")
	assert.Len(t, e.srv.Requests(), 1, "nor is the model asked again")
	assert.True(t, e.onPi(t, 0))

	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	var ran []WitEvent
	for ev := range sub {
		ran = append(ran, ev)
		if ev.Kind == "done" {
			break
		}
	}
	e.waitRun(t, in.RunID, "done")
	require.Len(t, witResults(ran, "search_mail"), 1, "the held-back read runs on resume")
	assert.Equal(t, int32(1), e.applied.Load())
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.True(t, e.onPi(t, 0))
	calls, results := toolPairs(t, reqs[1])
	assert.Len(t, results, 2)
	assert.Equal(t, calls, results)
	msgs := savedMessages(t, e.s, conv.ID)
	assert.Equal(t, "Finished.", msgs[len(msgs)-1].Text)
}

// A pending approval outlives the app; the run resumes on pi after it.
func TestPiApprovalSurvivesRestart(t *testing.T) {
	e := newPiApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	e.restart(t)
	as, err := e.s.ListApprovals(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, as, 1)
	assert.Equal(t, "pending", as[0].State)

	require.NoError(t, e.s.ResolveApproval(t.Context(), as[0].ID, true))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, int32(1), e.applied.Load())
	require.Len(t, e.srv.Requests(), 2)
	assert.True(t, e.onPi(t, 0))
}

// An approved write cut off before it took effect never runs again.
func TestPiCrashAfterApproval(t *testing.T) {
	e := newPiApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	e.s.ledger.afterApprovalClaim = func() bool { return true }
	require.Error(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	e.s.ledger.afterApprovalClaim = nil

	e.restart(t)
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, msgQuit, e.approval(t, in.Approvals[0].ID).Error)
	assert.Zero(t, e.applied.Load())
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.True(t, e.onPi(t, 0))
	assert.Contains(t, string(reqs[1].Messages), msgQuit)
}

// A write that took effect before the app quit is not run again when the
// run resumes after the restart.
func TestPiCrashBeforeResume(t *testing.T) {
	e := newPiApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	e.s.ledger.beforeResumeClaim = func() bool { return true }
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	e.s.ledger.beforeResumeClaim = nil
	e.waitRun(t, in.RunID, "interrupted")

	e.restart(t)
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, int32(1), e.applied.Load())
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM drafts"))
	require.Len(t, e.srv.Requests(), 2)
	assert.True(t, e.onPi(t, 0))
}

// An approval is bound to the draft's revision: one the user changed since
// is refused, and the model is told why.
func TestPiStaleTargetRejected(t *testing.T) {
	e := newPiApprovalEnv(t)
	d, err := e.s.SaveDraft(t.Context(), Draft{AccountID: 1, Mode: "new", Subject: "Draft"})
	require.NoError(t, err)
	e.s.registerExecutorForTest("fake_rename", executor{
		Prepare: func(ctx context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
			var in subjectArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return prepared{}, err
			}
			d, err := e.s.GetDraft(ctx, in.DraftID)
			if err != nil {
				return prepared{}, err
			}
			return prepared{Args: args, Summary: "Rename", TargetKind: "draft", TargetID: d.ID, TargetVersion: d.Revision}, nil
		},
		Apply: func(context.Context, Approval) ([]sqlite.Statement, any, any, error) { return nil, "renamed", nil, nil },
	})
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_rename", Args: fmt.Sprintf(`{"draft_id":%d,"Subject":"New"}`, d.ID)}}}, finished)
	ch, err := e.s.startRun(t.Context(), conv, "rename it", e.s.registered("fake_rename"))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)
	d.Subject = "From the user"
	_, err = e.s.SaveDraft(t.Context(), d)
	require.NoError(t, err)

	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true), ErrStaleDraft)
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, "stale", e.approval(t, in.Approvals[0].ID).State)
	assert.True(t, e.onPi(t, 0))
	assert.Contains(t, string(e.srv.Requests()[1].Messages), msgStale)
}

// 全部允许 approves the writes and leaves the question to the user; the run
// waits for its answer.
func TestPiAllowDoesNotAnswerAQuestion(t *testing.T) {
	e := newPiApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"Hi"}`}, {Name: askQuestionTool, Args: toneQuestion}}}, finished)
	ch, err := e.s.startRun(t.Context(), conv, "do it", e.s.registered("fake_draft", askQuestionTool))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	in := evs[len(evs)-1]
	require.Len(t, in.Approvals, 2)
	q := in.Approvals[1].ID

	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), q, true), ErrNeedsAnswer)
	require.NoError(t, e.s.ResolveAll(t.Context(), in.RunID, true))
	assert.Equal(t, "done", e.approval(t, in.Approvals[0].ID).State)
	assert.Equal(t, "pending", e.approval(t, q).State)
	assert.Len(t, e.srv.Requests(), 1, "the run waits for the answer")

	require.NoError(t, e.s.AnswerQuestion(t.Context(), q, 0, [][]string{{"Formal"}}))
	e.waitRun(t, in.RunID, "done")
	assert.True(t, e.onPi(t, 0))
	assert.Contains(t, string(e.srv.Requests()[1].Messages), "The user answered your questions")
}

// 本次对话都允许 approves the tool's other pending call of the round, then
// later calls of it in the conversation, on pi as on Genkit.
func TestPiSessionAllow(t *testing.T) {
	e := newExtEnv(t)
	conv := e.askConv(t)
	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{draftCall("A"), draftCall("B")}}, testllm.Turn{Text: []string{"Drafted."}})
	in := e.skillEnv.ask(t, conv.ID, "Draft two")
	last := in[len(in)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 2)
	require.NoError(t, e.s.ResolveApprovalForConversation(t.Context(), last.Approvals[0].ID))
	for waitTerminal(t, sub).Kind != "done" {
	}
	assert.Equal(t, 2, e.count(t, "SELECT COUNT(*) FROM drafts"))

	e.srv.Script(testllm.Turn{Calls: []testllm.Call{draftCall("C")}}, testllm.Turn{Text: []string{"Again."}})
	e.skillEnv.ask(t, conv.ID, "One more")
	for waitTerminal(t, sub).Kind != "done" {
	}
	assert.Equal(t, 3, e.count(t, "SELECT COUNT(*) FROM drafts"))
	assert.Equal(t, 2, e.audited(t, "create_draft", decisionSession))
	assert.True(t, e.onPi(t, 0))
}

// Cancelling a held round closes every call of it, the held-back ones too,
// so the conversation goes on.
func TestPiCancelClosesHeldRound(t *testing.T) {
	e := newPiApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "search_mail", Args: `{"query":"harbour"}`}, {Name: "fake_draft", Args: `{"subject":"Hi"}`}}})
	ch, err := e.s.startRun(t.Context(), conv, "do it", e.s.registered("search_mail", "fake_draft"))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)
	require.NoError(t, e.s.CancelRun(t.Context(), in.RunID))
	e.waitRun(t, in.RunID, "cancelled")

	evs = e.start(t, conv, "something else")
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	calls, results := toolPairs(t, reqs[1])
	assert.Len(t, calls, 2)
	assert.Equal(t, calls, results, "every tool request in the history has its result")
}

// A trusted MCP tool that asks a form as it runs interrupts the run on pi
// too; the answer resumes it there.
func TestPiMCPQuestionOfTrustedCall(t *testing.T) {
	e := newPiApprovalEnv(t)
	f := newFakeMCP(t)
	addBooking(f)
	srv := fakeServer(t, e.s)
	require.NoError(t, e.s.SetMCPToolTrusted(t.Context(), srv.ID, "book", true))
	srv = mcpServer(t, e.s, srv.ID)

	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(witName(t, srv, "book"), `{}`), finished)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)
	require.Len(t, in.Approvals, 1)
	assert.NotNil(t, in.Approvals[0].Form)

	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, 1, [][]string{{"Small"}, {"false"}, {}}))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, "done", e.approval(t, in.Approvals[0].ID).State)
	assert.Equal(t, int32(2), f.calls.Load())
	assert.True(t, e.onPi(t, 0))
}

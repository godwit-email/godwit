package core

import (
	"encoding/json"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const toneQuestion = `{"questions":[{"question":"Which tone?","header":"Tone","description":"Bob is a new client.","multiSelect":false,
	"options":[{"label":"Formal","description":"Polite and distant"},{"label":"Casual","description":"Like a friend"}]}]}`

// askQuestion runs Ask in a new conversation whose model asks args, and
// returns the interrupted event.
func (e *approvalEnv) askQuestion(t *testing.T, args string) WitEvent {
	t.Helper()
	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(askQuestionTool, args), finished)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	return last
}

func TestAnsweredQuestionGoesBackToTheModel(t *testing.T) {
	e := newApprovalEnv(t)
	in := e.askQuestion(t, toneQuestion)
	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, [][]string{{"Formal"}}))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, "The user answered your questions:\n\"Which tone?\"=\"Formal\"", e.toolMessage(t))
	a := e.approval(t, in.Approvals[0].ID)
	assert.Equal(t, "done", a.State)
	assert.Contains(t, string(a.Args), `"description":"Bob is a new client."`, "the card shows it")
}

func TestASkippedQuestionGoesBackAsSkipped(t *testing.T) {
	e := newApprovalEnv(t)
	in := e.askQuestion(t, `{"questions":[`+q("Which tone?")+`,`+q("Which day?")+`]}`)
	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, [][]string{{}, {"B"}}))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, "The user answered your questions:\n\"Which tone?\"=\"(skipped)\"\n\"Which day?\"=\"B\"", e.toolMessage(t))
}

func TestSkippingEveryQuestionSkipsTheCall(t *testing.T) {
	e := newApprovalEnv(t)
	in := e.askQuestion(t, toneQuestion)
	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, [][]string{{}}))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, "rejected", e.approval(t, in.Approvals[0].ID).State)
	assert.Equal(t, msgUnanswered, e.toolMessage(t))
}

func TestSkippedQuestionLetsTheModelAssume(t *testing.T) {
	e := newApprovalEnv(t)
	in := e.askQuestion(t, toneQuestion)
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, false))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, msgUnanswered, e.toolMessage(t))
}

func TestAllowDoesNotAnswerAQuestion(t *testing.T) {
	e := newApprovalEnv(t)
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

	require.NoError(t, e.s.AnswerQuestion(t.Context(), q, [][]string{{"My own words"}}))
	e.waitRun(t, in.RunID, "done")
	assert.Len(t, e.srv.Requests(), 2)
}

func TestQuestionSurvivesRestart(t *testing.T) {
	e := newApprovalEnv(t)
	in := e.askQuestion(t, toneQuestion)
	e.restart(t)
	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, [][]string{{"Casual"}}))
	e.waitRun(t, in.RunID, "done")
	assert.Contains(t, e.toolMessage(t), `"Which tone?"="Casual"`)
}

func TestAnswersMustFitTheQuestions(t *testing.T) {
	e := newApprovalEnv(t)
	in := e.askQuestion(t, toneQuestion)
	id := in.Approvals[0].ID
	for _, bad := range [][][]string{nil, {{" "}}, {{"Formal", "Casual"}}, {{"Formal"}, {"Extra"}}} {
		assert.Error(t, e.s.AnswerQuestion(t.Context(), id, bad), "%q", bad)
	}
	assert.Equal(t, "pending", e.approval(t, id).State)
}

func TestMalformedQuestionGoesBackAsAnError(t *testing.T) {
	for name, args := range map[string]string{
		"one option":    `{"questions":[{"question":"Which?","header":"H","multiSelect":false,"options":[{"label":"Only","description":""}]}]}`,
		"same label":    `{"questions":[{"question":"Which?","header":"H","multiSelect":false,"options":[{"label":"A","description":""},{"label":"A","description":""}]}]}`,
		"same question": `{"questions":[` + q("Which?") + `,` + q("Which?") + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newApprovalEnv(t)
			evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(askQuestionTool, args), finished)
			assert.Equal(t, "done", evs[len(evs)-1].Kind)
			assert.NotEmpty(t, e.toolMessage(t))
			assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM approvals"))
		})
	}
}

func q(text string) string {
	return `{"question":"` + text + `","header":"H","multiSelect":false,"options":[{"label":"A","description":""},{"label":"B","description":""}]}`
}

// toolMessage is what the model was told for the only tool call of its last request.
func (e *approvalEnv) toolMessage(t *testing.T) string {
	t.Helper()
	reqs := e.srv.Requests()
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	require.NoError(t, json.Unmarshal(reqs[len(reqs)-1].Messages, &msgs))
	for _, m := range msgs {
		if m.Role == "tool" {
			var s string
			require.NoError(t, json.Unmarshal(m.Content, &s))
			var r struct{ Error string }
			if json.Unmarshal([]byte(s), &r) == nil && r.Error != "" {
				return r.Error
			}
			_ = json.Unmarshal([]byte(s), &s) // a result is a JSON string
			return s
		}
	}
	t.Fatal("no tool message")
	return ""
}

package core

import (
	"encoding/json"

	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The compose window's Assist on pi (Labs, #235) works as on Genkit; these
// mirror the Genkit tests in assist_test.go.

func newPiAssistEnv(t *testing.T) skillEnv {
	t.Helper()
	e := newAssistEnv(t)
	e.setPi(t, true)
	return e
}

// allPi asserts that pi-ai sent every model request so far.
func (e skillEnv) allPi(t *testing.T) {
	t.Helper()
	reqs := e.srv.Requests()
	require.NotEmpty(t, reqs)
	for i, r := range reqs {
		assert.Equal(t, "Godwit", r.Header.Get("User-Agent"), "request %d went on pi", i)
	}
}

func TestPiAssistProposalAcceptReject(t *testing.T) {
	e := newPiAssistEnv(t)
	d := e.replyDraft(t, "<p>I will come on Friday.</p>")
	_, q, sig := parts(t, d.HTML)
	a := e.propose(t, d.ID, "<p>I will gladly come on Friday.</p>").Approvals[0]
	assert.Equal(t, "pending", a.State)
	assert.Equal(t, "partial", a.Summary)
	assert.Equal(t, d.HTML, e.getDraft(t, d.ID).HTML, "nothing is written before approval")
	var sys string
	require.NoError(t, json.Unmarshal(e.srv.Requests()[0].System, &sys))
	assert.Contains(t, sys, "Meet on Friday at the harbour.", "the original email")
	assert.Contains(t, sys, `<untrusted_email source="message:`, "as data")

	e.setPi(t, false) // the run resumes on the engine it started on
	e.accept(t, a)
	got := e.getDraft(t, d.ID)
	ed, q2, sig2 := parts(t, got.HTML)
	assert.Equal(t, "<p>I will gladly come on Friday.</p>", ed)
	assert.Equal(t, q, q2)
	assert.Equal(t, sig, sig2)
	e.allPi(t)

	e.setPi(t, true)
	b := e.propose(t, d.ID, "<p>No.</p>").Approvals[0]
	assert.Equal(t, "rewrite", b.Summary)
	require.NoError(t, e.s.ResolveApproval(t.Context(), b.ID, false))
	e.waitRunOf(t, b.RunID, "done")
	assert.Equal(t, got.HTML, e.getDraft(t, d.ID).HTML, "a rejected proposal writes nothing")
	ap, err := e.s.getApproval(t.Context(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, "rejected", ap.State)
	e.allPi(t)
}

func TestPiAssistStaleProposal(t *testing.T) {
	e := newPiAssistEnv(t)
	d := e.replyDraft(t, "<p>Draft one.</p>")
	a := e.propose(t, d.ID, "<p>Draft two.</p>").Approvals[0]
	cur := e.getDraft(t, d.ID)
	_, q, sig := parts(t, cur.HTML)
	cur.HTML = mail.JoinDraft("<p>My own edit.</p>", q, sig)
	_, err := e.s.SaveDraft(t.Context(), cur)
	require.NoError(t, err)

	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrStaleDraft)
	e.waitRunOf(t, a.RunID, "done")
	ap, err := e.s.getApproval(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "stale", ap.State)
	ed, _, _ := parts(t, e.getDraft(t, d.ID).HTML)
	assert.Equal(t, "<p>My own edit.</p>", ed)
	e.allPi(t)
}

func TestPiUndoLastAssist(t *testing.T) {
	e := newPiAssistEnv(t)
	d := e.replyDraft(t, "<p>Version one.</p>")
	_, q, sig := parts(t, d.HTML)
	e.accept(t, e.propose(t, d.ID, "<p>Version two.</p>").Approvals[0])
	require.NoError(t, e.s.UndoLastAssist(t.Context(), d.ID))
	assert.Equal(t, mail.JoinDraft("<p>Version one.</p>", q, sig), e.getDraft(t, d.ID).HTML)
	assert.ErrorIs(t, e.s.UndoLastAssist(t.Context(), d.ID), ErrInvalidState, "undone already")
	e.allPi(t)
}

func TestPiAssistSkill(t *testing.T) {
	e := newPiAssistEnv(t)
	d := e.replyDraft(t, "<p>我周五到。</p>")
	e.srv.Script(testllm.Turn{Text: []string{"```html\n<p>I'll be there ", "on Friday.</p>\n```"}})
	ch, err := e.s.AssistSkill(t.Context(), d.ID, "translate-draft", SkillOptions{})
	require.NoError(t, err)
	evs := drainWit(t, ch)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	a := last.Approvals[0]
	assert.Equal(t, "translate", a.Summary)
	e.allPi(t)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 1)
	p, sys := promptOf(t, reqs[0]), string(reqs[0].System)
	assert.Contains(t, p, "我周五到。")
	assert.Contains(t, sys, "Translate the draft")
	for _, s := range []string{p, sys} {
		assert.NotContains(t, s, "translate-draft", "the model never sees the command")
		assert.NotContains(t, s, "Meet on Friday at the harbour.", "nor other mail")
	}

	e.accept(t, a)
	ed, _, _ := parts(t, e.getDraft(t, d.ID).HTML)
	assert.Equal(t, "<p>I&#39;ll be there on Friday.</p>", ed)
	msgs := savedMessages(t, e.s, a.ConversationID)
	require.Len(t, msgs, 1, "the skill run's history is its command")
	require.NotNil(t, msgs[0].Command)
	assert.Equal(t, "translate-draft", msgs[0].Command.Skill)
}

// A question in the compose window interrupts the run on pi; the answer
// resumes it there. The quoted mail goes as data.
func TestPiAssistQuestionAndQuote(t *testing.T) {
	e := newPiAssistEnv(t)
	other := addMessage(t, e.s, "Tickets", "Ann <ann@x>", "train at nine", "", 1_700_000_300)
	d := e.replyDraft(t, "<p>Hi.</p>")
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: askQuestionTool, Args: toneQuestion}}}, testllm.Turn{Text: []string{"Noted."}})
	ch, err := e.s.AssistWith(t.Context(), d.ID, "Use this", []int64{other})
	require.NoError(t, err)
	evs := drainWit(t, ch)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)
	require.Len(t, in.Approvals, 1)
	p := promptOf(t, e.srv.Requests()[0])
	assert.Contains(t, p, "train at nine")
	assert.Contains(t, p, "untrusted_email", "quoted mail is data")

	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, 0, [][]string{{"Formal"}}))
	e.waitRunOf(t, in.RunID, "done")
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[1].Messages), "The user answered your questions")
	e.allPi(t)
}

package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bookingForm is the form the fake "book" tool asks before it books.
var bookingForm = map[string]any{"type": "object", "required": []any{"room"}, "properties": map[string]any{
	"room":      map[string]any{"type": "string", "title": "Room", "oneOf": []any{map[string]any{"const": "a", "title": "Small"}, map[string]any{"const": "b", "title": "Large"}}},
	"nights":    map[string]any{"type": "integer", "minimum": 1, "maximum": 7},
	"breakfast": map[string]any{"type": "boolean"},
}}

// addBooking adds the "book" tool to f: called without answers it asks
// bookingForm (input_required); with them it books and records the answer.
func addBooking(f *fakeMCP) *atomic.Value {
	var got atomic.Value
	f.srv.AddTool(&mcp.Tool{Name: "book", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.calls.Add(1)
			if res, ok := r.Params.InputResponses["form"].(*mcp.ElicitResult); ok && r.Params.RequestState == "s1" {
				got.Store(res)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "booked"}}}, nil
			}
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"form": &mcp.ElicitParams{Message: "Which room?", RequestedSchema: bookingForm}},
				RequestState: "s1"}, nil
		})
	return &got
}

func TestMCPQuestionOfApprovedCall(t *testing.T) {
	e := newApprovalEnv(t)
	f := newFakeMCP(t)
	got := addBooking(f)
	srv := fakeServer(t, e.s)

	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(witName(t, srv, "book"), `{}`), finished)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)
	id := in.Approvals[0].ID
	sub, unsub := e.s.SubscribeConversation(in.ConversationID)
	defer unsub()
	require.NoError(t, e.s.ResolveApproval(t.Context(), id, true))

	// the call asked: the same approval waits again, now for the answers, and followers are told
	ev := <-sub
	assert.Equal(t, "interrupted", ev.Kind)
	assert.True(t, ev.Ends)
	require.Len(t, ev.Approvals, 1)
	assert.Equal(t, "pending", ev.Approvals[0].State)
	assert.NotNil(t, ev.Approvals[0].Form)
	a := e.approval(t, id)
	require.Equal(t, "pending", a.State)
	var form formView
	require.NoError(t, json.Unmarshal(a.Form, &form))
	assert.Equal(t, "fake", form.Server)
	assert.Equal(t, "Which room?", form.Message)
	require.Len(t, form.Questions, 3)
	assert.Equal(t, "Room", form.Questions[0].Question, "required first")
	assert.Equal(t, "choice", form.Questions[0].Kind)
	assert.Equal(t, []questionOption{{Label: "Small"}, {Label: "Large"}}, form.Questions[0].Options)
	assert.Equal(t, []string{"breakfast", "nights"}, []string{form.Questions[1].Question, form.Questions[2].Question})
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE decision = 'input_required'"))

	// nothing answers it but the user's answers
	assert.ErrorIs(t, e.s.ResolveApproval(t.Context(), id, true), ErrNeedsAnswer)
	require.NoError(t, e.s.ResolveAll(t.Context(), in.RunID, true))
	assert.Equal(t, "pending", e.approval(t, id).State)
	assert.Error(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{}, {"true"}, {"2"}}), "the room is required")
	assert.Error(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{"Large"}, {}, {"9"}}), "at most 7 nights")
	assert.Error(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{"Huge"}, {}, {}}), "only its choices")
	assert.ErrorContains(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{"Large"}, {}, {"9007199254740993"}}), "needs a integer", "not exact as a float64")

	require.NoError(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{"Large"}, {}, {"2"}}))
	e.waitRun(t, in.RunID, "done")
	res := got.Load().(*mcp.ElicitResult)
	assert.Equal(t, "accept", res.Action)
	assert.Equal(t, map[string]any{"room": "b", "nights": float64(2)}, res.Content)
	a = e.approval(t, id)
	assert.Equal(t, "done", a.State)
	require.NoError(t, json.Unmarshal(a.Form, &form))
	assert.Equal(t, [][]string{{"Large"}, {}, {"2"}}, form.Answers)
	assert.Equal(t, int32(2), f.calls.Load())
}

func TestMCPQuestionOfTrustedCall(t *testing.T) {
	e := newApprovalEnv(t)
	f := newFakeMCP(t)
	addBooking(f)
	srv := fakeServer(t, e.s)
	require.NoError(t, e.s.SetMCPToolTrusted(t.Context(), srv.ID, "book", true))
	srv = mcpServer(t, e.s, srv.ID)

	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(witName(t, srv, "book"), `{}`), finished)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "an in-run call that asks becomes an approval: %+v", in.Err)
	require.Len(t, in.Approvals, 1)
	assert.NotNil(t, in.Approvals[0].Form)
	assert.Equal(t, []string{"mcp:fake"}, in.Approvals[0].Destinations)

	require.NoError(t, e.s.AnswerQuestion(t.Context(), in.Approvals[0].ID, 1, [][]string{{"Small"}, {"false"}, {}}))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, "done", e.approval(t, in.Approvals[0].ID).State)
	assert.Equal(t, int32(2), f.calls.Load())
}

func TestMCPQuestionSkipped(t *testing.T) {
	e := newApprovalEnv(t)
	f := newFakeMCP(t)
	addBooking(f)
	srv := fakeServer(t, e.s)

	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(witName(t, srv, "book"), `{}`), finished)
	in := evs[len(evs)-1]
	id := in.Approvals[0].ID
	require.NoError(t, e.s.ResolveApproval(t.Context(), id, true))
	require.NoError(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{}, {}, {}}))
	e.waitRun(t, in.RunID, "done")
	a := e.approval(t, id)
	assert.Equal(t, "rejected", a.State)
	assert.Equal(t, msgUnanswered, a.Error)
	assert.Equal(t, int32(1), f.calls.Load(), "a skipped form is not sent")
}

func TestMCPInputOf(t *testing.T) {
	form := &mcp.ElicitParams{Message: "?", RequestedSchema: bookingForm}
	asks := func(reqs mcp.InputRequestMap) *mcp.CallToolResult {
		return &mcp.CallToolResult{InputRequests: reqs, RequestState: "s"}
	}
	in, err := mcpInputOf(asks(mcp.InputRequestMap{"f": form}), nil)
	require.NoError(t, err)
	assert.Equal(t, mcpInput{ID: "f", Form: form, State: "s", Round: 1}, in)

	_, err = mcpInputOf(asks(mcp.InputRequestMap{"f": form}), &mcpInput{Round: maxMCPRounds})
	assert.ErrorContains(t, err, "in a row")
	_, err = mcpInputOf(asks(mcp.InputRequestMap{}), nil)
	assert.ErrorContains(t, err, "busy")
	_, err = mcpInputOf(asks(mcp.InputRequestMap{"f": form, "g": form}), nil)
	assert.ErrorContains(t, err, "several")
	_, err = mcpInputOf(asks(mcp.InputRequestMap{"u": &mcp.ElicitParams{Mode: "url", URL: "https://x.test"}}), nil)
	assert.ErrorContains(t, err, "only forms")
	_, err = mcpInputOf(asks(mcp.InputRequestMap{"r": &mcp.ListRootsParams{}}), nil)
	assert.ErrorContains(t, err, "only forms")
	nested := &mcp.ElicitParams{RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"o": map[string]any{"type": "object"}}}}
	_, err = mcpInputOf(asks(mcp.InputRequestMap{"n": nested}), nil)
	assert.ErrorContains(t, err, "not a field")
}

func TestMCPQuestionStaleAnswer(t *testing.T) {
	e := newApprovalEnv(t)
	f := newFakeMCP(t)
	addBooking(f)
	srv := fakeServer(t, e.s)
	require.NoError(t, e.s.SetMCPToolTrusted(t.Context(), srv.ID, "book", true))
	srv = mcpServer(t, e.s, srv.ID)
	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(witName(t, srv, "book"), `{}`), finished)
	id := evs[len(evs)-1].Approvals[0].ID

	read, err := e.s.approvalRow(t.Context(), id)
	require.NoError(t, err)
	// another round (or another answer) came in since the form was read
	_, err = e.s.db.Execute(t.Context(), "UPDATE approvals SET input = json_set(input, '$.round', 2) WHERE id = ?", id)
	require.NoError(t, err)
	assert.ErrorIs(t, e.s.answerForm(t.Context(), read, 1, [][]string{{"Small"}, {}, {}}), ErrInvalidState)
	assert.Equal(t, "pending", e.approval(t, id).State)
	assert.Equal(t, int32(1), f.calls.Load(), "the stale answer is not sent")
}

func TestMCPAnswerToAnotherRoundRefused(t *testing.T) {
	e := newApprovalEnv(t)
	f := newFakeMCP(t)
	addBooking(f)
	srv := fakeServer(t, e.s)
	require.NoError(t, e.s.SetMCPToolTrusted(t.Context(), srv.ID, "book", true))
	srv = mcpServer(t, e.s, srv.ID)
	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(witName(t, srv, "book"), `{}`), finished)
	id := evs[len(evs)-1].Approvals[0].ID
	// the call asked its second form since the card showed the first
	_, err := e.s.db.Execute(t.Context(), "UPDATE approvals SET input = json_set(input, '$.round', 2) WHERE id = ?", id)
	require.NoError(t, err)
	assert.ErrorIs(t, e.s.AnswerQuestion(t.Context(), id, 1, [][]string{{"Small"}, {}, {}}), ErrInvalidState)
	assert.Equal(t, "pending", e.approval(t, id).State)
	assert.Equal(t, int32(1), f.calls.Load(), "the first round's answer is not sent to the second")
}

func TestMCPQuestionNotInCompose(t *testing.T) {
	e := newApprovalEnv(t)
	f := newFakeMCP(t)
	addBooking(f)
	srv := fakeServer(t, e.s)
	require.NoError(t, e.s.SetMCPToolTrusted(t.Context(), srv.ID, "book", true))
	srv = mcpServer(t, e.s, srv.ID)
	d, err := e.s.NewDraft(t.Context(), 1, "new", 0)
	require.NoError(t, err)
	e.srv.Script(callArgs(witName(t, srv, "book"), `{}`), finished)
	ch, err := e.s.Assist(t.Context(), d.ID, "help")
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.Equal(t, "done", evs[len(evs)-1].Kind, "the call fails; the run goes on: %+v", evs[len(evs)-1].Err)
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM approvals"))
	assert.Contains(t, lastSent(t, e), "only Wit's own window can show")
}

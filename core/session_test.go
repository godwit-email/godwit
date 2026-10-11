package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func draftCall(subject string) testllm.Call {
	return testllm.Call{Name: "create_draft", Args: `{"to":["carol@y"],"subject":"` + subject + `","html":"<p>Hi</p>"}`}
}

func (e *extEnv) audited(t *testing.T, tool, decision string) int {
	t.Helper()
	return e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = ? AND decision = ?", tool, decision)
}

// TestSessionAllow (本次对话都允许): the grant approves the call and that
// tool's other pending calls in the run, then later calls of it in the same
// conversation, audited auto:session; never another conversation, another
// tool, an external tool, or after the conversation is deleted.
func TestSessionAllow(t *testing.T) {
	e := newExtEnv(t)
	conv := e.askConv(t)
	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{draftCall("A"), draftCall("B")}}, testllm.Turn{Text: []string{"Drafted."}})
	in := e.skillEnv.ask(t, conv.ID, "Draft two")
	last := in[len(in)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 2)
	assert.True(t, last.Approvals[0].SessionAllow, "create_draft offers the session grant")

	require.NoError(t, e.s.ResolveApprovalForConversation(t.Context(), last.Approvals[0].ID))
	for waitTerminal(t, sub).Kind != "done" {
	}
	assert.Equal(t, 2, e.count(t, "SELECT COUNT(*) FROM drafts"), "the tool's other pending call is approved too")
	assert.Equal(t, 1, e.audited(t, "create_draft", "executed"), "the one clicked")
	assert.Equal(t, 1, e.audited(t, "create_draft", decisionSession))

	// A later call in the same conversation is approved at once.
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{draftCall("C")}}, testllm.Turn{Text: []string{"Again."}})
	e.skillEnv.ask(t, conv.ID, "One more")
	for waitTerminal(t, sub).Kind != "done" {
	}
	assert.Equal(t, 3, e.count(t, "SELECT COUNT(*) FROM drafts"))
	assert.Equal(t, 2, e.audited(t, "create_draft", decisionSession))

	// Another conversation asks again.
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{draftCall("D")}})
	other := e.skillEnv.ask(t, e.askConv(t).ID, "Draft")
	require.Equal(t, "pending", other[len(other)-1].Approvals[0].State)

	// Another tool, external ones included, asks again and offers no grant.
	ext := e.interruptExt(t, conv, callTurn("taint"), testllm.Turn{Calls: []testllm.Call{
		{Name: "ext_write", Args: `{"x":1}`}, {Name: "ext_read", Args: `{"x":1}`}, {Name: "fake_draft", Args: `{"subject":"s"}`}}})
	require.Len(t, ext.Approvals, 3)
	for _, a := range ext.Approvals {
		assert.Equal(t, "pending", a.State, a.Tool)
		assert.False(t, a.SessionAllow, a.Tool)
		require.ErrorIs(t, e.s.ResolveApprovalForConversation(t.Context(), a.ID), ErrNotGrantable, a.Tool)
	}
	assert.Zero(t, e.runs.Load(), "no external call ran")
	require.NoError(t, e.s.CancelRun(t.Context(), ext.Approvals[0].RunID))

	// Deleting the conversation drops its grant.
	require.NoError(t, e.s.DeleteConversation(t.Context(), conv.ID))
	assert.False(t, e.s.hasGrants(conv.ID))
}

// TestSessionGrantBinding: a grant covers the tool as bound when granted; a
// call bound otherwise is not approved by it.
func TestSessionGrantBinding(t *testing.T) {
	e := newExtEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{draftCall("A")}})
	in := e.skillEnv.ask(t, conv.ID, "Draft")
	a := in[len(in)-1].Approvals[0]
	e.s.sessionGrants.Store(grantKey{conv: conv.ID, tool: "create_draft", binding: "old"}, true)
	as, applied := e.s.applyInitial(t.Context(), in[len(in)-1].Approvals)
	assert.False(t, applied)
	assert.Equal(t, "pending", as[0].State)
	e.s.sessionGrants.Store(grantKey{conv: conv.ID, tool: "create_draft"}, true)
	_, applied = e.s.applyInitial(t.Context(), []Approval{a})
	assert.True(t, applied)
	assert.Equal(t, 1, e.audited(t, "create_draft", decisionSession))
}

// TestNoSendGrant: no tool sends, and only create_draft can be granted.
func TestNoSendGrant(t *testing.T) {
	e := newExtEnv(t)
	e.s.toolsMu.Lock()
	defer e.s.toolsMu.Unlock()
	for name, d := range e.s.tools {
		assert.NotContains(t, name, "send")
		a := approvalRow{Approval: Approval{Tool: name}, source: d.Source}
		if d.Source == "" {
			a.source = "wit"
		}
		assert.Equal(t, name == "create_draft", sessionGrantable(a), name)
	}
}

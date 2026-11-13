package webui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/require"
)

// Over the ui socket an approval without an MCP form reaches the page with
// no form, as in process: a "null" one would make every card a question.
func TestApprovalFormOverSocket(t *testing.T) {
	f := newWitPageFixture(t)
	f.llm.Script(coretest.Turn{Text: []string{"我起草了一封回复。"}, Calls: []coretest.Call{{Name: "create_draft", Args: witDraftCall}}},
		coretest.Turn{Text: []string{"草稿已创建。"}})
	conv, ev := f.interrupted(t, "回复 Lena")
	require.Len(t, ev.Approvals, 1)
	require.Empty(t, ev.Approvals[0].Form, "the live event's approval")
	as, err := uiService(t, f.e).ListApprovals(t.Context(), conv)
	require.NoError(t, err)
	require.Len(t, as, 1)
	require.Nil(t, as[0].Form, "ListApprovals over the socket")
}

type followStub struct {
	Service
	raw  chan core.WitEvent
	conv core.Conversation
}

func (s *followStub) SubscribeConversation(int64) (<-chan core.WitEvent, func()) {
	return s.raw, func() {}
}

func (s *followStub) GetConversation(context.Context, int64) (core.Conversation, error) {
	return s.conv, nil
}

// A conversation read while its run waits on approval 3; the MCP server then
// asks a form on it, and core publishes "interrupted" again for the same
// approval (askAgain): the follower gets it, the form being news.
func TestFollowReaskedForm(t *testing.T) {
	raw := make(chan core.WitEvent, 4)
	a := core.Approval{ID: 3, RunID: 7, ConversationID: 1, State: "pending"}
	conv := core.Conversation{ID: 1, Kind: "ask", ActiveRunID: 7, Awaiting: true,
		Turns: []core.Turn{{ID: "1", Runs: []int64{7}, Calls: []core.TurnCall{{Approval: &a}}}}}
	_, ch, stop, err := followConversation(t.Context(), &followStub{raw: raw, conv: conv}, 1)
	require.NoError(t, err)
	defer stop()
	same, reask := a, a
	reask.Form = json.RawMessage(`{"round":1}`)
	raw <- core.WitEvent{ConversationID: 1, RunID: 7, Ends: true, Event: ai.Event{Kind: "interrupted"}, Approvals: []core.Approval{same}}
	raw <- core.WitEvent{ConversationID: 1, RunID: 7, Ends: true, Event: ai.Event{Kind: "interrupted"}, Approvals: []core.Approval{reask}}
	select {
	case ev := <-ch:
		require.Equal(t, "interrupted", ev.Kind)
		require.JSONEq(t, `{"round":1}`, string(ev.Approvals[0].Form), "the interrupt the snapshot shows goes; the re-asked form comes")
	case <-time.After(3 * time.Second):
		t.Fatal("the re-asked form never came")
	}
}

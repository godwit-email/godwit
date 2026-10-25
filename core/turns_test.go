package core

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// savedMessages is conversation id's history as saved, for display.
func savedMessages(t *testing.T, s *Service, id int64) []ai.Message {
	t.Helper()
	h, err := s.history(t.Context(), id)
	require.NoError(t, err)
	return h.Messages()
}

// span is the part of a conversation's history a run added: seqs From up to To.
type span struct{ ID, From, To int64 }

// spans are conversation id's runs that added any of its history, in run order.
func spans(t *testing.T, s *Service, id int64) []span {
	t.Helper()
	rs, err := s.conversationRuns(t.Context(), id)
	require.NoError(t, err)
	out := []span{}
	for _, r := range rs {
		out = append(out, span{r.id, r.from, r.to})
	}
	return out
}

// cites are conversation id's citations, numbered, in the order they came.
func cites(t *testing.T, s *Service, id int64) []Citation {
	t.Helper()
	cs, err := s.citationsOf(t.Context(), id)
	require.NoError(t, err)
	return cs
}

// A conversation's turns: each answer's calls carry their results and the
// approval they wait on; none can be asked again while a run is active, and
// the ones done can once it is not.
func TestConversationTurns(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "search_mail", Args: `{"query":"harbour"}`}}},
		testllm.Turn{Text: []string{"Friday."}},
		testllm.Turn{Calls: []testllm.Call{{Name: "create_draft", Args: `{"to":["a@b.c"],"subject":"Re","html":"<p>x</p>"}`}}},
		testllm.Turn{Text: []string{"Drafted."}})
	e.ask(t, conv.ID, "when?")
	evs := e.ask(t, conv.ID, "draft a reply")
	require.Equal(t, "interrupted", evs[len(evs)-1].Kind)
	a := evs[len(evs)-1].Approvals[0]

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, c.Turns, 2)
	t0, t1 := c.Turns[0], c.Turns[1]
	assert.Equal(t, "when?", t0.Question)
	assert.Equal(t, "Friday.", t0.Text)
	require.Len(t, t0.Calls, 1)
	assert.Equal(t, "search_mail", t0.Calls[0].Name)
	require.NotNil(t, t0.Calls[0].Result, "the call's result is on it")
	require.Len(t, t0.Cited, 1)
	assert.Equal(t, []string{t0.Calls[0].ID}, t0.Cited[0].CallIDs)
	assert.Positive(t, t0.Elapsed)
	require.Len(t, t1.Calls, 1)
	require.NotNil(t, t1.Calls[0].Approval, "the approval is on its call")
	assert.Equal(t, a.ID, t1.Calls[0].Approval.ID)
	assert.Empty(t, t1.Approvals)
	assert.False(t, t0.CanAskAgain || t1.CanAskAgain, "a run waits on the user")
	assert.True(t, c.Awaiting)
	assert.False(t, c.Answering)

	sub, stop := e.s.SubscribeConversation(conv.ID)
	defer stop()
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	for ev := range sub {
		if ev.Ends {
			assert.Equal(t, "done", ev.Kind)
			assert.Equal(t, t1.ID, ev.Turn, "the resume's events are the turn's")
			break
		}
	}
	c, err = e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", c.Turns[1].Calls[0].Approval.State)
	assert.Equal(t, "Drafted.", c.Turns[1].Text)
	assert.True(t, c.Turns[0].CanAskAgain && c.Turns[1].CanAskAgain)
}

// A skill is a turn whose question is its command; its proposal answers no
// call, so it is the turn's own.
func TestSkillTurn(t *testing.T) {
	e := newAssistEnv(t)
	d := e.replyDraft(t, "<p>我周五到。</p>")
	e.srv.Script(testllm.Turn{Text: []string{"<p>I'll be there on Friday.</p>"}})
	ch, err := e.s.AssistSkill(t.Context(), d.ID, "translate-draft", SkillOptions{TargetLanguage: "en"})
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.Equal(t, "interrupted", evs[len(evs)-1].Kind)
	conv, err := e.s.AssistConversation(t.Context(), d.ID, false)
	require.NoError(t, err)
	c, err := e.s.GetConversation(t.Context(), conv)
	require.NoError(t, err)
	require.Len(t, c.Turns, 1)
	assert.Equal(t, &ai.Command{Skill: "translate-draft", Lang: "en"}, c.Turns[0].Command)
	assert.Equal(t, evs[0].Turn, c.Turns[0].ID)
	require.Len(t, c.Turns[0].Approvals, 1)
	assert.True(t, c.Turns[0].Approvals[0].DraftEdit)
	assert.False(t, c.Turns[0].CanAskAgain, "a command is not asked again")
}

// Following a conversation gives it as it is and then its runs' events: a
// run waiting on the user resumes on the follow.
func TestFollowConversation(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Text: []string{"Hi."}},
		testllm.Turn{Calls: []testllm.Call{{Name: "create_draft", Args: `{"to":["a@b.c"],"subject":"Re","html":"<p>x</p>"}`}}},
		testllm.Turn{Text: []string{"Done."}})
	e.ask(t, conv.ID, "hello")
	evs := e.ask(t, conv.ID, "draft it")
	require.Equal(t, "interrupted", evs[len(evs)-1].Kind)

	c, ch, stop, err := e.s.FollowConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	defer stop()
	require.Len(t, c.Turns, 2)
	assert.Equal(t, evs[0].RunID, c.ActiveRunID)
	select {
	case ev := <-ch:
		t.Fatalf("nothing is sent before something happens: %+v", ev)
	default:
	}
	require.NoError(t, e.s.ResolveApproval(t.Context(), evs[len(evs)-1].Approvals[0].ID, true))
	var kinds []string
	for ev := range ch {
		kinds = append(kinds, ev.Kind)
		if ev.Ends {
			break
		}
	}
	assert.Equal(t, "done", kinds[len(kinds)-1])
	assert.NotContains(t, kinds, "start", "a resume does not start a run")

	_, _, _, err = e.s.FollowConversation(t.Context(), 999)
	assert.Error(t, err)
}

// A run that ends while FollowConversation reads shows as ended, and its end
// is not sent again; one that starts meanwhile goes on being sent.
func TestFollowConversationRaces(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Text: []string{"thinking"}, Hang: true}, testllm.Turn{Text: []string{"again"}, Hang: true})
	ch, err := e.s.Ask(t.Context(), conv.ID, "hi")
	require.NoError(t, err)
	first := waitKind(t, ch, "text")
	var second int64
	beforeFollowRead = func() { // the run ends, and another starts, once the follow subscribed
		beforeFollowRead = nil
		e.srv.Release()
		waitTerminal(t, ch)
		next, err := e.s.Ask(t.Context(), conv.ID, "again")
		require.NoError(t, err)
		second = (<-next).RunID
		go func() {
			for range next {
			}
		}()
	}
	t.Cleanup(func() { beforeFollowRead = nil })
	c, evs, stop, err := e.s.FollowConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	defer stop()
	require.Len(t, c.Turns, 1, "the first run's answer is in it; the second saved nothing yet")
	assert.Equal(t, "thinking", c.Turns[0].Text)
	assert.Equal(t, second, c.ActiveRunID)
	e.srv.Release()
	for ev := range evs {
		assert.NotEqual(t, first.RunID, ev.RunID, "the ended run's events are in what was read: %+v", ev)
		if ev.RunID == second && ev.Ends {
			break
		}
	}
}

func waitKind(t *testing.T, ch <-chan WitEvent, kind string) WitEvent {
	t.Helper()
	for ev := range ch {
		if ev.Kind == kind {
			return ev
		}
	}
	t.Fatalf("no %s event", kind)
	return WitEvent{}
}

// A turn joins its answer's text, keeps its steps in order, puts each result on its call, adds up its
// runs' time and cites each message once, as the conversation numbers it.
func TestTurnsOf(t *testing.T) {
	state := `[{"role":"user","content":[{"text":"q"}]},
		{"role":"model","content":[{"text":"a"},{"toolRequest":{"name":"search_mail","ref":"c1","input":{}}}]},
		{"role":"tool","content":[{"toolResponse":{"name":"search_mail","ref":"c1","output":[1]}}]},
		{"role":"model","content":[{"text":"b"}]},
		{"role":"user","content":[{"text":"q2"}]}]`
	h, _, _, err := ai.LegacyHistory([]byte(state), nil)
	require.NoError(t, err)
	runs := []runInfo{{id: 1, from: 1, to: 5, active: 6400 * time.Millisecond}, {id: 2, from: 5, to: 6}}
	as := []Approval{{ID: 3, RunID: 1, CallID: "c1"}, {ID: 4, RunID: 2, CallID: "c1"}}
	cs := []Citation{{N: 1, MessageID: 7, Subject: "s7", RunID: 1, Source: citeQuote}, {N: 2, MessageID: 5, RunID: 1, CallID: "c1"},
		{N: 1, MessageID: 7, RunID: 1, CallID: "c1", Suspect: true, Quote: "x"}}
	got := turnsOf(h, runs, as, cs, true, func(m ai.Message) ai.Message { return m })
	require.Len(t, got, 2)
	t0 := got[0]
	assert.Equal(t, "1", t0.ID)
	assert.Equal(t, "a\n\nb", t0.Text)
	assert.Equal(t, []TurnStep{{Kind: "text", Text: "a"}, {Kind: "call"}, {Kind: "text", Text: "b"}}, t0.Steps, "text, its call, then the text after it")
	assert.Equal(t, []int64{1}, t0.Runs)
	assert.Equal(t, 6400*time.Millisecond, t0.Elapsed)
	require.Len(t, t0.Calls, 1)
	require.NotNil(t, t0.Calls[0].Result)
	require.NotNil(t, t0.Calls[0].Approval)
	assert.EqualValues(t, 3, t0.Calls[0].Approval.ID, "its run's approval, not another run's of the same call ID")
	require.Len(t, t0.Quoted, 1)
	assert.Equal(t, []Cited{{Citation: Citation{N: 1, MessageID: 7, Subject: "s7", Suspect: true, Quote: "x"}, CallIDs: []string{"c1"}},
		{Citation: Citation{N: 2, MessageID: 5}, CallIDs: []string{"c1"}}}, t0.Cited, "message 7 once, suspect as any citation of it is")
	assert.Equal(t, []Approval{{ID: 4, RunID: 2, CallID: "c1"}}, got[1].Approvals, "an approval with no call of its turn is the turn's own")
	assert.True(t, t0.CanAskAgain)
}

// A run that waits on the user by the time FollowConversation reads is sent
// from its resume on: its interrupt is in what was read, not sent again.
func TestFollowConversationInterruptedDuringRead(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "create_draft", Args: `{"to":["a@b.c"],"subject":"Re","html":"<p>x</p>"}`}}},
		testllm.Turn{Text: []string{"Done."}})
	var in WitEvent
	beforeFollowRead = func() {
		beforeFollowRead = nil
		evs := e.ask(t, conv.ID, "draft it")
		in = evs[len(evs)-1]
		require.Equal(t, "interrupted", in.Kind)
	}
	t.Cleanup(func() { beforeFollowRead = nil })
	c, ch, stop, err := e.s.FollowConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	defer stop()
	require.True(t, c.Awaiting)
	require.NotNil(t, c.Turns[0].Calls[0].Approval, "the interrupt is in what was read")
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	var kinds []string
	for ev := range ch {
		kinds = append(kinds, ev.Kind)
		if ev.Ends {
			break
		}
	}
	assert.NotContains(t, kinds, "interrupted", "the interrupt is not sent again")
	assert.NotContains(t, kinds, "start")
	assert.Equal(t, "done", kinds[len(kinds)-1])
}

// A run that starts and ends while FollowConversation reads is in what it
// read: none of its events is sent after.
func TestFollowConversationRunWithinRead(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Text: []string{"quick"}}, testllm.Turn{Text: []string{"next"}})
	beforeFollowRead = func() {
		beforeFollowRead = nil
		e.ask(t, conv.ID, "hi")
	}
	t.Cleanup(func() { beforeFollowRead = nil })
	c, ch, stop, err := e.s.FollowConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	defer stop()
	require.Len(t, c.Turns, 1)
	first := c.Turns[0].Runs[0]
	e.ask(t, conv.ID, "again")
	for ev := range ch {
		assert.NotEqual(t, first, ev.RunID, "the read run's events are in what was read: %+v", ev)
		if ev.Ends {
			break
		}
	}
}

// An assist citation saved before sources were kept shows as quoted and as
// context, as it did then.
func TestTurnsOfUnknownSource(t *testing.T) {
	h, _, _, err := ai.LegacyHistory([]byte(`[{"role":"user","content":[{"text":"q"}]}]`), nil)
	require.NoError(t, err)
	got := turnsOf(h, []runInfo{{id: 1, from: 1, to: 2}}, nil, []Citation{{N: 1, MessageID: 7, RunID: 1, Source: citeUnknown}}, false,
		func(m ai.Message) ai.Message { return m })
	require.Len(t, got[0].Quoted, 1)
	require.Len(t, got[0].Cited, 1)
	assert.True(t, got[0].Cited[0].InContext)
}

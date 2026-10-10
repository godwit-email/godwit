package webui

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// witFixture is a fixture with a model; window 1 has a draft whose
// editable part is editable ("" keeps it blank).
func witFixture(t *testing.T, editable string) (*fixture, core.Draft, *coretest.LLM) {
	t.Helper()
	f := newFixture(t, coretest.WithoutAutoSync())
	llm := f.e.LLM(t)
	t.Cleanup(llm.Release)
	d := f.draft(t, "new", 0)
	if editable != "" {
		d = f.setEditable(t, d, editable)
	}
	f.c.Attach(1, d.ID)
	return f, d, llm
}

func (f *fixture) setEditable(t *testing.T, d core.Draft, editable string) core.Draft {
	t.Helper()
	_, q, sig := mail.SplitDraft(d.HTML)
	d.HTML = mail.JoinDraft(editable, q, sig)
	d, err := f.e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	return d
}

// events is what window win got on WitStream so far.
func (f *fixture) events(win int) []WitEvent {
	f.evMu.Lock()
	defer f.evMu.Unlock()
	return slices.Clone(f.evs[win])
}

// waitKind waits for window win to get an event of kind and returns it.
func (f *fixture) waitKind(t *testing.T, win int, kind string) WitEvent {
	t.Helper()
	var got WitEvent
	require.Eventually(t, func() bool {
		for _, ev := range f.events(win) {
			if ev.Kind == kind {
				got = ev
				return true
			}
		}
		return false
	}, 10*time.Second, 5*time.Millisecond, "no %s event; got %+v", kind, f.events(win))
	return got
}

func kinds(evs []WitEvent) []string {
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func proposeTurn(id, html string) coretest.Turn {
	b, _ := json.Marshal(map[string]string{"html": html})
	return coretest.Turn{Calls: []coretest.Call{{ID: id, Name: "update_draft", Args: string(b)}}}
}

func TestComposeWitInfo(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync())
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	info, err := f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.False(t, info.Enabled, "no model")

	llm := f.e.LLM(t)
	t.Cleanup(llm.Release)
	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ReadingLanguage = "ja"
	_, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
	info, err = f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.True(t, info.Enabled)
	assert.Equal(t, []Skill{{"tone-formal", "更正式"}, {"tone-friendly", "更友好"}, {"shorter", "更短"}, {"longer", "更长"}, {"proofread", "校对"}}, info.Skills)
	assert.Equal(t, []Language{{"ja", "日本語"}, {"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"}, {"en", "English"}}, info.Languages)
	assert.Empty(t, info.Chips)
	st2, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, info, st2.Wit)

	f.e.Deliver(t, f.acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "earlier", "before",
		coretest.Date(time.Now().Add(-time.Hour))))
	reply := f.replyDraft(t)
	f.c.Attach(2, reply.ID)
	_, err = f.page.Ask(f.ctx(2), "hi")
	require.NoError(t, err)
	f.waitKind(t, 2, "done")
	info, err = f.page.WitInfo(f.ctx(2))
	require.NoError(t, err)
	assert.Equal(t, []string{"原邮件", "与 Bob 的往来 1 封"}, info.Chips)
}

func TestComposeAskStreams(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	f.c.Attach(2, f.draft(t, "new", 0).ID)
	llm.Script(coretest.Turn{Text: []string{"Hel", "lo"}})
	run, err := f.page.Ask(f.ctx(1), "hi")
	require.NoError(t, err)
	done := f.waitKind(t, 1, "done")
	assert.Equal(t, run, done.Run)
	evs := f.events(1)
	var text string
	for _, ev := range evs {
		assert.Equal(t, run, ev.Run)
		assert.NotZero(t, ev.Conversation)
		if ev.Kind == "text" {
			text += ev.Text
		}
	}
	assert.Equal(t, "Hello", text)
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	assert.Less(t, slices.Index(kinds(evs), "text"), slices.Index(kinds(evs), "done"))
	assert.Empty(t, f.events(2), "another window gets nothing")
}

func TestComposeAskRunActive(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	_, err := f.page.Ask(f.ctx(1), "hi")
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	_, err = f.page.Ask(f.ctx(1), "again")
	require.ErrorIs(t, err, core.ErrRunActive)
	assert.Equal(t, "error.run_active", err.Error())
	llm.Release()
	f.waitKind(t, 1, "done") // still followed
}

func TestComposeStop(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	run, err := f.page.Ask(f.ctx(1), "hi")
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	require.NoError(t, f.page.Stop(f.ctx(1), run))
	require.Eventually(t, func() bool {
		evs := f.events(1)
		return slices.ContainsFunc(evs, func(ev WitEvent) bool { return ev.Kind == "error" || ev.Kind == "done" })
	}, 10*time.Second, 5*time.Millisecond)
	for _, ev := range f.events(1) {
		assert.Empty(t, ev.Error, "a stop is no failure")
	}
}

func TestComposeStopOtherDraftRefused(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	f.c.Attach(2, f.draft(t, "new", 0).ID)
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	run, err := f.page.Ask(f.ctx(1), "hi")
	require.NoError(t, err)
	ev := f.waitKind(t, 1, "text")
	assert.ErrorIs(t, f.page.Stop(f.ctx(2), run), ErrNoDraft)
	c, err := f.e.Svc.GetConversation(t.Context(), ev.Conversation)
	require.NoError(t, err)
	assert.Equal(t, run, c.ActiveRunID, "nothing cancelled")
	llm.Release()
	f.waitKind(t, 1, "done")
}

func TestComposeHistoryPendingEdit(t *testing.T) {
	f, d, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Calls: []coretest.Call{{ID: "c1", Name: "search_mail", Args: `{"query":"plan"}`}}},
		proposeTurn("c2", "<p>Hi</p>"))
	_, err := f.page.Ask(f.ctx(1), "改一下")
	require.NoError(t, err)
	f.waitKind(t, 1, "interrupted")
	f.c.Release(1)
	require.False(t, f.gone(t, d.ID))

	f.c.Attach(3, d.ID)
	h, err := f.page.Follow(f.ctx(3))
	require.NoError(t, err)
	assert.NotZero(t, h.Conversation)
	assert.Zero(t, h.Running, "waiting for a decision is not running")
	require.Len(t, h.Messages, 2)
	assert.Equal(t, "user", h.Messages[0].Role)
	assert.Equal(t, "改一下", h.Messages[0].Text)
	ans := h.Messages[1]
	assert.Equal(t, "assistant", ans.Role)
	require.Len(t, ans.Calls, 2)
	assert.Equal(t, WitCall{ID: "c1", Name: "search_mail", Args: `{"query":"plan"}`}, ans.Calls[0])
	assert.Equal(t, "c2", ans.Calls[1].ID)
	require.Len(t, ans.Results, 1)
	assert.Equal(t, "c1", ans.Results[0].CallID)
	require.Len(t, h.Approvals, 1)
	a := h.Approvals[0]
	assert.Equal(t, "pending", a.State)
	assert.Equal(t, "c2", a.CallID)
	assert.Equal(t, "update_draft", a.Tool)
}

func TestComposeFollowRaceEnded(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	_, err := f.page.Ask(f.ctx(1), "hi")
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	followHook = func() {
		llm.Release()
		f.waitKind(t, 1, "done")
	}
	t.Cleanup(func() { followHook = nil })
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.Zero(t, h.Running, "the run ended")
	require.Len(t, h.Messages, 2)
	assert.Equal(t, "thinking", h.Messages[1].Text)
}

func TestComposeFirstGenerationFollows(t *testing.T) {
	f, d, llm := witFixture(t, "")
	llm.Script(proposeTurn("", "<p>Thanks, Lena!</p>"), coretest.Turn{Text: []string{"写好了"}})
	_, err := f.page.Ask(f.ctx(1), "写一封感谢信")
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	ks := kinds(f.events(1))
	in := slices.Index(ks, "interrupted")
	require.GreaterOrEqual(t, in, 0, "%v", ks)
	assert.Contains(t, ks[in:], "text", "the resume follows")
	ev := f.events(1)[in]
	require.Len(t, ev.Approvals, 1)
	assert.Equal(t, "done", ev.Approvals[0].State)
	ed, _, _ := mail.SplitDraft(f.stored(t, d.ID).HTML)
	assert.Contains(t, ed, "Thanks, Lena!")
}

func TestComposeSkillInterruptCall(t *testing.T) {
	f, _, llm := witFixture(t, "<p>hey there</p>")
	llm.Script(coretest.Turn{Text: []string{"<p>Dear Sir or Madam</p>"}})
	_, err := f.page.Skill(f.ctx(1), "tone-formal", "")
	require.NoError(t, err)
	ev := f.waitKind(t, 1, "interrupted")
	require.Len(t, ev.Interrupts, 1)
	assert.Equal(t, "skill", ev.Interrupts[0].ID)
	require.Len(t, ev.Approvals, 1)
	assert.Equal(t, "skill", ev.Approvals[0].CallID)
	assert.Equal(t, "pending", ev.Approvals[0].State)
}

func TestComposeCloseEmptyReplyMidRun(t *testing.T) {
	f, _, llm := witFixture(t, "")
	reply := f.replyDraft(t)
	f.c.Attach(2, reply.ID)
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	_, err := f.page.Ask(f.ctx(2), "hi")
	require.NoError(t, err)
	f.waitKind(t, 2, "text")
	f.c.Release(2)
	assert.False(t, f.gone(t, reply.ID), "a draft with a running Wit run is kept")
}

func TestComposeWitHistoryGrouping(t *testing.T) {
	c := core.Conversation{ID: 9,
		Messages: []ai.Message{
			{Role: "user", Text: "q"},
			{Role: "model", Text: "a", Calls: []ai.ToolCall{{ID: "c1", Name: "search_mail", Args: json.RawMessage(`{}`)}}},
			{Role: "tool", Results: []ai.ToolResult{{CallID: "c1", Name: "search_mail", Output: json.RawMessage(`[1]`)}}},
			{Role: "model", Text: "b"},
		},
		Runs: []core.RunSpan{{ID: 1, From: 0, To: 4}},
		Citations: []core.Citation{{MessageID: 7, Subject: "s7", RunID: 1}, {MessageID: 5, RunID: 1, CallID: "c1"},
			{MessageID: 7, RunID: 1, CallID: "c1", Suspect: true}},
	}
	got := witMessages(c)
	require.Len(t, got, 2)
	assert.Equal(t, WitMessage{ID: "q0", Role: "user", Text: "q", Calls: []WitCall{}, Results: []WitResult{}, Citations: []Citation{}}, got[0])
	a := got[1]
	assert.Equal(t, "a1", a.ID)
	assert.Equal(t, "assistant", a.Role)
	assert.Equal(t, "a\n\nb", a.Text)
	assert.Equal(t, []WitCall{{ID: "c1", Name: "search_mail", Args: "{}"}}, a.Calls)
	assert.Equal(t, []WitResult{{CallID: "c1", Name: "search_mail", Output: "[1]"}}, a.Results)
	assert.Equal(t, []Citation{{N: 1, MessageID: 7, Subject: "s7", Suspect: true}, {N: 2, MessageID: 5}}, a.Citations)
}

func TestComposeWitErrorKeys(t *testing.T) {
	assert.Equal(t, "", errorKey(&ai.Error{Code: ai.CodeCanceled}))
	assert.Equal(t, "error.ai.rate_limited", errorKey(&ai.Error{Code: ai.CodeRateLimited}))
	assert.Equal(t, "error.run_active", errorKey(errors.Join(errors.New("x"), core.ErrRunActive)))
	assert.Equal(t, "error.unknown", errorKey(errors.New("boom")))
	for _, k := range witErrorKeys {
		assert.Contains(t, composeKeys, k.key)
	}
	for _, code := range []ai.Code{ai.CodeUnauthorized, ai.CodeRateLimited, ai.CodeUnavailable, ai.CodeBadResponse, ai.CodeInternal, ai.CodeStale, ai.CodeIncomplete} {
		assert.Contains(t, composeKeys, "error.ai."+string(code))
	}
	assert.True(t, strings.HasPrefix(errorKey(ErrNoDraft), "error."))
}

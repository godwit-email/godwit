package webui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
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

func TestWitInfoSkillHints(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync())
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	info, err := f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	hints := map[string]string{}
	for _, s := range info.Skills {
		hints[s.ID] = s.Hint
	}
	assert.Equal(t, map[string]string{"tone-formal": "语气", "tone-friendly": "语气", "shorter": "长度", "longer": "长度", "proofread": "只改错字和语法", "format": "仅整理段落与结构"}, hints)
}

func TestComposeWitInfo(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync())
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	info, err := f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.False(t, info.Enabled, "no model")
	assert.Empty(t, info.Model)

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
	_, model, _ := strings.Cut(st.DefaultModel, "/")
	assert.Equal(t, model, info.Model, "the model chip: its name, without the provider")
	assert.NotEmpty(t, info.Model)
	assert.Equal(t, []Skill{{"tone-formal", "更正式", "语气"}, {"tone-friendly", "更友好", "语气"}, {"shorter", "更短", "长度"}, {"longer", "更长", "长度"}, {"proofread", "校对", "只改错字和语法"}, {"format", "排版整理", "仅整理段落与结构"}}, info.Skills)
	assert.Equal(t, []Language{
		{"ja", "日本語"}, {"de", "Deutsch"}, {"zh-Hans", "简体中文"}, {"zh-Hant", "繁體中文"}, {"en", "English"},
		{"ko", "한국어"}, {"fr", "Français"}, {"es", "Español"}, {"pt", "Português"}, {"it", "Italiano"},
		{"ru", "Русский"}, {"nl", "Nederlands"}, {"tr", "Türkçe"}, {"vi", "Tiếng Việt"},
	}, info.Languages)
	assert.Empty(t, info.Chips)
	st2, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, info, st2.Wit)

	f.e.Deliver(t, f.acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "earlier", "before",
		coretest.Date(time.Now().Add(-time.Hour))))
	reply := f.replyDraft(t)
	f.c.Attach(2, reply.ID)
	_, err = f.page.Ask(f.ctx(2), "hi", nil, "", "", "")
	require.NoError(t, err)
	f.waitKind(t, 2, "done")
	info, err = f.page.WitInfo(f.ctx(2))
	require.NoError(t, err)
	assert.Equal(t, []string{"原邮件", "与 Bob 的往来 1 封"}, info.Chips)
	assert.Equal(t, "正體中文", langName("zh-TW", isZhTW("zh-TW")))
	assert.Equal(t, "繁體中文", langName("zh-TW", isZhTW("zh-HK")))
}

// The compose panel picks its conversation's model and effort as the Wit
// page does, from the same list, and opens or checks cited mail.
func TestComposeModelAndCitedMail(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync())
	llm := f.e.LLM(t)
	t.Cleanup(llm.Release)
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	ms, err := f.page.Models(f.ctx(1))
	require.NoError(t, err)
	require.NotEmpty(t, ms)
	_, err = f.page.Ask(f.ctx(1), "hi", nil, ms[0].Ref, "high", "")
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, ms[0].Ref, h.Model)
	assert.Equal(t, "high", h.Effort)
	_, err = f.page.Ask(f.ctx(1), "hi", nil, "", "extreme", "")
	assert.Error(t, err, "an unknown effort")

	var opened []int64
	f.page.d.OpenMessage = func(id int64) { opened = append(opened, id) }
	f.e.Deliver(t, f.acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Hi", "hi"))
	page, err := f.e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	id := page.Items[0].ID
	require.NoError(t, f.page.OpenCited(f.ctx(1), id))
	assert.Equal(t, []int64{id}, opened)
	assert.ErrorIs(t, f.page.CheckCited(f.ctx(1), id+999), ErrMailGone)
	assert.ErrorIs(t, f.page.OpenCited(f.ctx(1), id+999), ErrMailGone)
	assert.Equal(t, []int64{id}, opened, "nothing opens for a mail gone")
}

func TestComposeOpenModels(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync())
	opened := 0
	f.page.d.OpenModels = func() { opened++ }
	f.c.Attach(1, f.draft(t, "new", 0).ID)
	require.NoError(t, f.page.OpenModels(f.ctx(1)))
	assert.Equal(t, 1, opened)
	assert.Error(t, f.page.OpenModels(t.Context()), "no calling window")
	assert.Equal(t, 1, opened)
}

func TestComposeAskStreams(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	f.c.Attach(2, f.draft(t, "new", 0).ID)
	llm.Script(coretest.Turn{Text: []string{"Hel", "lo"}})
	runStart, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	run := runStart.Run
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
	_, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	_, err = f.page.Ask(f.ctx(1), "again", nil, "", "", "")
	require.ErrorIs(t, err, core.ErrRunActive)
	assert.Equal(t, "error.run_active", err.Error())
	llm.Release()
	f.waitKind(t, 1, "done") // still followed
}

func TestComposeStop(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	runStart, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	run := runStart.Run
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
	runStart, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	run := runStart.Run
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
	_, err := f.page.Ask(f.ctx(1), "改一下", nil, "", "", "")
	require.NoError(t, err)
	f.waitKind(t, 1, "interrupted")
	f.c.Release(1)
	require.False(t, f.gone(t, d.ID))

	f.c.Attach(3, d.ID)
	h, err := f.page.Follow(f.ctx(3))
	require.NoError(t, err)
	assert.NotZero(t, h.Conversation)
	assert.Zero(t, h.Running, "waiting for a decision is not running")
	require.Len(t, h.Turns, 1)
	ans := h.Turns[0]
	assert.Equal(t, "改一下", ans.Question)
	require.Len(t, ans.Calls, 2)
	assert.Equal(t, WitCall{ID: "c1", Name: "search_mail", Args: `{"query":"plan"}`}, ans.Calls[0].Call)
	require.NotNil(t, ans.Calls[0].Result)
	assert.Nil(t, ans.Calls[0].Approval)
	assert.Equal(t, "c2", ans.Calls[1].Call.ID)
	assert.Nil(t, ans.Calls[1].Result)
	require.NotNil(t, ans.Calls[1].Approval, "the approval is on its call")
	a := *ans.Calls[1].Approval
	assert.Equal(t, "pending", a.State)
	assert.Equal(t, "update_draft", a.Tool)
	assert.True(t, a.DraftEdit)
	assert.Empty(t, ans.Approvals)
}

func TestComposeFollowRunningThenEnds(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	runStart, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	run := runStart.Run
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, run, h.Running)
	llm.Release()
	assert.Equal(t, run, f.waitKind(t, 1, "done").Run, "the end comes after Follow's reply")
}

func TestComposeReleaseDuringAskLeavesNoSubscription(t *testing.T) {
	f, d, llm := witFixture(t, "<p>Hello</p>")
	startHook = func() { // the window closes while Ask is inside
		go f.c.Release(1)
		require.Eventually(t, func() bool {
			f.c.mu.Lock()
			defer f.c.mu.Unlock()
			return f.c.sessions[1] == nil
		}, 10*time.Second, 5*time.Millisecond)
	}
	t.Cleanup(func() { startHook = nil })
	_, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	require.ErrorIs(t, err, ErrNoDraft)
	f.c.mu.Lock()
	assert.Empty(t, f.c.follows)
	f.c.mu.Unlock()
	assert.Zero(t, llm.Requests(), "no run")
	assert.False(t, f.gone(t, d.ID))
}

func TestComposeSameConversationNoDuplicates(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"one"}, Hang: true}, coretest.Turn{Text: []string{"two"}})
	r1Start, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "")
	r1 := r1Start.Run
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	_, err = f.page.Ask(f.ctx(1), "again", nil, "", "", "")
	require.ErrorIs(t, err, core.ErrRunActive)
	llm.Release()
	f.waitKind(t, 1, "done")
	r2Start, err := f.page.Ask(f.ctx(1), "and again", nil, "", "", "") // right after: the first run's subscription stays
	r2 := r2Start.Run
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(f.events(1), func(ev WitEvent) bool { return ev.Kind == "done" && ev.Run == r2 })
	}, 10*time.Second, 5*time.Millisecond)
	var got []string
	for _, ev := range f.events(1) {
		if ev.Kind == "text" || ev.Kind == "done" {
			got = append(got, ev.Kind+":"+ev.Text)
			assert.Contains(t, []int64{r1, r2}, ev.Run)
		}
	}
	assert.Equal(t, []string{"text:one", "done:", "text:two", "done:"}, got)
}

func TestComposeWitJSONLists(t *testing.T) {
	f, d, _ := witFixture(t, "<p>Hello</p>")
	_, err := f.e.Svc.NewConversation(t.Context(), "assist", d.ID)
	require.NoError(t, err)
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.NotZero(t, h.Conversation)
	b, err := json.Marshal(h)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"turns":[]`)
	assert.NotContains(t, string(b), "null")
	b, err = json.Marshal(witTurns(core.Conversation{Turns: []core.Turn{{ID: "1", Question: "q", Text: "a"}}}))
	require.NoError(t, err)
	assert.NotContains(t, strings.ReplaceAll(string(b), `"command":null`, ""), "null")
	b, err = json.Marshal(witEvent(1, core.WitEvent{Event: ai.Event{Kind: "done"}}))
	require.NoError(t, err)
	assert.NotContains(t, strings.ReplaceAll(strings.ReplaceAll(string(b), `"call":null`, ""), `"result":null`, ""), "null")
}

func TestComposeFirstGenerationFollows(t *testing.T) {
	f, d, llm := witFixture(t, "")
	llm.Script(proposeTurn("", "<p>Thanks, Lena!</p>"))
	_, err := f.page.Ask(f.ctx(1), "写一封感谢信", nil, "", "", "")
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	ks := kinds(f.events(1))
	in := slices.Index(ks, "interrupted")
	require.GreaterOrEqual(t, in, 0, "%v", ks)
	assert.Equal(t, "done", ks[len(ks)-1], "the run finishes directly as done")
	assert.NotContains(t, ks[in:], "text", "no redundant model turn after draft edit")
	ev := f.events(1)[in]
	require.Len(t, ev.Approvals, 1)
	assert.Equal(t, "done", ev.Approvals[0].State)
	ed, _, _ := mail.SplitDraft(f.stored(t, d.ID).HTML)
	assert.Contains(t, ed, "Thanks, Lena!")
}

func TestComposeSkillInterruptCall(t *testing.T) {
	f, _, llm := witFixture(t, "<p>hey there</p>")
	llm.Script(coretest.Turn{Text: []string{"<p>Dear Sir or Madam</p>"}})
	_, err := f.page.Skill(f.ctx(1), "tone-formal", "", "")
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
	_, err := f.page.Ask(f.ctx(2), "hi", nil, "", "", "")
	require.NoError(t, err)
	f.waitKind(t, 2, "text")
	f.c.Release(2)
	assert.False(t, f.gone(t, reply.ID), "a draft with a running Wit run is kept")
}

func TestComposeWitTurns(t *testing.T) {
	pending := core.Approval{ID: 4, RunID: 1, CallID: "c2", Tool: "update_draft", State: "pending", DraftEdit: true}
	c := core.Conversation{ID: 9, Turns: []core.Turn{{
		ID: "1", Question: "q", Quoted: []core.Citation{{MessageID: 7, Subject: "s7"}}, Text: "a\n\nb", Reasoning: "r",
		Calls: []core.TurnCall{
			{ToolCall: ai.ToolCall{ID: "c1", Name: "search_mail", Args: json.RawMessage(`{}`)}, Result: &ai.ToolResult{CallID: "c1", Name: "search_mail", Output: json.RawMessage(`[1]`)}},
			{ToolCall: ai.ToolCall{ID: "c2", Name: "update_draft", Args: json.RawMessage(`{}`)}, Approval: &pending}},
		Cited: []core.Cited{{Citation: core.Citation{N: 1, MessageID: 7, Subject: "s7", Suspect: true}, CallIDs: []string{"c1"}}},
		Runs:  []int64{1}, Elapsed: 6400 * time.Millisecond, CanAskAgain: true,
	}, {ID: "5", Command: &ai.Command{Skill: "translate-draft", Lang: "fr"}, Approvals: []core.Approval{{ID: 6, CallID: "skill", Tool: "update_draft"}}, Runs: []int64{2}}}}
	got := witTurns(c)
	require.Len(t, got, 2)
	q := got[0]
	assert.Equal(t, "1", q.ID)
	assert.Equal(t, []MailHit{{ID: 7, Subject: "s7"}}, q.Quoted)
	assert.Equal(t, "a\n\nb", q.Text)
	assert.Equal(t, int64(6400), q.Elapsed)
	assert.True(t, q.CanAskAgain)
	require.Len(t, q.Calls, 2)
	assert.Equal(t, WitTurnCall{Call: WitCall{ID: "c1", Name: "search_mail", Args: "{}"}, Result: &WitResult{CallID: "c1", Name: "search_mail", Output: "[1]"}}, q.Calls[0])
	require.NotNil(t, q.Calls[1].Approval)
	assert.Equal(t, int64(4), q.Calls[1].Approval.ID)
	assert.True(t, q.Calls[1].Approval.DraftEdit)
	assert.Equal(t, []Citation{{N: 1, MessageID: 7, Subject: "s7", Suspect: true, CallIDs: []string{"c1"}}}, q.Citations)
	assert.Equal(t, &WitCommand{Skill: "translate-draft", Lang: "fr"}, got[1].Command)
	require.Len(t, got[1].Approvals, 1, "a skill's edit is the turn's own")
}
func TestComposeWitErrorKeys(t *testing.T) {
	assert.Equal(t, "", errorKey(&ai.Error{Code: ai.CodeCanceled}))
	assert.Equal(t, "error.ai.rate_limited", errorKey(&ai.Error{Code: ai.CodeRateLimited}))
	assert.Equal(t, "error.run_active", errorKey(errors.Join(errors.New("x"), core.ErrRunActive)))
	assert.Equal(t, "error.unknown", errorKey(errors.New("boom")))
	for _, k := range witErrorKeys {
		assert.NotEqual(t, k.key, i18n.T(k.key))
	}
	for _, code := range []ai.Code{ai.CodeUnauthorized, ai.CodeRateLimited, ai.CodeUnavailable, ai.CodeBadResponse, ai.CodeInternal, ai.CodeStale, ai.CodeIncomplete} {
		assert.NotEqual(t, "error.ai."+string(code), i18n.T("error.ai."+string(code)))
	}
	assert.True(t, strings.HasPrefix(errorKey(ErrNoDraft), "error."))
	assert.Equal(t, "", keyed(context.Canceled).Error(), "a cancellation shows nothing")
	assert.ErrorIs(t, keyed(context.Canceled), context.Canceled)
}

// installNetPlugin installs a plugin holding mail:read and net with a read
// tool, ping: every call of it waits for an egress approval.
func (f *fixture) installNetPlugin(t *testing.T) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "netty")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := `{"name":"netty","version":"1.0.0","main":"index.ts","permissions":{"mail":["read"],"net":["` + coretest.EgressHost + `:80"]}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600))
	code := `export default function (ctx: any) {
  ctx.tools.register({ name: "ping", description: "Pings.", write: false, schema: { type: "object" },
    run: async () => (await ctx.net.fetch("http://` + coretest.EgressHost + `:80/ping")).text() })
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(code), 0o600))
	ctx := t.Context()
	_, err := f.e.Svc.InstallPlugin(ctx, dir)
	require.NoError(t, err)
	require.NoError(t, f.e.Svc.GrantPermission(ctx, "netty"))
	require.NoError(t, f.e.Svc.SetPluginEnabled(ctx, "netty", true))
}

// A plugin tool's approval in an Assist run is decided from the compose
// window: one by Decide, the rest of its run by DecideAll; another
// draft's window can do neither.
func TestComposeDecideToolApprovals(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	f.installNetPlugin(t)
	f.c.Attach(2, f.draft(t, "new", 0).ID)
	llm.Script(coretest.Turn{Calls: []coretest.Call{{ID: "p1", Name: "plugin_netty_ping", Args: `{"to":["a@x.com"]}`},
		{ID: "p2", Name: "plugin_netty_ping", Args: `{}`}}}, coretest.Turn{Text: []string{"pinged"}})
	runStart, err := f.page.Ask(f.ctx(1), "ping twice", nil, "", "", "")
	run := runStart.Run
	require.NoError(t, err)
	in := f.waitKind(t, 1, "interrupted")
	require.Len(t, in.Approvals, 2)
	a := in.Approvals[0]
	assert.Equal(t, "plugin_netty_ping", a.Tool)
	assert.Equal(t, []string{coretest.EgressHost + ":80"}, a.Destinations)
	assert.JSONEq(t, `{"to":["a@x.com"]}`, a.Args, "the page shows the call's arguments")

	_, err = f.page.Decide(f.ctx(2), a.ID, true)
	assert.ErrorIs(t, err, core.ErrInvalidState, "another draft's window")
	_, err = f.page.DecideAll(f.ctx(2), run, true)
	assert.ErrorIs(t, err, core.ErrInvalidState, "another draft's window")

	_, err = f.page.Decide(f.ctx(1), a.ID, true)
	require.NoError(t, err)
	n := len(f.events(1))
	_, err = f.page.DecideAll(f.ctx(1), run, false)
	require.NoError(t, err)
	f.doneAfter(t, n, run)
	as, err := f.e.Svc.ListApprovals(t.Context(), in.Conversation)
	require.NoError(t, err)
	var states []string
	for _, x := range as {
		states = append(states, x.State)
	}
	assert.Equal(t, []string{"done", "rejected"}, states)
	_, err = f.page.DecideAll(f.ctx(1), run, true)
	assert.ErrorIs(t, err, core.ErrInvalidState, "nothing pending")
}

// A ⌘K run goes on after its window closes: the window reopened follows
// its conversation, running, and gets the rest of its events.
func TestComposeFollowReopenedCmdKRun(t *testing.T) {
	f, d, llm := witFixture(t, "<p>Hello world</p>")
	llm.Script(coretest.Turn{Text: []string{"hi"}})
	_, err := f.page.Ask(f.ctx(1), "hi", nil, "", "", "") // the draft has its Assist conversation too
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	st, err := f.page.Load(f.ctx(1))
	require.NoError(t, err)
	text, _, _ := plainText(st.Body)
	rs, err := f.page.Rewrite(f.ctx(1), st.Revision, text, 6, 11, "更正式")
	require.NoError(t, err)
	require.False(t, rs.Conflict)
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(f.events(1), func(ev WitEvent) bool { return ev.Kind == "text" && ev.Run == rs.Run })
	}, 10*time.Second, 5*time.Millisecond)
	f.c.Release(1)
	f.c.Attach(3, d.ID)
	h, err := f.page.Follow(f.ctx(3))
	require.NoError(t, err)
	assert.Equal(t, rs.Run, h.Running)
	llm.Release()
	assert.Equal(t, rs.Run, f.waitKind(t, 3, "done").Run)
}

// The compose panel quotes mail as the Wit page does: SearchMail finds it,
// Ask cites it first.
func TestComposeAskQuotes(t *testing.T) {
	f, _, llm := witFixture(t, "")
	f.e.Deliver(t, f.acc.ID, "INBOX", coretest.Msg("lena@partner.example", f.acc.Email, "Q4 partnership", "November works"))
	hits, err := f.page.SearchMail(f.ctx(1), "partnership")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	_, err = f.page.SearchMail(f.ctx(2), "partnership")
	assert.Error(t, err, "a window without a draft")
	llm.Script(coretest.Turn{Text: []string{"ok"}})
	_, err = f.page.Ask(f.ctx(1), "用这封", []int64{hits[0].ID}, "", "", "")
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	evs := f.events(1)
	require.Equal(t, "start", evs[0].Kind)
	require.Equal(t, "context", evs[1].Kind)
	assert.Equal(t, hits[0].ID, evs[1].Citations[0].MessageID)
}

// No model but a translation plugin: the panel shows, for translating alone.
func TestComposeWitInfoTranslateOnly(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync())
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.Translator = "plugin/translate-demo"
	_, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
	info, err := f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.False(t, info.Enabled, "the chosen plugin is not running")

	f.e.InstallPlugin(t, "translate-demo")
	info, err = f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.True(t, info.Enabled)
	assert.True(t, info.TranslateOnly)
	assert.NotEmpty(t, info.Languages)

	llm := f.e.LLM(t)
	t.Cleanup(llm.Release)
	info, err = f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.True(t, info.Enabled)
	assert.False(t, info.TranslateOnly, "with a model the whole panel shows")
}

type fakeSysTranslator struct {
	installed []string
}

func (f *fakeSysTranslator) Version() string              { return "1.0" }
func (f *fakeSysTranslator) InstalledLanguages() []string { return f.installed }
func (f *fakeSysTranslator) Translate(_ context.Context, texts []string, _, _ string) ([]string, string, error) {
	return texts, "en", nil
}

func TestComposeWitInfoSystemTranslateInstalledLanguages(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync(), coretest.WithSystemTranslator(&fakeSysTranslator{installed: []string{"de", "en", "tr"}}))
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)

	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.Translator = "plugin/" + core.SystemTranslationPlugin
	_, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)

	info, err := f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.True(t, info.Enabled)
	var tags []string
	for _, l := range info.Languages {
		tags = append(tags, l.Tag)
	}
	assert.Contains(t, tags, "de")
	assert.Contains(t, tags, "en")
	assert.Contains(t, tags, "tr")
	assert.NotContains(t, tags, "vi")
	assert.NotContains(t, tags, "ja")
	assert.NotContains(t, tags, "es")
}

// None downloaded offers none, not the reading language it cannot translate to.
func TestComposeWitInfoSystemTranslateNoneInstalled(t *testing.T) {
	f := newFixture(t, coretest.WithoutAutoSync(), coretest.WithSystemTranslator(&fakeSysTranslator{}))
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)
	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.Translator = "plugin/" + core.SystemTranslationPlugin
	_, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)

	info, err := f.page.WitInfo(f.ctx(1))
	require.NoError(t, err)
	assert.NotNil(t, info.Languages, "the panel reads an empty list, not null")
	assert.Empty(t, info.Languages)
}

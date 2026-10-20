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
	assert.Equal(t, map[string]string{"tone-formal": "语气", "tone-friendly": "语气", "shorter": "长度", "longer": "长度", "proofread": "只改错字和语法"}, hints)
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
	assert.Equal(t, []Skill{{"tone-formal", "更正式", "语气"}, {"tone-friendly", "更友好", "语气"}, {"shorter", "更短", "长度"}, {"longer", "更长", "长度"}, {"proofread", "校对", "只改错字和语法"}}, info.Skills)
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
	_, err = f.page.Ask(f.ctx(2), "hi", nil)
	require.NoError(t, err)
	f.waitKind(t, 2, "done")
	info, err = f.page.WitInfo(f.ctx(2))
	require.NoError(t, err)
	assert.Equal(t, []string{"原邮件", "与 Bob 的往来 1 封"}, info.Chips)
	assert.Equal(t, "正體中文", langName("zh-TW", isZhTW("zh-TW")))
	assert.Equal(t, "繁體中文", langName("zh-TW", isZhTW("zh-HK")))
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
	run, err := f.page.Ask(f.ctx(1), "hi", nil)
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
	_, err := f.page.Ask(f.ctx(1), "hi", nil)
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	_, err = f.page.Ask(f.ctx(1), "again", nil)
	require.ErrorIs(t, err, core.ErrRunActive)
	assert.Equal(t, "error.run_active", err.Error())
	llm.Release()
	f.waitKind(t, 1, "done") // still followed
}

func TestComposeStop(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	run, err := f.page.Ask(f.ctx(1), "hi", nil)
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
	run, err := f.page.Ask(f.ctx(1), "hi", nil)
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
	_, err := f.page.Ask(f.ctx(1), "改一下", nil)
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
	run, err := f.page.Ask(f.ctx(1), "hi", nil)
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	followHook = func() { // the run ends after Follow read it running
		llm.Release()
		require.Eventually(t, func() bool {
			f.c.mu.Lock()
			fl := f.c.follows[1]
			f.c.mu.Unlock()
			return fl.endedWhileHeld(run)
		}, 10*time.Second, 5*time.Millisecond)
		assert.NotContains(t, kinds(f.events(1)), "done", "held back during the reads")
	}
	t.Cleanup(func() { followHook = nil })
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.Zero(t, h.Running, "the run ended")
	require.Len(t, h.Messages, 2)
	assert.Equal(t, "thinking", h.Messages[1].Text, "read again")
	assert.Contains(t, kinds(f.events(1)), "done", "sent once Follow returns")
}

func TestComposeFollowRunningThenEnds(t *testing.T) {
	f, _, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	run, err := f.page.Ask(f.ctx(1), "hi", nil)
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, run, h.Running)
	llm.Release()
	assert.Equal(t, run, f.waitKind(t, 1, "done").Run, "the end comes after Follow's reply")
}

func TestComposeFollowKeepsRunStartedDuringReads(t *testing.T) {
	f, d, llm := witFixture(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true}, coretest.Turn{Status: 503}) // the second run retries: still running
	run, err := f.page.Ask(f.ctx(1), "hi", nil)
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	var next int64
	followHook = func() { // the run ends, and another starts, after Follow read it
		llm.Release()
		require.Eventually(t, func() bool {
			f.c.mu.Lock()
			fl := f.c.follows[1]
			f.c.mu.Unlock()
			return fl.endedWhileHeld(run)
		}, 10*time.Second, 5*time.Millisecond)
		ch, err := f.e.Svc.Assist(t.Context(), d.ID, "again")
		require.NoError(t, err)
		go func() {
			for range ch {
			}
		}()
		c, err := f.e.Svc.GetConversation(t.Context(), f.events(1)[0].Conversation)
		require.NoError(t, err)
		next = c.ActiveRunID
		require.NotZero(t, next)
		require.NotEqual(t, run, next)
	}
	t.Cleanup(func() { followHook = nil })
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	assert.Equal(t, next, h.Running, "the new run is not wiped")
	require.NoError(t, f.page.Stop(f.ctx(1), next))
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
	_, err := f.page.Ask(f.ctx(1), "hi", nil)
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
	r1, err := f.page.Ask(f.ctx(1), "hi", nil)
	require.NoError(t, err)
	f.waitKind(t, 1, "text")
	_, err = f.page.Ask(f.ctx(1), "again", nil)
	require.ErrorIs(t, err, core.ErrRunActive)
	llm.Release()
	f.waitKind(t, 1, "done")
	r2, err := f.page.Ask(f.ctx(1), "and again", nil) // right after: the first run's subscription stays
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
	assert.Contains(t, string(b), `"messages":[]`)
	assert.Contains(t, string(b), `"approvals":[]`)
	assert.NotContains(t, string(b), "null")
	b, err = json.Marshal(witMessages(core.Conversation{Messages: []ai.Message{{Role: "user", Text: "q"}, {Role: "model", Text: "a"}}}))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "null")
	b, err = json.Marshal(witEvent(1, core.WitEvent{Event: ai.Event{Kind: "done"}}))
	require.NoError(t, err)
	assert.NotContains(t, strings.ReplaceAll(strings.ReplaceAll(string(b), `"call":null`, ""), `"result":null`, ""), "null")
}

func TestComposeFirstGenerationFollows(t *testing.T) {
	f, d, llm := witFixture(t, "")
	llm.Script(proposeTurn("", "<p>Thanks, Lena!</p>"), coretest.Turn{Text: []string{"写好了"}})
	_, err := f.page.Ask(f.ctx(1), "写一封感谢信", nil)
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
	_, err := f.page.Ask(f.ctx(2), "hi", nil)
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
		Citations: []core.Citation{{N: 1, MessageID: 7, Subject: "s7", RunID: 1}, {N: 2, MessageID: 5, RunID: 1, CallID: "c1"},
			{N: 1, MessageID: 7, RunID: 1, CallID: "c1", Suspect: true, Quote: "q"}},
	}
	got := witMessages(c)
	require.Len(t, got, 2)
	assert.Equal(t, WitMessage{ID: "q0", Role: "user", Text: "q", Calls: []WitCall{}, Results: []WitResult{}, Citations: []Citation{}, Runs: []int64{1},
		Quoted: []MailHit{{ID: 7, Subject: "s7"}}}, got[0], "the question quoted the mail its context cites")
	a := got[1]
	assert.Equal(t, "a1", a.ID)
	assert.Equal(t, "assistant", a.Role)
	assert.Equal(t, "a\n\nb", a.Text)
	assert.Equal(t, []int64{1}, a.Runs)
	assert.Empty(t, a.Quoted)
	assert.Equal(t, []WitCall{{ID: "c1", Name: "search_mail", Args: "{}"}}, a.Calls)
	assert.Equal(t, []WitResult{{CallID: "c1", Name: "search_mail", Output: "[1]"}}, a.Results)
	assert.Equal(t, []Citation{{N: 1, MessageID: 7, Subject: "s7", Suspect: true, Quote: "q"}, {N: 2, MessageID: 5}}, a.Citations)
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
	run, err := f.page.Ask(f.ctx(1), "ping twice", nil)
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
	_, err := f.page.Ask(f.ctx(1), "hi", nil) // the draft has its Assist conversation too
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
	_, err = f.page.Ask(f.ctx(1), "用这封", []int64{hits[0].ID})
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	evs := f.events(1)
	require.Equal(t, "context", evs[0].Kind)
	assert.Equal(t, hits[0].ID, evs[0].Citations[0].MessageID)
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
	f := newFixture(t, coretest.WithoutAutoSync())
	d := f.draft(t, "new", 0)
	f.c.Attach(1, d.ID)

	fakeSys := &fakeSysTranslator{installed: []string{"de", "en", "tr"}}
	f.e.Svc.SetSystemTranslator(fakeSys)

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

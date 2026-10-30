package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nativeTranslator installs, grants and enables a fake native translator dlx
// (plugins.FakeNativeOpener) with extra in its library, and selects it. It
// returns the path its record file uses.
func useNativeTranslator(t *testing.T, s *Service, extra map[string]any) string {
	t.Helper()
	s.SetNativeHostForTest("GODWIT_CORETEST_NATIVE=1")
	record := filepath.Join(t.TempDir(), "record.json")
	lib := map[string]any{"describe": map[string]any{"name": "dlx", "version": "0.1.0", "abi": 1, "capabilities": []string{"translate"}}, "record": record}
	for k, v := range extra {
		lib[k] = v
	}
	dir := filepath.Join(t.TempDir(), "dlx")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"dlx","version":"0.1.0","native":{"abi":1},"capabilities":["translate"],"config":[{"key":"engine","type":"text","label":"Engine"}]}`), 0o600))
	b, _ := json.Marshal(lib)
	require.NoError(t, os.WriteFile(filepath.Join(dir, plugins.LibraryName()), b, 0o600))
	_, err := s.InstallPlugin(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(t.Context(), "dlx"))
	require.NoError(t, s.SetPluginEnabled(t.Context(), "dlx", true))
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.Translator = "plugin/dlx"
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	return record
}

// translateRequests are the translate requests the fake library received.
func translateRequests(t *testing.T, record string) []map[string]any {
	t.Helper()
	f, err := os.Open(record + ".translate")
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r))
		out = append(out, r)
	}
	return out
}

func setBody(t *testing.T, e skillEnv, text, html string) {
	t.Helper()
	_, err := e.s.db.Execute(t.Context(), "UPDATE messages SET body_text = ?, body_html = ? WHERE id = ?", text, html, e.msg)
	require.NoError(t, err)
}

func TestPluginTranslate(t *testing.T) {
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	setBody(t, e, "Hello\n\nSee you", "")
	tg := Target{Kind: "message", ID: e.msg}
	evs := e.run(t, "translate", tg)
	require.Equal(t, "context", evs[0].Kind)
	assert.Equal(t, "plugin/dlx", evs[0].Model)
	assert.Equal(t, []string{"[zh-CN] Hello\n\n[zh-CN] See you"}, texts(evs))
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	req := translateRequests(t, record)
	require.Len(t, req, 1)
	assert.Equal(t, []any{"Hello", "See you"}, req[0]["texts"])
	assert.Equal(t, "zh-CN", req[0]["target"])
	assert.Equal(t, "", req[0]["source"])

	assert.Equal(t, texts(evs), texts(e.run(t, "translate", tg)), "cached")
	assert.Len(t, translateRequests(t, record), 1, "the second run did not ask the plugin")
	assert.Empty(t, e.srv.Requests(), "no model was asked")
}

func TestPluginTranslateNoTranslation(t *testing.T) {
	e := newSkillEnv(t)
	useNativeTranslator(t, e.s, map[string]any{"source": "zh"})
	assert.Equal(t, []ai.Event{{Kind: "done"}}, noContext(e.run(t, "translate", Target{Kind: "message", ID: e.msg})))

	e2 := newSkillEnv(t)
	useNativeTranslator(t, e2.s, map[string]any{"source": "en-US"})
	st, _ := e2.s.GetSettings(t.Context())
	st.NoTranslate = []string{"en"}
	_, err := e2.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	assert.Equal(t, []ai.Event{{Kind: "done"}}, noContext(e2.run(t, "translate", Target{Kind: "message", ID: e2.msg})))
}

func TestPluginTranslateWithoutModel(t *testing.T) {
	e := newSkillEnv(t)
	useNativeTranslator(t, e.s, nil)
	require.NoError(t, e.s.RemoveModelService(t.Context(), e.ms.ID))
	st, _ := e.s.GetSettings(t.Context())
	st.DefaultModel, st.SmallModel = "", ""
	_, err := e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	ref, err := e.s.TaskModel(t.Context(), "translate")
	require.NoError(t, err)
	assert.Equal(t, "plugin/dlx", ref)
	assert.NotEmpty(t, texts(e.run(t, "translate", Target{Kind: "message", ID: e.msg})))
}

func TestPluginTranslateUnavailable(t *testing.T) {
	e := newSkillEnv(t)
	useNativeTranslator(t, e.s, nil)
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), "dlx", false))
	_, err := e.s.TaskModel(t.Context(), "translate")
	assert.ErrorIs(t, err, ErrTranslatorUnavailable)
	_, err = e.s.RunSkill(t.Context(), "translate", Target{Kind: "message", ID: e.msg})
	assert.ErrorIs(t, err, ErrTranslatorUnavailable)
	assert.Empty(t, e.srv.Requests(), "it does not fall back to the model")
}

func TestPluginTranslateBodies(t *testing.T) {
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	tg := Target{Kind: "message", ID: e.msg}

	setBody(t, e, "", "<p>Hello <b>there</b></p>")
	e.run(t, "translate", tg)
	req := translateRequests(t, record)
	require.Len(t, req, 1)
	got := fmt.Sprint(req[0]["texts"])
	assert.Contains(t, got, "Hello")
	assert.NotContains(t, got, "<p>")

	setBody(t, e, " \n ", "")
	assert.Equal(t, []ai.Event{{Kind: "done"}}, noContext(e.run(t, "translate", tg)))
	assert.Len(t, translateRequests(t, record), 1, "an empty body asks nothing")

	setBody(t, e, strings.Repeat("a", 30000), "")
	e.run(t, "translate", tg)
	req = translateRequests(t, record)
	require.Len(t, req, 2)
	n := 0
	for _, x := range req[1]["texts"].([]any) {
		n += utf8.RuneCountInString(x.(string))
	}
	assert.LessOrEqual(t, n, defaultMailBudget.PerMessage)
}

func TestPluginTranslateSelection(t *testing.T) {
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	tg := Target{Kind: "selection", ID: e.msg, Text: "Hi"}
	assert.Equal(t, []string{"[zh-CN] Hi"}, texts(e.run(t, "translate", tg)))
	e.run(t, "translate", tg)
	assert.Len(t, translateRequests(t, record), 2, "a selection is not cached")
}

func TestPluginTranslateFailures(t *testing.T) {
	e := newSkillEnv(t)
	useNativeTranslator(t, e.s, map[string]any{"translateWrongCount": true})
	setBody(t, e, "One\n\nTwo", "")
	for range 3 {
		_, err := e.s.RunSkill(t.Context(), "translate", Target{Kind: "selection", ID: e.msg, Text: "a\n\nb"})
		assert.Error(t, err)
	}
	p := pluginInfoByName(t, e.s, "dlx")
	assert.False(t, p.Enabled)
	assert.Equal(t, disabledFailures, p.DisabledReason)

	e2 := newSkillEnv(t)
	useNativeTranslator(t, e2.s, map[string]any{"translateError": "rate_limited"})
	for range 10 {
		_, err := e2.s.RunSkill(t.Context(), "translate", Target{Kind: "selection", ID: e2.msg, Text: "a"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "slow down")
	}
	assert.True(t, pluginInfoByName(t, e2.s, "dlx").Enabled, "the plugin's own error is an answer, not a failure")
}

func TestListTranslators(t *testing.T) {
	e := newSkillEnv(t)
	useNativeTranslator(t, e.s, nil)
	list, err := e.s.ListTranslators(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, TranslatorInfo{}, list[0], "the model comes first")
	assert.Equal(t, TranslatorInfo{ID: "plugin/dlx", Name: "dlx", Native: true, Running: true}, list[1])
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), "dlx", false))
	list, _ = e.s.ListTranslators(t.Context())
	assert.False(t, list[1].Running)
}

func TestNativeEnvProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, map[string]any{"recordEnv": true})
	var got struct{ Env []string }
	b, err := os.ReadFile(record)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &got))
	found := slices.ContainsFunc(got.Env, func(kv string) bool { // Windows may report the name in another case
		k, v, _ := strings.Cut(kv, "=")
		return strings.EqualFold(k, "HTTPS_PROXY") && v == "http://proxy.example:3128"
	})
	assert.True(t, found, "%v", got.Env)
}

func pluginInfoByName(t *testing.T, s *Service, name string) PluginInfo {
	t.Helper()
	list, err := s.ListPlugins(t.Context())
	require.NoError(t, err)
	for _, p := range list {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no plugin %s", name)
	return PluginInfo{}
}

func TestScriptPluginTranslate(t *testing.T) {
	e := newSkillEnv(t)
	src, err := filepath.Abs(filepath.Join("coretest", "testdata", "translate-demo"))
	require.NoError(t, err)
	_, err = e.s.InstallPlugin(t.Context(), src)
	require.NoError(t, err)
	require.NoError(t, e.s.GrantPermission(t.Context(), "translate-demo"))
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), "translate-demo", true))
	st, _ := e.s.GetSettings(t.Context())
	st.Translator = "plugin/translate-demo"
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	setBody(t, e, "Hello\n\nSee you", "")
	evs := e.run(t, "translate", Target{Kind: "message", ID: e.msg})
	assert.Equal(t, "plugin/translate-demo", evs[0].Model)
	assert.Equal(t, []string{"HELLO\n\nSEE YOU"}, texts(evs))
	held, err := e.s.holdsMail(t.Context(), "translate-demo")
	require.NoError(t, err)
	assert.True(t, held, "the plugin was handed mail content")
	list, err := e.s.ListTranslators(t.Context())
	require.NoError(t, err)
	assert.Contains(t, list, TranslatorInfo{ID: "plugin/translate-demo", Name: "translate-demo", Running: true})
}

// A translate plugin may register more than a translator.
func TestScriptPluginTranslatesAndHasTools(t *testing.T) {
	e := newSkillEnv(t)
	st, _ := e.s.GetSettings(t.Context())
	st.Translator = "plugin/tooly"
	_, err := e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "tooly")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"tooly","version":"0.1.0","main":"index.ts","capabilities":["translate"],"permissions":{}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(`export default function (ctx: any) {
		ctx.tools.register({ name: "ping", description: "d", schema: { type: "object" }, write: false, run: () => "pong" })
		ctx.translators.register({ translate: (req: any) => ({ texts: req.texts.map((t: string) => t + "!"), source: "en" }) })
	}`), 0o600))
	_, err = e.s.InstallPlugin(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, e.s.GrantPermission(t.Context(), "tooly"))
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), "tooly", true))
	assert.Len(t, e.s.running["tooly"].p.Tools(), 1)
	evs := e.run(t, "translate", Target{Kind: "selection", ID: e.msg, Text: "Hi"})
	assert.Equal(t, []string{"Hi!"}, texts(evs))
}

// Changing the plugin's settings may change its translations: no stale cache.
func TestPluginTranslateCacheFollowsConfig(t *testing.T) {
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	tg := Target{Kind: "message", ID: e.msg}
	e.run(t, "translate", tg)
	require.NoError(t, e.s.SetPluginConfig(t.Context(), "dlx", map[string]json.RawMessage{"engine": json.RawMessage(`"formal"`)}))
	e.run(t, "translate", tg)
	assert.Len(t, translateRequests(t, record), 2, "translated again after a settings change")
}

func shortIdle(t *testing.T) {
	old := nativeIdle
	nativeIdle = 200 * time.Millisecond
	t.Cleanup(func() { nativeIdle = old })
}

// After an idle exit the plugin's next process gets its current settings first.
func TestNativeReconfiguredAfterRestart(t *testing.T) {
	shortIdle(t)
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	require.NoError(t, e.s.SetPluginConfig(t.Context(), "dlx", map[string]json.RawMessage{"engine": json.RawMessage(`"formal"`)}))
	require.NoError(t, os.Remove(record))
	time.Sleep(700 * time.Millisecond)
	e.run(t, "translate", Target{Kind: "selection", ID: e.msg, Text: "Hi"})
	b, err := os.ReadFile(record)
	require.NoError(t, err, "configured again")
	assert.Contains(t, string(b), `"formal"`)
}

// A restarted process must describe itself as the manifest says before it gets any settings.
func TestNativeRestartChecksDescribe(t *testing.T) {
	shortIdle(t)
	e := newSkillEnv(t)
	record := useNativeTranslator(t, e.s, map[string]any{"restartDescribe": map[string]any{"name": "dlx", "version": "9.9.9", "abi": 1, "capabilities": []string{"translate"}}})
	require.NoError(t, os.Remove(record))
	time.Sleep(700 * time.Millisecond)
	_, err := e.s.RunSkill(t.Context(), "translate", Target{Kind: "selection", ID: e.msg, Text: "Hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "describes itself")
	assert.NoFileExists(t, record, "no settings for a library that is not the one allowed")
	for range 2 { // each call starts another process that fails the same check
		_, err = e.s.RunSkill(t.Context(), "translate", Target{Kind: "selection", ID: e.msg, Text: "Hi"})
		require.Error(t, err)
	}
	p := pluginInfoByName(t, e.s, "dlx")
	assert.False(t, p.Enabled, "the third failed restart disables it")
	assert.Equal(t, disabledFailures, p.DisabledReason)
}

// translateDraft runs translate-draft and returns its one update_draft proposal.
func translateDraft(t *testing.T, e skillEnv, d Draft, opts SkillOptions) Approval {
	t.Helper()
	ch, err := e.s.AssistSkill(t.Context(), d.ID, "translate-draft", opts)
	require.NoError(t, err)
	evs := drainWit(t, ch)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	require.Equal(t, "update_draft", last.Approvals[0].Tool)
	return last.Approvals[0]
}

func TestTranslateDraftWithPlugin(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, nil)
	d := e.replyDraft(t, `<p>Hello <a href="https://x.example">there</a></p><ul><li>One</li><li>Two</li></ul>`)
	_, q, sig := parts(t, d.HTML)
	a := translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"})
	assert.Equal(t, d.HTML, e.getDraft(t, d.ID).HTML, "a proposal, not yet applied")
	e.accept(t, a)
	ed, q2, sig2 := parts(t, e.getDraft(t, d.ID).HTML)
	assert.Contains(t, ed, "[fr] Hello there", "a block of mixed styles becomes plain text")
	assert.Contains(t, ed, "<li>[fr] One</li>")
	assert.Contains(t, ed, "<li>[fr] Two</li>")
	assert.Equal(t, q, q2)
	assert.Equal(t, sig, sig2)
	assert.Empty(t, e.srv.Requests(), "no model was asked")

	d = e.replyDraft(t, `<p><a href="https://x.example/docs">Docs</a></p>`)
	e.accept(t, translateDraft(t, e, d, SkillOptions{}))
	ed, _, _ = parts(t, e.getDraft(t, d.ID).HTML)
	assert.Contains(t, ed, `href="https://x.example/docs"`, "a block in one style keeps it")
	assert.Contains(t, ed, "[zh-CN] Docs", "no target: the reading language")
	assert.Equal(t, d.Subject, e.getDraft(t, d.ID).Subject, "a reply's subject is not translated")

	d = e.newDraft(t, "Fish <b> & Chips", "<p>Hello</p>")
	e.accept(t, translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"}))
	got := e.getDraft(t, d.ID)
	assert.Equal(t, "[fr] Fish <b> & Chips", got.Subject, "a new draft's subject is translated, as text")
	ed, _, _ = parts(t, got.HTML)
	assert.Equal(t, "<p>[fr] Hello</p>", ed)
}

// A draft changed after the proposal is not overwritten by it.
func TestTranslateDraftStaleWithPlugin(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, nil)
	d := e.replyDraft(t, `<p>One</p><p>Two</p>`)
	a := translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"})
	d = e.getDraft(t, d.ID)
	d.HTML = strings.Replace(d.HTML, "One", "Uno", 1)
	_, err := e.s.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	assert.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrStaleDraft)
	ed, _, _ := parts(t, e.getDraft(t, d.ID).HTML)
	assert.Contains(t, ed, "Uno")
	assert.NotContains(t, ed, "[fr]")
}

func TestTranslateDraftPluginUnavailable(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, nil)
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), "dlx", false))
	d := e.replyDraft(t, `<p>Hi</p>`)
	_, err := e.s.AssistSkill(t.Context(), d.ID, "translate-draft", SkillOptions{})
	assert.ErrorIs(t, err, ErrTranslatorUnavailable)
	assert.Empty(t, e.srv.Requests())
}

// Godwit's editor keeps a list item open for its nested list: the item's own
// text is translated too, as is text beside blocks.
func TestTranslateDraftNestedBlocks(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, nil)
	d := e.replyDraft(t, `<ul><li>Parent<ul><li>Child</li></ul></li></ul><div>Lead<p>Inner</p></div>`)
	e.accept(t, translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"}))
	ed, _, _ := parts(t, e.getDraft(t, d.ID).HTML)
	for _, want := range []string{"[fr] Parent", "[fr] Child", "[fr] Lead", "[fr] Inner"} {
		assert.Contains(t, ed, want)
	}
	assert.Contains(t, ed, "<ul><li>[fr] Child</li></ul>", "the nested list stays in its item")
}

// The audit log shows that the plugin got the draft.
func TestTranslateDraftAudited(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, nil)
	d := e.replyDraft(t, `<p>Hi</p>`)
	translateDraft(t, e, d, SkillOptions{})
	rows, err := e.s.db.Query(t.Context(), "SELECT target_kind, target_id FROM audit_log WHERE extension = 'dlx' AND tool = 'plugin:dlx/translate'")
	require.NoError(t, err)
	require.Len(t, rows.Values, 1)
	assert.Equal(t, "draft", rows.Values[0][0])
}

// A plugin model ("plugin/p/m1") translating is a model run, not a translator
// plugin: no translator row is audited for it.
func TestTranslateDraftByPluginModelNotAuditedAsTranslator(t *testing.T) {
	e := newAssistEnv(t)
	srv, host := modelServer(t, e.s, "openai")
	startModelPlugin(t, e.s, "p", host, httpModelPlugin("openai-completions", host))
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.DefaultModel = "plugin/p/m1"
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	srv.Script(testllm.Turn{Text: []string{"<p>Bonjour</p>"}})
	d := e.replyDraft(t, `<p>Hi</p>`)
	translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"})
	rows, err := e.s.db.Query(t.Context(), "SELECT tool FROM audit_log WHERE tool LIKE '%/translate'")
	require.NoError(t, err)
	assert.Empty(t, rows.Values)
}

func TestTranslateDraftHTMLKeepsImagesAndCode(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, nil)
	for src, want := range map[string][]string{
		`<p>Hello<img src="cid:x"></p>`:                    {"[fr] Hello", `<img src="cid:x"/>`},
		`<p><code>npm install foo</code></p>`:              {`<p><code>npm install foo</code></p>`},
		`<p>Run <code>npm i</code> now</p>`:                {"<p>[fr] Run <code>npm i</code> [fr] now</p>"},
		`<p>before<img src="cid:x">after</p>`:              {`<p>[fr] before<img src="cid:x"/>[fr] after</p>`},
		`<p>One<br>Two</p>`:                                {"[fr] One<br/>Two"},
		`<p><a href="https://x.example/docs">Docs</a></p>`: {`<a href="https://x.example/docs">[fr] Docs</a>`},
	} {
		got, err := e.s.translateDraftHTML(t.Context(), "dlx", src, "fr")
		require.NoError(t, err, src)
		for _, w := range want {
			assert.Contains(t, got, w, src)
		}
		assert.NotContains(t, got, "[fr] npm", src)
	}
}

// 脱敏 at a translation plugin (docs/adr/0025-mask-at-cloud-egress.md, M3):
// at each of the four calls the plugin gets the card as a placeholder, and
// the reader, the cache and the draft get the card.
func TestPluginTranslateMasks(t *testing.T) {
	const card = "4111 1111 1111 1111"
	e := newAssistEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	assertMasked := func(what string) {
		t.Helper()
		req := translateRequests(t, record)
		require.NotEmpty(t, req)
		got := fmt.Sprint(req[len(req)-1]["texts"])
		assert.NotContains(t, got, card, what)
		assert.Contains(t, got, "{{BANK_CARD_", what)
	}

	setBody(t, e, "Card "+card+"\n\nSee you", "")
	tg := Target{Kind: "message", ID: e.msg}
	want := []string{"[zh-CN] Card " + card + "\n\n[zh-CN] See you"}
	assert.Equal(t, want, texts(e.run(t, "translate", tg)), "the reading pane")
	assertMasked("the reading pane")
	assert.Equal(t, want, texts(e.run(t, "translate", tg)), "cached with the card")

	sel := Target{Kind: "selection", ID: e.msg, Text: "Pay with " + card}
	assert.Equal(t, []string{"[zh-CN] Pay with " + card}, texts(e.run(t, "translate", sel)), "a selection")
	assertMasked("a selection")

	setBody(t, e, "", "<p>Card "+card+"</p>")
	body := Target{Kind: "message", ID: e.msg, PreserveHTML: true}
	evs := e.run(t, "translate", body)
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
	assert.Equal(t, map[string]string{"Card " + card: "[zh-CN] Card " + card}, evs[1].Translations, "the body")
	assertMasked("the body")
	assert.Equal(t, evs, e.run(t, "translate", body), "cached with the card")

	d := e.newDraft(t, "Card "+card, "<p>Card "+card+"</p>")
	e.accept(t, translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"}))
	got := e.getDraft(t, d.ID)
	assert.Equal(t, "[fr] Card "+card, got.Subject, "the draft's subject")
	assertMasked("the draft's subject")
	ed, _, _ := parts(t, got.HTML)
	assert.Equal(t, "<p>[fr] Card "+card+"</p>", ed, "the draft")
	for _, r := range translateRequests(t, record) {
		assert.NotContains(t, fmt.Sprint(r["texts"]), card)
	}
}

// A value a long text is cut inside is masked before the cut, in the body
// and in a draft, and a placeholder the plugin wrote in capitals restores.
func TestPluginTranslateMasksAcrossACut(t *testing.T) {
	const card = "4111 1111 1111 1111"
	e := newAssistEnv(t)
	record := useNativeTranslator(t, e.s, map[string]any{"translateUpper": true})
	// splitText cuts at the last space before the limit: inside the card.
	long := func(limit int) string { return strings.Repeat("a", limit-10) + " " + card + " b" }

	text := long(8 << 10)
	setBody(t, e, text, "")
	evs := e.run(t, "translate", Target{Kind: "message", ID: e.msg, PreserveHTML: true})
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
	require.Greater(t, len(translateRequests(t, record)[0]["texts"].([]any)), 1, "the text was cut")
	assert.Contains(t, evs[1].Translations[text], card)

	d := e.newDraft(t, "", "<p>"+long(draftPieceBytes)+"</p>")
	e.accept(t, translateDraft(t, e, d, SkillOptions{TargetLanguage: "fr"}))
	assert.Contains(t, e.getDraft(t, d.ID).HTML, card)
	for _, r := range translateRequests(t, record) {
		for _, x := range r["texts"].([]any) {
			assert.NotContains(t, x, "1111", "no piece of the card is sent")
		}
	}
}

// A translation plugin is sent nothing of a request a blocked category
// (拦截) holds: the translation fails naming it.
func TestPluginTranslateBlocks(t *testing.T) {
	e := newAssistEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	setMaskAction(t, e.s, map[string]string{mask.IDs: MaskBlock})
	setBody(t, e, "Card 4111 1111 1111 1111\n\nSee you", "")
	_, err := e.s.RunSkill(t.Context(), "translate", Target{Kind: "message", ID: e.msg})
	assert.Equal(t, []string{mask.IDs}, MaskBlocked(err))
	assert.Empty(t, translateRequests(t, record))
}

// A translation plugin's error that quotes what it was sent shows the
// values, not their placeholders.
func TestPluginTranslateErrorRestores(t *testing.T) {
	e := newAssistEnv(t)
	useNativeTranslator(t, e.s, map[string]any{"translateError": "rate_limited"})
	_, err := e.s.RunSkill(t.Context(), "translate", Target{Kind: "selection", ID: e.msg, Text: "Card 4111 1111 1111 1111"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Card 4111 1111 1111 1111")
	assert.NotContains(t, err.Error(), "{{")
}

// A draft larger than one ABI request is sent in batches, in order.
func TestTranslateDraftHTMLBatches(t *testing.T) {
	e := newAssistEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	block := strings.Repeat("a", 400<<10)
	src := "<p>" + block + "1</p><p>" + block + "2</p><p>" + block + "3</p>"
	got, err := e.s.translateDraftHTML(t.Context(), "dlx", src, "fr")
	require.NoError(t, err)
	plain := strings.ReplaceAll(got, "[fr] ", "") // each piece of a long block comes back prefixed by the fake
	i1, i2, i3 := strings.Index(plain, block+"1"), strings.Index(plain, block+"2"), strings.Index(plain, block+"3")
	assert.True(t, i1 >= 0 && i1 < i2 && i2 < i3, "all three, whole and in order")
	reqs := translateRequests(t, record)
	assert.Greater(t, len(reqs), 1, "more than one request")
	for _, r := range reqs {
		b, _ := json.Marshal(r)
		assert.Less(t, len(b), 1<<20, "each within the ABI's request limit")
	}
}

// One block over the ABI's request limit, and text that grows when JSON-encoded, still fit.
func TestTranslateDraftHTMLSplitsLargeBlocks(t *testing.T) {
	e := newAssistEnv(t)
	record := useNativeTranslator(t, e.s, nil)
	words := strings.Repeat("word ", 300<<10)             // 1.5 MiB in one paragraph
	angles := strings.Repeat("&lt;&lt;&lt;&lt; ", 40<<10) // < encodes as < in JSON: six bytes each
	got, err := e.s.translateDraftHTML(t.Context(), "dlx", "<p>"+words+"</p><p>"+angles+"</p>", "fr")
	require.NoError(t, err)
	assert.Contains(t, got, "word word", "the long paragraph came back")
	reqs := translateRequests(t, record)
	require.Greater(t, len(reqs), 2)
	for _, r := range reqs {
		b, _ := json.Marshal(r)
		assert.Less(t, len(b), 1<<20, "each within the ABI's request limit")
	}
}

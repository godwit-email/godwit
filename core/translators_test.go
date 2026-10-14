package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"dlx","version":"0.1.0","native":{"abi":1},"capabilities":["translate"]}`), 0o600))
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
	assert.Contains(t, got.Env, "HTTPS_PROXY=http://proxy.example:3128")
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

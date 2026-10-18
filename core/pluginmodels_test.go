package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startModelPlugin starts a script plugin declaring models, allowed to reach net.
func startModelPlugin(t *testing.T, s *Service, name, net, code string) {
	t.Helper()
	if s.pluginNet.Resolver == nil {
		s.pluginNet.Resolver = noDNS{}
	}
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := fmt.Sprintf(`{"name":%q,"version":"1.0.0","main":"index.ts","capabilities":["models"],"permissions":{"net":[%q],"llm":true},
		"config":[{"key":"region","type":"text","label":"Region"}]}`, name, net)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(code), 0o600))
	_, err := s.InstallPlugin(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(t.Context(), name))
	require.NoError(t, s.SetPluginEnabled(t.Context(), name, true))
}

// useNativeModels installs the fake native plugin "nm" declaring models,
// with the fake library's spec extra (FakeNativeOpener), and returns its record path.
func useNativeModels(t *testing.T, s *Service, describe, extra map[string]any) string {
	t.Helper()
	s.SetNativeHostForTest("GODWIT_CORETEST_NATIVE=1")
	record := filepath.Join(t.TempDir(), "record.json")
	d := map[string]any{"name": "nm", "version": "0.1.0", "abi": 1, "capabilities": []string{"models"}}
	for k, v := range describe {
		d[k] = v
	}
	lib := map[string]any{"describe": d, "record": record}
	for k, v := range extra {
		lib[k] = v
	}
	dir := filepath.Join(t.TempDir(), "nm")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"nm","version":"0.1.0","native":{"abi":1},"capabilities":["models"]}`), 0o600))
	b, _ := json.Marshal(lib)
	require.NoError(t, os.WriteFile(filepath.Join(dir, plugins.LibraryName()), b, 0o600))
	_, err := s.InstallPlugin(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(t.Context(), "nm"))
	require.NoError(t, s.SetPluginEnabled(t.Context(), "nm", true))
	return record
}

const listingPlugin = `export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "chat", name: "Chat " + (ctx.config.region ?? "us"), transport: "http",
      protocol: "openai-completions", baseURL: "https://api.example.com/v1" }] }),
  })
}`

func TestListPluginModels(t *testing.T) {
	s := newMailContextService(t)
	startModelPlugin(t, s, "lister", "api.example.com", listingPlugin)
	startModelPlugin(t, s, "broken", "api.example.com", `export default function (ctx: any) {
  ctx.models.register({ listModels: () => { throw new Error("no catalog today") } })
}`)
	useNativeModels(t, s, nil, map[string]any{"models": map[string]any{"models": []map[string]any{
		{"id": "judge", "name": "Judge", "transport": "plugin", "operation": "judge", "assets": map[string]string{"weights": "4b"}}}}})

	got, err := s.ListPluginModels(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "broken", got[0].Plugin)
	assert.Contains(t, got[0].Err, "no catalog today", "a failing plugin is reported, not dropped")
	assert.Equal(t, int64(1), pluginFailures(t, s, "broken"), "a failed list counts")
	assert.Equal(t, PluginModels{Plugin: "lister", Models: []plugins.ProviderModel{{ID: "chat", Name: "Chat us", Transport: "http",
		Protocol: "openai-completions", BaseURL: "https://api.example.com/v1"}}}, got[1])
	assert.Equal(t, PluginModels{Plugin: "nm", Models: []plugins.ProviderModel{{ID: "judge", Name: "Judge", Transport: "plugin",
		Operation: "judge", Assets: map[string]string{"weights": "4b"}}}}, got[2])

	require.NoError(t, s.SetPluginConfig(t.Context(), "lister", map[string]json.RawMessage{"region": json.RawMessage(`"eu"`)}))
	got, err = s.ListPluginModels(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Chat eu", got[1].Models[0].Name, "listed again with the new settings")
}

func TestListPluginModelsInvalid(t *testing.T) {
	s := newMailContextService(t)
	startModelPlugin(t, s, "bad", "api.example.com", `export default function (ctx: any) {
  ctx.models.register({ listModels: () => ({ models: [{ id: "x", name: "X", transport: "http", protocol: "openai-completions", baseURL: "http://api.example.com" }] }) })
}`)
	got, err := s.ListPluginModels(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Err, "https", "a script plugin's model must be https")
	assert.Equal(t, int64(1), pluginFailures(t, s, "bad"), "an invalid list counts")
	assert.Empty(t, got[0].Models)
}

// modelServer is a fake model service of kind at api.example.com:<port>, as a script plugin reaches it.
func modelServer(t *testing.T, s *Service, kind string) (*testllm.Server, string) {
	srv := testllm.Start(t, kind)
	u, _ := url.Parse(srv.URL)
	s.pluginNet = plugins.NetOptions{Resolver: mapDNS{"api.example.com": "127.0.0.1"}, AllowHTTP: true, AllowLoopback: true}
	return srv, "api.example.com:" + u.Port()
}

// httpModelPlugin offers model "m1" of protocol at https://<host>, and prepares each
// request: plain http (the fake), a key header, and the upstream model's name.
func httpModelPlugin(protocol, host string) string {
	return fmt.Sprintf(`export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "m1", name: "M1", transport: "http", protocol: %q, baseURL: "https://%s" }] }),
    prepareRequest: (req: any) => ({ url: req.url.replace("https://", "http://"),
      headers: { ...req.headers, "X-Plugin-Key": "secret-" + req.modelId }, body: req.body.replace('"m1"', '"upstream"') }),
  })
}`, protocol, host)
}

func TestPluginHTTPModels(t *testing.T) {
	for protocol, kind := range map[string]string{"openai-completions": "openai", "openai-responses": "openai-responses", "anthropic-message": "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			s := newMailContextService(t)
			srv, host := modelServer(t, s, kind)
			srv.Script(testllm.Turn{Text: []string{"hello there"}})
			startModelPlugin(t, s, "p", host, httpModelPlugin(protocol, host))

			m, ref, err := s.resolveRef(t.Context(), "plugin/p/m1")
			require.NoError(t, err)
			assert.Equal(t, "plugin/p/m1", ref)
			assert.Less(t, m.ServiceID, int64(0))
			e, err := s.aiEngine()
			require.NoError(t, err)
			out, err := s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100)
			require.NoError(t, err)
			assert.Equal(t, "hello there", out)
			reqs := srv.Requests()
			require.Len(t, reqs, 1)
			assert.Equal(t, "upstream", reqs[0].Model, "the body the plugin changed")
			assert.Equal(t, "secret-m1", reqs[0].Header.Get("X-Plugin-Key"))
		})
	}
}

func TestPluginHTTPModelEgress(t *testing.T) {
	s := newMailContextService(t)
	srv, host := modelServer(t, s, "openai")
	srv.Script(testllm.Turn{Text: []string{"never"}})
	s.pluginNet.Resolver = mapDNS{"api.example.com": "127.0.0.1", "elsewhere.example.com": "127.0.0.1"} // reachable, but not declared
	startModelPlugin(t, s, "p", host, fmt.Sprintf(`export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "m1", name: "M1", transport: "http", protocol: "openai-completions", baseURL: "https://%s" }] }),
    prepareRequest: (req: any) => ({ url: req.url.replace("https://api.", "http://elsewhere."), headers: req.headers, body: req.body }),
  })
}`, host))
	m, _, err := s.resolveRef(t.Context(), "plugin/p/m1")
	require.NoError(t, err)
	e, err := s.aiEngine()
	require.NoError(t, err)
	_, err = s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100)
	assert.Error(t, err)
	assert.Empty(t, srv.Requests(), "a host outside permissions.net is never reached")
	rows, err := s.db.Query(t.Context(), "SELECT destination, decision, reason FROM audit_log WHERE tool = 'plugin:p:models'")
	require.NoError(t, err)
	require.NotEmpty(t, rows.Values)
	assert.Equal(t, []any{"elsewhere.example.com:" + strings.Split(host, ":")[1], "deny", "host_not_allowed"}, rows.Values[0])
}

func TestNativePluginHTTPModel(t *testing.T) {
	s := newMailContextService(t)
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"from native"}})
	useNativeModels(t, s, map[string]any{"methods": []string{"models.prepare"}}, map[string]any{"models": map[string]any{"models": []map[string]any{
		{"id": "local", "name": "Local", "transport": "http", "protocol": "openai-completions", "baseURL": srv.URL}}}})
	m, _, err := s.resolveRef(t.Context(), "plugin/nm/local")
	require.NoError(t, err)
	e, err := s.aiEngine()
	require.NoError(t, err)
	out, err := s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100)
	require.NoError(t, err)
	assert.Equal(t, "from native", out)
	assert.Equal(t, "1", srv.Requests()[0].Header.Get("X-Native"), "models.prepare ran")
}

func TestPluginModelRefs(t *testing.T) {
	s := newMailContextService(t)
	_, host := modelServer(t, s, "openai")
	startModelPlugin(t, s, "p", host, httpModelPlugin("openai-completions", host))
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.DefaultModel, st.SmallModel, st.JudgeService = "gone/model", "plugin/p/m1", "plugin/p/m1"
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	m, ref, err := s.resolveTask(t.Context(), "", true)
	require.NoError(t, err)
	assert.Equal(t, "plugin/p/m1", ref)
	require.NoError(t, s.SetPluginConfig(t.Context(), "p", map[string]json.RawMessage{"region": json.RawMessage(`"eu"`)}))
	m2, _, err := s.resolveTask(t.Context(), "", true)
	require.NoError(t, err)
	assert.NotEqual(t, m.Version, m2.Version, "new settings: cached results of the old ones do not match")
	_, _, _, err = s.resolveJudge(t.Context())
	assert.ErrorIs(t, err, ErrModelNotFound, "a generation model does not judge")

	require.NoError(t, s.SetPluginEnabled(t.Context(), "p", false))
	_, _, err = s.resolveTask(t.Context(), "", true)
	assert.ErrorIs(t, err, ErrModelUnavailable, "a stopped plugin's model does not fall back to the default")
	_, _, _, err = s.resolveJudge(t.Context())
	assert.ErrorIs(t, err, ErrModelUnavailable)

	for _, bad := range []string{"plugin/", "plugin/p", "plugin/Bad Name/m"} {
		st.SmallModel = bad
		_, err = s.SetSettings(t.Context(), st)
		assert.Error(t, err, bad)
	}
	assert.Equal(t, "plugin-2", pickSlug("Plugin", 0, nil, nil), "a service never gets the plugin slug")
}

func TestPluginHTTPJudge(t *testing.T) {
	s := newMailContextService(t)
	var auth atomic.Value
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"q": map[string]any{"type": "noul", "noul": 0.9}}})
	}))
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	host := "api.example.com:" + u.Port()
	s.pluginNet = plugins.NetOptions{Resolver: mapDNS{"api.example.com": "127.0.0.1"}, AllowHTTP: true, AllowLoopback: true}
	startModelPlugin(t, s, "p", host, fmt.Sprintf(`export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "jev", name: "Jev", transport: "http", protocol: "typesafe-systemone", baseURL: "https://%s/v1" }] }),
    prepareRequest: (req: any) => ({ url: req.url.replace("https://", "http://"), headers: { ...req.headers, Authorization: "Bearer plugin" }, body: req.body }),
  })
}`, host))
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.JudgeService = "plugin/p/jev"
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	j, label, gen, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "plugin/p/jev", label)
	assert.False(t, gen)
	got, err := j.Ask(t.Context(), "mail", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "?"}})
	require.NoError(t, err)
	assert.InDelta(t, 0.9, *got[0].Probability, 1e-9)
	assert.Equal(t, "Bearer plugin", auth.Load())

	before := hits.Load()
	require.NoError(t, s.SetPluginConfig(t.Context(), "p", map[string]json.RawMessage{"region": json.RawMessage(`"eu"`)}))
	_, err = j.Ask(t.Context(), "mail", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "?"}})
	assert.Error(t, err)
	assert.Equal(t, before, hits.Load(), "a judge resolved on the old settings sends nothing")
	j, _, _, err = s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "?"}})
	assert.NoError(t, err, "resolved again, it answers")
}

func TestPluginModelWithoutPrepareHoldsMail(t *testing.T) {
	s := newMailContextService(t)
	srv, host := modelServer(t, s, "openai")
	srv.Script(testllm.Turn{Text: []string{"x"}})
	startModelPlugin(t, s, "p", host, fmt.Sprintf(`export default function (ctx: any) {
  ctx.models.register({ listModels: () => ({ models: [{ id: "m1", name: "M1", transport: "http", protocol: "openai-completions", baseURL: "https://%s" }] }) })
}`, host))
	m, _, err := s.resolveRef(t.Context(), "plugin/p/m1")
	require.NoError(t, err)
	e, err := s.aiEngine()
	require.NoError(t, err)
	s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100) // the fake is plain http: it fails, after the request left
	held, err := s.holdsMail(t.Context(), "p")
	require.NoError(t, err)
	assert.True(t, held, "the endpoint the plugin chose got the request")
}

func TestStoppingPluginEndsItsRuns(t *testing.T) {
	for name, change := range map[string]func(*Service) error{
		"stop": func(s *Service) error { return s.SetPluginEnabled(t.Context(), "p", false) },
		"settings": func(s *Service) error {
			return s.SetPluginConfig(t.Context(), "p", map[string]json.RawMessage{"region": json.RawMessage(`"eu"`)})
		},
	} {
		t.Run(name, func(t *testing.T) { testPluginChangeEndsRuns(t, change) })
	}
}

func testPluginChangeEndsRuns(t *testing.T, change func(*Service) error) {
	s := newMailContextService(t)
	srv, host := modelServer(t, s, "openai")
	srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
	startModelPlugin(t, s, "p", host, httpModelPlugin("openai-completions", host))
	m, _, err := s.resolveRef(t.Context(), "plugin/p/m1")
	require.NoError(t, err)
	e, err := s.aiEngine()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100)
		done <- err
	}()
	require.Eventually(t, func() bool { return len(srv.Requests()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, change(s))
	select {
	case err := <-done:
		var ae *ai.Error
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, ai.CodeStale, ae.Code, "the run ends as a removed service's does")
	case <-time.After(5 * time.Second):
		t.Fatal("the run kept going after its plugin stopped")
	}
}

// A client resolved before its plugin stopped sends nothing, even with no
// prepare hook to fail on (classifyBatch reuses one judge for a batch).
func TestStoppedPluginClientRefuses(t *testing.T) {
	s := newMailContextService(t)
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"q": map[string]any{"type": "noul", "noul": 0.9}}})
	}))
	t.Cleanup(ts.Close)
	useNativeModels(t, s, nil, map[string]any{"models": map[string]any{"models": []map[string]any{
		{"id": "jev", "name": "Jev", "transport": "http", "protocol": "typesafe-systemone", "baseURL": ts.URL}}}})
	j, err := s.pluginJudge(t.Context(), "plugin/nm/jev")
	require.NoError(t, err)
	qs := []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "?"}}
	_, err = j.Ask(t.Context(), "mail", qs)
	require.NoError(t, err)
	require.NoError(t, s.SetPluginEnabled(t.Context(), "nm", false))
	_, err = j.Ask(t.Context(), "mail", qs)
	assert.ErrorIs(t, err, ErrModelUnavailable, "said as such, not as could not reach")
	assert.Equal(t, int64(1), hits.Load())
}

// A model request is audited with what happened to it: here the name
// resolves to a loopback address, which the plugin network refuses.
func TestPluginModelRequestAuditedOutcome(t *testing.T) {
	s := newMailContextService(t)
	srv, host := modelServer(t, s, "openai")
	srv.Script(testllm.Turn{Text: []string{"never"}})
	s.pluginNet.AllowLoopback = false
	startModelPlugin(t, s, "p", host, httpModelPlugin("openai-completions", host))
	m, _, err := s.resolveRef(t.Context(), "plugin/p/m1")
	require.NoError(t, err)
	e, err := s.aiEngine()
	require.NoError(t, err)
	_, err = s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100)
	assert.Error(t, err)
	rows, err := s.db.Query(t.Context(), "SELECT destination, decision, reason FROM audit_log WHERE tool = 'plugin:p:models'")
	require.NoError(t, err)
	require.NotEmpty(t, rows.Values)
	assert.Equal(t, []any{host, "deny", "private_address"}, rows.Values[0])
}

func TestPluginPrepareBadURLCounts(t *testing.T) {
	for name, ret := range map[string]string{"url": `{ url: "", headers: req.headers, body: req.body }`, "no body": `{ url: req.url, headers: req.headers }`} {
		t.Run(name, func(t *testing.T) { testPrepareWrongAnswer(t, ret) })
	}
}

func testPrepareWrongAnswer(t *testing.T, ret string) {
	s := newMailContextService(t)
	srv, host := modelServer(t, s, "openai")
	srv.Script(testllm.Turn{Text: []string{"never"}})
	startModelPlugin(t, s, "p", host, fmt.Sprintf(`export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "m1", name: "M1", transport: "http", protocol: "openai-completions", baseURL: "https://%s" }] }),
    prepareRequest: (req: any) => (%s),
  })
}`, host, ret))
	m, _, err := s.resolveRef(t.Context(), "plugin/p/m1")
	require.NoError(t, err)
	e, err := s.aiEngine()
	require.NoError(t, err)
	_, err = s.runText(t.Context(), e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 100)
	assert.Error(t, err)
	assert.GreaterOrEqual(t, pluginFailures(t, s, "p"), int64(1), "a wrong answer counts")
}

// A client made from a listing of other settings than the plugin's now
// (settings saved while it was resolved) refuses.
func TestPluginModelClientOfOldListing(t *testing.T) {
	s := newMailContextService(t)
	srv, host := modelServer(t, s, "openai")
	startModelPlugin(t, s, "p", host, httpModelPlugin("openai-completions", host))
	s.pluginsMu.RLock()
	rp := s.running["p"]
	s.pluginsMu.RUnlock()
	old := &plugins.ConfigSnapshot{}
	c := s.pluginModelClient("p", rp, "m1", old)
	req, _ := http.NewRequestWithContext(t.Context(), "POST", "https://"+host+"/chat/completions", strings.NewReader("{}"))
	_, err := c.Do(req)
	assert.ErrorIs(t, err, ErrModelUnavailable)
	assert.Empty(t, srv.Requests())
}

// A plugin's own llm.complete on its own prepared model fails at once: its
// VM, busy with that call, could not run prepareRequest until it ends.
func TestPluginOwnModelFailsFast(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	srv, host := modelServer(t, s, "openai")
	srv.Script(testllm.Turn{Text: []string{"self answer"}})
	startModelPlugin(t, s, "p", host, fmt.Sprintf(`export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "m1", name: "M1", transport: "http", protocol: "openai-completions", baseURL: "https://%s" }] }),
    prepareRequest: (req: any) => ({ url: req.url.replace("https://", "http://"), headers: req.headers, body: req.body }),
  })
  ctx.actions.register({ id: "ask", label: "Ask", on: ["message"], run: async () => ctx.llm.complete("hi") })
}`, host))
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.DefaultModel = "plugin/p/m1"
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	start := time.Now()
	res, err := s.RunAction(t.Context(), "p/ask", Target{Kind: "message", ID: id}, RunActionOptions{})
	assert.ErrorContains(t, err, "its own model")
	assert.Empty(t, res.Text)
	assert.Less(t, time.Since(start), 10*time.Second, "no wait for the plugin timeout")
}

// A plugin that fails to list its models does not send plugin/<name>/<id>
// to a service that once had the slug plugin and has a model "<name>/<id>".
func TestFailingPluginListNoLegacyFallback(t *testing.T) {
	s := newMailContextService(t)
	legacyService(t, s, 1, "Plugin", "p/m1")
	_, err := s.db.Execute(t.Context(), "UPDATE model_services SET slug = 'plugin' WHERE id = 1")
	require.NoError(t, err)
	upgrade(t, s)
	startModelPlugin(t, s, "p", "api.example.com", `export default function (ctx: any) {
  ctx.models.register({ listModels: () => { throw new Error("catalog down") } })
}`)
	_, _, err = s.resolveRef(t.Context(), "plugin/p/m1")
	assert.ErrorContains(t, err, "catalog down")
	require.NoError(t, s.SetPluginEnabled(t.Context(), "p", false))
	m, _, err := s.resolveRef(t.Context(), "plugin/p/m1")
	require.NoError(t, err, "with no plugin p, the legacy service's model")
	assert.EqualValues(t, 1, m.ServiceID)
}

func TestPluginModelClientShared(t *testing.T) {
	s := newMailContextService(t)
	_, host := modelServer(t, s, "openai")
	startModelPlugin(t, s, "p", host, httpModelPlugin("openai-completions", host))
	s.pluginsMu.RLock()
	rp := s.running["p"]
	s.pluginsMu.RUnlock()
	cfg := rp.cfgKey()
	got := make(chan *http.Client, 8)
	for range 8 {
		go func() { got <- s.pluginModelClient("p", rp, "m1", cfg) }()
	}
	first := <-got
	for range 7 {
		assert.Same(t, first, <-got, "concurrent first resolutions share one client")
	}
}

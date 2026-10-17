package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// Review Focus 1: services an older Godwit saved resolve exactly as before.
func TestModelServicesMigrate(t *testing.T) {
	keyring.MockInit()
	ctx, dir := context.Background(), t.TempDir()
	all := store.Migrations
	caps := slices.IndexFunc(all, func(m []string) bool { return strings.Contains(m[len(m)-1], "CREATE TABLE model_service_status") })
	require.Positive(t, caps)
	store.Migrations = all[:caps] // before this migration, whatever comes after it
	db, err := store.Open(ctx, filepath.Join(dir, "godwit.db"))
	store.Migrations = all
	require.NoError(t, err)
	for _, r := range [][]string{
		{"OpenAI", "openai", "openai-responses", "https://api.openai.com/v1", `["gpt-5"]`},
		{"Anthropic", "anthropic", "anthropic", "https://api.anthropic.com", `["claude"]`}, // stored without /v1
		{"Ollama", "ollama", "openai", "http://localhost:11434/v1", `["llama"]`},
		{"Proxy", "proxy", "anthropic", "https://api.openai.com/v1", `["c"]`}, // a host that doesn't offer that format
	} {
		_, err := db.Execute(ctx, "INSERT INTO model_services(name, slug, kind, base_url, models) VALUES (?, ?, ?, ?, ?)", r[0], r[1], r[2], r[3], r[4])
		require.NoError(t, err)
	}
	// From before slugs: backfilled at startup, its reference rewritten.
	_, err = db.Execute(ctx, "INSERT INTO model_services(name, kind, base_url, models) VALUES ('My Gateway', 'openai', 'http://127.0.0.1:4000', '[\"m\"]')")
	require.NoError(t, err)
	_, err = db.Execute(ctx, `INSERT INTO settings(key, value) VALUES ('app', '{"DefaultModel":"anthropic/claude","SmallModel":"My Gateway/m","SettingsVersion":3}')`)
	require.NoError(t, err)
	db.Close()

	s, err := New(ctx, Options{DataDir: dir})
	require.NoError(t, err)
	defer s.Close()
	list, err := s.ListModelServices(ctx)
	require.NoError(t, err)
	byName := map[string]ModelService{}
	for _, m := range list {
		byName[m.Name] = m
		assert.Empty(t, m.JudgeFormat+m.JudgeModel, "judge starts off")
		assert.Empty(t, m.Status)
	}
	for name, p := range map[string]string{"OpenAI": ProviderOpenAI, "Anthropic": ProviderAnthropic, "Ollama": ProviderOther, "Proxy": ProviderOther, "My Gateway": ProviderOther} {
		assert.Equal(t, p, byName[name].Provider, name)
	}

	an, gw := byName["Anthropic"], byName["My Gateway"]
	m, err := s.resolveModel(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, ai.ResolvedModel{ServiceID: an.ID, Version: 1, Kind: "anthropic", BaseURL: "https://api.anthropic.com", Model: "claude"}, m)
	m, ref, err := s.resolveTask(ctx, "", true)
	require.NoError(t, err)
	assert.Equal(t, ai.ResolvedModel{ServiceID: gw.ID, Version: 1, Kind: "openai", BaseURL: "http://127.0.0.1:4000", Model: "m"}, m)
	assert.Equal(t, "my-gateway/m", ref)
	ref, err = s.TaskModel(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "my-gateway/m", ref)
	m, err = s.resolveModel(ctx, "openai/gpt-5")
	require.NoError(t, err)
	assert.Equal(t, ai.ResolvedModel{ServiceID: byName["OpenAI"].ID, Version: 1, Kind: "openai-responses", BaseURL: "https://api.openai.com/v1", Model: "gpt-5"}, m)

	// Saved again unchanged, an old service keeps its address and key.
	in := an.Input()
	got, err := s.UpdateModelService(ctx, an.ID, in)
	require.NoError(t, err)
	assert.Equal(t, "https://api.anthropic.com", got.BaseURL)
	assert.Equal(t, ProviderAnthropic, got.Provider)
}

func TestModelServiceCapabilities(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	ts := ModelServiceInput{Name: "TypeSafe", Provider: ProviderTypeSafe, BaseURL: "https://api.typesafe.ai/v1/systemone", APIKey: "k",
		JudgeFormat: "systemone", JudgeModel: "jev-latest"}
	ms, err := s.AddModelService(bg, ts)
	require.NoError(t, err, "a judge-only service needs no generate models")
	assert.Equal(t, "", ms.Kind)
	assert.Equal(t, "https://api.typesafe.ai/v1", ms.BaseURL)
	assert.Equal(t, "systemone", ms.JudgeFormat)
	assert.Equal(t, "jev-latest", ms.JudgeModel)
	assert.Empty(t, ms.Models)

	ts.Name, ts.JudgeModel = "TypeSafe", "jev-preview"
	ts.Provider, ts.APIKey = "", ""
	ms, err = s.UpdateModelService(bg, ms.ID, ts)
	require.NoError(t, err, "provider \"\" keeps the stored one")
	assert.Equal(t, ProviderTypeSafe, ms.Provider)
	assert.Equal(t, "jev-preview", ms.JudgeModel)
	assert.True(t, ms.HasKey)

	ts.Provider = ProviderOther
	_, err = s.UpdateModelService(bg, ms.ID, ts)
	assert.ErrorContains(t, err, "provider can't change")

	or := ModelServiceInput{Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", Kind: ai.KindAnthropic, Models: []string{"c"},
		JudgeFormat: "systemone", JudgeModel: "jev-latest"}
	ms, err = s.AddModelService(bg, or)
	require.NoError(t, err)
	assert.Equal(t, ProviderOther, ms.Provider, "inferred from the address")
	assert.Equal(t, "https://openrouter.ai/api/v1", ms.BaseURL)
	or.JudgeFormat = ""
	ms, err = s.UpdateModelService(bg, ms.ID, or)
	require.NoError(t, err)
	assert.Empty(t, ms.JudgeModel, "judge off drops its model")

	ms, err = s.AddModelService(bg, ModelServiceInput{Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Kind: ai.KindResponses, Models: []string{"g"}})
	require.NoError(t, err)
	assert.Equal(t, ProviderOpenAI, ms.Provider)

	for name, c := range map[string]struct {
		in   ModelServiceInput
		want string
	}{
		"nothing on":       {ModelServiceInput{Name: "a", BaseURL: "https://a.com"}, "turn on generate or judge"},
		"gen no models":    {ModelServiceInput{Name: "a", BaseURL: "https://a.com", Kind: "openai"}, "add at least one model"},
		"judge no model":   {ModelServiceInput{Name: "a", BaseURL: "https://a.com", JudgeFormat: "decisions"}, "choose a judge model"},
		"typesafe gen":     {ModelServiceInput{Name: "a", Provider: ProviderTypeSafe, BaseURL: "https://a.com", Kind: "openai", Models: []string{"m"}}, "invalid service kind"},
		"openai systemone": {ModelServiceInput{Name: "a", Provider: ProviderOpenAI, BaseURL: "https://a.com", JudgeFormat: "systemone", JudgeModel: "j"}, "invalid judge format"},
		"anthropic judge":  {ModelServiceInput{Name: "a", Provider: ProviderAnthropic, BaseURL: "https://a.com", JudgeFormat: "decisions", JudgeModel: "j"}, "invalid judge format"},
		"bad provider":     {ModelServiceInput{Name: "a", Provider: "acme", BaseURL: "https://a.com", Kind: "openai", Models: []string{"m"}}, "invalid provider"},
		"bad judge format": {ModelServiceInput{Name: "a", BaseURL: "https://a.com", JudgeFormat: "oracle", JudgeModel: "j"}, "invalid judge format"},
	} {
		_, err := s.AddModelService(bg, c.in)
		assert.ErrorContains(t, err, c.want, name)
	}
}

// A service with generate off stops resolving, as one without that model.
func TestGenOffStopsResolving(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	in := ModelServiceInput{Name: "gw", BaseURL: "https://gw.example.com/v1", Kind: "openai", Models: []string{"m"}}
	ms, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	_, err = s.resolveModel(bg, "gw/m")
	require.NoError(t, err)
	in.Kind, in.JudgeFormat, in.JudgeModel = "", "decisions", "gpt-6-luna"
	_, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	_, err = s.resolveModel(bg, "gw/m")
	assert.ErrorIs(t, err, ErrModelNotFound)
}

func TestSettingsJudgeModel(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	assert.Empty(t, st.JudgeModel, "the default model answers until one is chosen")
	st.JudgeModel = "typesafe"
	_, err = s.SetSettings(bg, st)
	assert.ErrorContains(t, err, "invalid model reference")
	st.JudgeModel = "typesafe/jev-latest"
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	got, err := s.GetSettings(bg)
	require.NoError(t, err)
	assert.Equal(t, "typesafe/jev-latest", got.JudgeModel)
}

// judgeAPI serves System One on srv: yes for every question, or status with
// a body that echoes the key it got and a sentinel.
func judgeAPI(srv *testllm.Server, status *int, gate chan struct{}, arrived chan struct{}) {
	srv.Mux.HandleFunc("POST /systemone", func(w http.ResponseWriter, r *http.Request) {
		if arrived != nil {
			arrived <- struct{}{}
			<-gate
		}
		if status != nil && *status != 0 {
			http.Error(w, "BODY-SENTINEL "+r.Header.Get("Authorization"), *status)
			return
		}
		var in struct{ Questions map[string]any }
		json.NewDecoder(r.Body).Decode(&in)
		answers := map[string]any{}
		for name := range in.Questions {
			answers[name] = map[string]any{"type": "noul", "noul": 0.9}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	})
}

func TestResolveJudge(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	judgeAPI(srv, nil, nil, nil)
	gen, err := s.AddModelService(bg, ModelServiceInput{Name: "gen", BaseURL: srv.URL, Kind: "openai", Models: []string{"m1"}})
	require.NoError(t, err)
	ts, err := s.AddModelService(bg, ModelServiceInput{Name: "ts", Provider: ProviderOther, BaseURL: srv.URL, JudgeFormat: "systemone", JudgeModel: "jev-latest"})
	require.NoError(t, err)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	st.DefaultModel = "gen/m1"
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	q := []ai.Question{{Name: "q", Instructions: "Is it?", Type: ai.Predicate}}

	// Unset: the default model answers.
	srv.Script(testllm.Turn{Text: []string{`{"answers":[{"name":"q","answer":"yes"}]}`}})
	j, label, byGen, err := s.resolveJudge(bg)
	require.NoError(t, err)
	assert.Equal(t, "gen/m1", label)
	assert.True(t, byGen)
	as, err := j.Ask(bg, "state", q)
	require.NoError(t, err)
	assert.Equal(t, 1.0, *as[0].Probability)

	st.JudgeModel = "ts/jev-latest"
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	j, label, byGen, err = s.resolveJudge(bg)
	require.NoError(t, err)
	assert.Equal(t, "ts/jev-latest", label)
	assert.False(t, byGen)
	as, err = j.Ask(bg, "state", q)
	require.NoError(t, err)
	assert.Equal(t, 0.9, *as[0].Probability)

	// An explicit unavailable judge never falls back to the generator.
	_, err = s.UpdateModelService(bg, ts.ID, ModelServiceInput{Name: "ts", BaseURL: srv.URL, Kind: "openai", Models: []string{"x"}})
	require.NoError(t, err)
	_, _, _, err = s.resolveJudge(bg)
	require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
	require.NoError(t, s.RemoveModelService(bg, ts.ID))
	_, _, _, err = s.resolveJudge(bg)
	require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
	st.JudgeModel = ""
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	require.NoError(t, s.RemoveModelService(bg, gen.ID))
	_, _, _, err = s.resolveJudge(bg)
	assert.ErrorIs(t, err, ErrModelServiceNotFound)

}

func statusOf(t *testing.T, s *Service, id int64) map[string]CapStatus {
	t.Helper()
	list, err := s.ListModelServices(t.Context())
	require.NoError(t, err)
	for _, m := range list {
		if m.ID == id {
			return m.Status
		}
	}
	t.Fatalf("service %d gone", id)
	return nil
}

// Review Focus 4: a test's status is stored per capability and dropped when
// what it tested changes.
func TestCapabilityStatus(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	judgeAPI(srv, nil, nil, nil)
	in := ModelServiceInput{Name: "both", BaseURL: srv.URL, Kind: "openai", Models: []string{"m1"}, JudgeFormat: "systemone", JudgeModel: "jev-latest"}
	ms, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	testBoth := func() {
		t.Helper()
		for _, c := range []string{CapGen, CapJudge} {
			st, err := s.TestCapability(bg, ms.ID, ms.Input(), c)
			require.NoError(t, err, c)
			assert.Equal(t, "ok", st.State)
			assert.False(t, st.At.IsZero())
		}
	}
	testBoth()
	got := statusOf(t, s, ms.ID)
	require.Len(t, got, 2)
	assert.Equal(t, "ok", got[CapJudge].State)
	assert.GreaterOrEqual(t, got[CapGen].LatencyMs, 0)
	assert.EqualValues(t, 1, versionOf(t, s, ms.ID), "a status write doesn't bump the version")

	// Unsaved input is tested, not stored.
	other := in
	other.BaseURL = srv.URL + "/v1"
	_, err = s.TestCapability(bg, 0, other, CapJudge)
	assert.Error(t, err, "/v1/systemone isn't served")
	_, err = s.TestCapability(bg, ms.ID, other, CapJudge)
	assert.Error(t, err)
	assert.Equal(t, "ok", statusOf(t, s, ms.ID)[CapJudge].State, "a test of another address isn't this service's")

	// A format change drops only its own capability's status.
	in.Kind = ai.KindResponses
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Equal(t, []string{CapJudge}, keys(ms.Status))
	in.Kind = "openai"
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	testBoth()
	in.JudgeModel = "jev-preview"
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Equal(t, []string{CapGen}, keys(ms.Status))

	// The address, or the key, drops both.
	testBoth()
	in.BaseURL = srv.URL + "/v1"
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Empty(t, ms.Status)
	in.BaseURL = srv.URL
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	testBoth()
	in.APIKey = "new-key"
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Empty(t, ms.Status)
	testBoth()
	in.APIKey = ""
	ms, err = s.UpdateModelService(bg, ms.ID, in) // keeps the key: a rename-like save
	require.NoError(t, err)
	assert.Len(t, ms.Status, 2)

	// Removal takes the status with it.
	require.NoError(t, s.RemoveModelService(bg, ms.ID))
	rows, err := s.db.Query(bg, "SELECT count(*) FROM model_service_status")
	require.NoError(t, err)
	assert.EqualValues(t, 0, store.Int(rows.Values[0][0]))
}

func versionOf(t *testing.T, s *Service, id int64) int64 {
	t.Helper()
	m, err := s.modelServiceBy(t.Context(), "id = ?", id)
	require.NoError(t, err)
	return m.Version
}

func keys(m map[string]CapStatus) []string {
	var out []string
	for _, c := range []string{CapGen, CapJudge} {
		if _, ok := m[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

// A gen test of another first model isn't the saved service's; a recorded
// dialog test lands only on the version it was taken for.
func TestCapabilityStatusModelAndRecord(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "g", BaseURL: srv.URL, Kind: "openai", Models: []string{"m1", "m2"}})
	require.NoError(t, err)
	other := ms.Input()
	other.Models = []string{"m2"}
	_, err = s.TestCapability(bg, ms.ID, other, CapGen)
	require.NoError(t, err)
	assert.Empty(t, statusOf(t, s, ms.ID), "m2 was tested, m1 is saved first")

	ok := CapStatus{State: "ok", LatencyMs: 812, At: time.Now()}
	require.NoError(t, s.RecordCapStatus(bg, ms.ID, ms.Version-1, CapGen, ok))
	assert.Empty(t, statusOf(t, s, ms.ID), "a stale version")
	require.NoError(t, s.RecordCapStatus(bg, ms.ID, ms.Version, CapGen, ok))
	assert.Equal(t, 812, statusOf(t, s, ms.ID)[CapGen].LatencyMs)
	assert.Error(t, s.RecordCapStatus(bg, ms.ID, ms.Version, "x", ok))
	assert.Error(t, s.RecordCapStatus(bg, ms.ID, ms.Version, CapGen, CapStatus{}))
}

// A test that started before an update must not store its result.
func TestCapabilityStatusStale(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	gate, arrived := make(chan struct{}), make(chan struct{})
	judgeAPI(srv, nil, gate, arrived)
	in := ModelServiceInput{Name: "j", Provider: ProviderOther, BaseURL: srv.URL, JudgeFormat: "systemone", JudgeModel: "jev-latest"}
	ms, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	done := make(chan error)
	go func() {
		_, err := s.TestCapability(bg, ms.ID, ms.Input(), CapJudge)
		done <- err
	}()
	<-arrived
	in.Name = "renamed"
	_, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	close(gate)
	require.NoError(t, <-done)
	assert.Empty(t, statusOf(t, s, ms.ID), "the service changed while the test ran")
}

// A generation test that hangs ends at genTestTimeout with code timeout.
func TestCapabilityGenTimeout(t *testing.T) {
	prev := genTestTimeout
	genTestTimeout = 200 * time.Millisecond
	t.Cleanup(func() { genTestTimeout = prev })
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "g", BaseURL: srv.URL, Kind: "openai", Models: []string{"m1"}})
	require.NoError(t, err)
	srv.Script(testllm.Turn{Hang: true})
	st, err := s.TestCapability(bg, ms.ID, ms.Input(), CapGen)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, "timeout", st.Code)
	assert.Equal(t, "timeout", statusOf(t, s, ms.ID)[CapGen].Code)
}

// The status holds codes: never the key, the body or error text.
func TestCapabilityStatusHoldsNoText(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	status := http.StatusUnauthorized
	judgeAPI(srv, &status, nil, nil)
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "j", BaseURL: srv.URL, APIKey: "sekret-key-9", Kind: "openai", Models: []string{"m1"},
		JudgeFormat: "systemone", JudgeModel: "jev-latest"})
	require.NoError(t, err)
	st, err := s.TestCapability(bg, ms.ID, ms.Input(), CapJudge)
	require.ErrorIs(t, err, ai.ErrModelAuth)
	assert.Equal(t, CapStatus{State: "fail", HTTPStatus: 401, Code: "auth", LatencyMs: st.LatencyMs, At: st.At}, st)
	status = http.StatusForbidden
	st, err = s.TestCapability(bg, ms.ID, ms.Input(), CapJudge)
	require.ErrorIs(t, err, ai.ErrModelForbidden)
	assert.Equal(t, "forbidden", st.Code)
	status = 422
	st, _ = s.TestCapability(bg, ms.ID, ms.Input(), CapJudge)
	assert.Equal(t, 422, st.HTTPStatus)

	srv.Script(testllm.Turn{Status: 401})
	st, err = s.TestCapability(bg, ms.ID, ms.Input(), CapGen)
	require.Error(t, err)
	assert.Equal(t, "auth", st.Code)

	b, err := json.Marshal(statusOf(t, s, ms.ID))
	require.NoError(t, err)
	for _, leak := range []string{"sekret-key-9", "BODY-SENTINEL"} {
		assert.NotContains(t, string(b), leak)
		assert.NotContains(t, logs.String(), leak)
	}
	assert.Contains(t, string(b), `"Code":"auth"`)
}

// Probing a judge: other gets the format's list, others list with a key.
func TestCheckJudgeModels(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	srv.SetModels("jev-1", "jev-2")
	srv.RequireKey("k")
	ts := ModelServiceInput{Name: "ts", Provider: ProviderTypeSafe, BaseURL: srv.URL, JudgeFormat: "systemone", KeyAction: KeyClear}
	c, err := s.CheckModelService(bg, 0, ts)
	require.NoError(t, err)
	assert.Equal(t, ServiceCheck{BaseURL: srv.URL, JudgeEndpoint: srv.URL + "/systemone"}, c, "no key: nothing to list yet")
	ts.APIKey, ts.KeyAction = "k", KeyReplace
	c, err = s.CheckModelService(bg, 0, ts)
	require.NoError(t, err)
	assert.Equal(t, []string{"jev-1", "jev-2"}, c.JudgeModels)
	ts.APIKey = "bad"
	_, err = s.CheckModelService(bg, 0, ts)
	assert.ErrorIs(t, err, ErrModelAuth)

	ts.Provider, ts.Kind = ProviderOther, "openai"
	ts.APIKey = "k"
	c, err = s.CheckModelService(bg, 0, ts)
	require.NoError(t, err)
	assert.Equal(t, ai.FormatSystemOne.Models(), c.JudgeModels)
	assert.Equal(t, []string{"jev-1", "jev-2"}, c.Models)
	assert.Equal(t, srv.URL+"/chat/completions", c.Endpoint)
}

func TestJudgeModelMigration(t *testing.T) {
	all := store.Migrations
	idx := slices.IndexFunc(all, func(m []string) bool { return strings.Contains(strings.Join(m, "\n"), "$.JudgeService") })
	require.NotEqual(t, -1, idx)
	for _, tc := range []struct{ old, want string }{
		{`{"JudgeService":"ts"}`, "ts/jev-latest"},
		{`{"JudgeService":"gone"}`, "gone/"},
		{`{"JudgeService":"off"}`, "off/saved"},
		{`{"JudgeService":""}`, ""},
		{`{"JudgeService":"ts","JudgeModel":"new/m"}`, "new/m"},
	} {
		t.Run(tc.old, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "godwit.db")
			store.Migrations = all[:idx]
			db, err := store.Open(bg, path)
			store.Migrations = all
			require.NoError(t, err)
			_, err = db.Execute(bg, `INSERT INTO model_services(name,slug,kind,base_url,models,judge_format,judge_model) VALUES ('ts','ts','','https://example.com','[]','systemone','jev-latest'),('off','off','','https://example.com','[]','','saved')`)
			require.NoError(t, err)
			_, err = db.Execute(bg, `INSERT INTO settings(key,value) VALUES ('app',?)`, tc.old)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			for range 2 {
				db, err = store.Open(bg, path)
				require.NoError(t, err)
				rows, err := db.Query(bg, `SELECT json_extract(value,'$.JudgeModel'),json_type(value,'$.JudgeService') FROM settings WHERE key='app'`)
				require.NoError(t, err)
				require.Equal(t, tc.want, store.Str(rows.Values[0][0]))
				require.Nil(t, rows.Values[0][1])
				require.NoError(t, db.Close())
			}
		})
	}
}
func TestJudgeModelExplicitNoFallback(t *testing.T) {
	s := modelSvc(t)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	st.JudgeModel = "missing/model"
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	_, _, _, err = s.resolveJudge(bg)
	require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
}
func TestPluginJudgeUnavailableKeepsLabels(t *testing.T) {
	require.False(t, unanswerable(errors.Join(ai.ErrJudgeUnavailable, &ai.HTTPError{Status: 400})))
	require.False(t, unanswerable(errors.Join(ai.ErrJudgePlugin, ai.ErrJudgeAnswer)))
}

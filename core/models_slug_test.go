package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"OpenAI": "openai", "Local Gateway!": "local-gateway", "我的 服务": "service",
		"--a__b--": "a-b", "": "service", "gpt4o": "gpt4o",
	} {
		assert.Equal(t, want, slugify(in), in)
	}
}

// legacyService inserts a service as an older Godwit stored it: no slug.
func legacyService(t *testing.T, s *Service, id int64, name string, models ...string) {
	t.Helper()
	_, err := s.db.Execute(t.Context(), "INSERT INTO model_services(id, name, kind, base_url, models) VALUES (?, ?, 'openai', 'https://a.com', ?)",
		id, name, modelsJSON(models))
	require.NoError(t, err)
}

func setRawSettings(t *testing.T, s *Service, v string) {
	t.Helper()
	_, err := s.db.Execute(t.Context(), `INSERT INTO settings(key, value) VALUES ('app', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, v)
	require.NoError(t, err)
}

func slugs(t *testing.T, s *Service) map[int64]any {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT id, slug FROM model_services")
	require.NoError(t, err)
	out := map[int64]any{}
	for _, r := range rows.Values {
		out[store.Int(r[0])] = r[1]
	}
	return out
}

func queryStr(t *testing.T, s *Service, q string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query(t.Context(), q, args...)
	require.NoError(t, err)
	require.NotEmpty(t, rows.Values, q)
	return store.Str(rows.Values[0][0])
}

func TestBackfillSlugCollisions(t *testing.T) {
	s := modelSvc(t)
	legacyService(t, s, 1, "OpenAI", "m1")
	legacyService(t, s, 2, "openai", "m1")
	legacyService(t, s, 3, "我的 服务")
	legacyService(t, s, 4, "他的 服务")
	setRawSettings(t, s, `{"DefaultModel":"openai/m1"}`)
	require.NoError(t, s.backfillSlugs(t.Context()))
	assert.Equal(t, map[int64]any{1: "openai-2", 2: "openai", 3: "service", 4: "service-2"}, slugs(t, s))
	r, err := s.resolveModel(t.Context(), "")
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.ServiceID)
	r, err = s.resolveModel(t.Context(), "OpenAI/m1") // the old name still resolves
	require.NoError(t, err)
	assert.EqualValues(t, 1, r.ServiceID)
}

func TestBackfillPreservesSettingsFields(t *testing.T) {
	s := modelSvc(t)
	legacyService(t, s, 1, "My GW", "m1")
	setRawSettings(t, s, `{"Theme":"dark","DefaultModel":"My GW/m1","FutureField":{"a":[1,2]}}`)
	require.NoError(t, s.backfillSlugs(t.Context()))
	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(queryStr(t, s, "SELECT value FROM settings WHERE key = 'app'")), &got))
	assert.JSONEq(t, `{"Theme":"dark","DefaultModel":"my-gw/m1","FutureField":{"a":[1,2]}}`, string(mustJSON(t, got)))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// seedRefs stores a conversation and a run that reference model, and one
// conversation that references a service that does not exist.
func seedRefs(t *testing.T, s *Service, model string) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO conversations(id, kind, title, model) VALUES (1, 'ask', '', ?)", []any{model}},
		{"INSERT INTO conversations(id, kind, title, model) VALUES (2, 'ask', '', 'ghost/m1')", nil},
		{"INSERT INTO runs(id, conversation_id, state, model, tools, snapshot) VALUES (1, 1, 'done', ?, '[]', '{}')", []any{model}},
		{"INSERT INTO runs(id, conversation_id, state, model, tools, snapshot) VALUES (2, 2, 'done', 'My GWx/m1', '[]', '{}')", nil},
	} {
		_, err := s.db.Execute(t.Context(), q.sql, q.args...)
		require.NoError(t, err)
	}
}

func TestBackfillRewritesRunRefs(t *testing.T) {
	s := modelSvc(t)
	legacyService(t, s, 1, "My GW", "m1")
	seedRefs(t, s, "My GW/org/m1")
	require.NoError(t, s.backfillSlugs(t.Context()))
	assert.Equal(t, "my-gw/org/m1", queryStr(t, s, "SELECT model FROM runs WHERE id = 1"))
	assert.Equal(t, "my-gw/org/m1", queryStr(t, s, "SELECT model FROM conversations WHERE id = 1"))
	assert.Equal(t, "My GWx/m1", queryStr(t, s, "SELECT model FROM runs WHERE id = 2"), "only an exact service part is rewritten")
	assert.Equal(t, "ghost/m1", queryStr(t, s, "SELECT model FROM conversations WHERE id = 2"))
}

func dump(t *testing.T, s *Service) []any {
	return []any{slugs(t, s), queryStr(t, s, "SELECT value FROM settings WHERE key = 'app'"),
		queryStr(t, s, "SELECT group_concat(model, '|') FROM runs"), queryStr(t, s, "SELECT group_concat(model, '|') FROM conversations")}
}

func TestBackfillIdempotent(t *testing.T) {
	s := modelSvc(t)
	legacyService(t, s, 1, "My GW", "m1")
	legacyService(t, s, 2, "my-gw-2", "m1") // a name that is a slug stays as it is
	setRawSettings(t, s, `{"DefaultModel":"My GW/m1"}`)
	seedRefs(t, s, "My GW/m1")
	require.NoError(t, s.backfillSlugs(t.Context()))
	first := dump(t, s)
	assert.Equal(t, map[int64]any{1: "my-gw", 2: "my-gw-2"}, first[0])
	require.NoError(t, s.backfillSlugs(t.Context()))
	assert.Equal(t, first, dump(t, s))

	dir := s.dataDir
	require.NoError(t, s.Close())
	s2, err := New(t.Context(), Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s2.Close() })
	assert.Equal(t, first, dump(t, s2))
}

func TestBackfillRollsBack(t *testing.T) {
	s := modelSvc(t)
	legacyService(t, s, 1, "My GW", "m1")
	setRawSettings(t, s, `{"DefaultModel":"My GW/m1"}`)
	seedRefs(t, s, "My GW/m1")
	// The last statement (the conversations rewrite) fails.
	_, err := s.db.Execute(t.Context(), `CREATE TRIGGER fail_conv BEFORE UPDATE OF model ON conversations BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	require.NoError(t, err)
	before := dump(t, s)
	require.ErrorContains(t, s.backfillSlugs(t.Context()), "boom")
	assert.Equal(t, before, dump(t, s))
	assert.Equal(t, map[int64]any{1: nil}, before[0])
}

func TestAddModelServiceSlug(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	in := ModelServiceInput{Name: "OpenAI", Kind: "openai", BaseURL: "https://a.com", Models: []string{"m"}}
	a, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	assert.Equal(t, "openai", a.Slug)
	in.Name = "OPENAI"
	b, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	assert.Equal(t, "openai-2", b.Slug)
	in.Name = "openai" // another service's reference name
	_, err = s.AddModelService(bg, in)
	assert.Error(t, err)
	list, err := s.ListModelServices(bg)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestRenameKeepsSlug(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	in := ModelServiceInput{Name: "My GW", Kind: "openai", BaseURL: "https://a.com", Models: []string{"m"}}
	ms, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	assert.Equal(t, "my-gw", ms.Slug)
	in.Name = "Renamed"
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Equal(t, "my-gw", ms.Slug)
	assert.Equal(t, "Renamed", ms.Name)
	r, err := s.resolveModel(bg, "my-gw/m")
	require.NoError(t, err)
	assert.Equal(t, ms.ID, r.ServiceID)
	in.Name = "my-gw" // its own slug is fine
	_, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
}

func TestRenameRejectsOtherSlug(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	in := ModelServiceInput{Name: "Foo Bar", Kind: "openai", BaseURL: "https://a.com"}
	_, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	in.Name = "b"
	b, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	in.Name = "foo-bar"
	_, err = s.UpdateModelService(bg, b.ID, in)
	require.ErrorContains(t, err, "reference name")
	got, err := s.modelServiceBy(bg, "id = ?", b.ID)
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name)
	assert.EqualValues(t, 1, got.Version)
}

func TestResolveLegacyName(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	a, err := s.AddModelService(bg, ModelServiceInput{Name: "My GW", Kind: "openai", BaseURL: "https://a.com", Models: []string{"m"}})
	require.NoError(t, err)
	r, err := s.resolveModel(bg, "My GW/m")
	require.NoError(t, err)
	assert.Equal(t, a.ID, r.ServiceID)
	_, ref, err := s.resolveRef(bg, "My GW/m")
	require.NoError(t, err)
	assert.Equal(t, "my-gw/m", ref, "the canonical reference uses the slug")

	// A slug match wins over a name match.
	b, err := s.AddModelService(bg, ModelServiceInput{Name: "other", Kind: "openai", BaseURL: "https://b.com", Models: []string{"m"}})
	require.NoError(t, err)
	_, err = s.db.Execute(bg, "UPDATE model_services SET slug = 'My GW' WHERE id = ?", b.ID)
	require.NoError(t, err)
	r, err = s.resolveModel(bg, "My GW/m")
	require.NoError(t, err)
	assert.Equal(t, b.ID, r.ServiceID)
}

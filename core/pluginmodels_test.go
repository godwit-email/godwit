package core_test

import (
	"context"
	"encoding/json"
	"github.com/godwit-email/godwit/core/coretest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/require"
)

func modelPluginFixture(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"`+name+`","version":"1.0.0","main":"index.ts","capabilities":["models"]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(`export default function(ctx){let calls=0;ctx.models.register({listModels(){calls++;`+body+`}})}`), 0600))
	return dir
}
func enableModelPlugin(t *testing.T, s *core.Service, dir, name string) {
	t.Helper()
	_, err := s.InstallPlugin(ctx, dir)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, name))
	require.NoError(t, s.SetPluginEnabled(ctx, name, true))
}

func TestPluginModelsCatalog(t *testing.T) {
	s := newTestService(t)
	dir := modelPluginFixture(t, "provider", `return {models:[{id:"org/a",name:"A",transport:"http",protocol:"openai-completions",baseURL:"https://example.com/v1"}]}`)
	enableModelPlugin(t, s, dir, "provider")
	models, err := s.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "provider/org/a", models[0].Ref)
	require.Equal(t, core.ModelState("ready"), models[0].Status.State)
	require.NoError(t, s.SetPluginEnabled(ctx, "provider", false))
	models, err = s.ListModels(ctx)
	require.NoError(t, err)
	require.Empty(t, models)
}

func TestPluginModelsStableSlug(t *testing.T) {
	s := newTestService(t)
	manual, err := s.AddModelService(ctx, core.ModelServiceInput{Name: "provider", Kind: "openai", BaseURL: "https://example.com/v1", Models: []string{"manual"}})
	require.NoError(t, err)
	dir := modelPluginFixture(t, "provider", `return {models:[{id:"m",name:"M",transport:"http",protocol:"openai-responses",baseURL:"https://example.com/v1"}]}`)
	enableModelPlugin(t, s, dir, "provider")
	list, err := s.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	var ref string
	for _, m := range list {
		if m.Model.ID == "m" {
			ref = m.Ref
		}
	}
	require.Equal(t, "provider-2/m", ref)
	require.NoError(t, s.UninstallPlugin(ctx, "provider"))
	require.NoError(t, s.RemoveModelService(ctx, manual.ID))
	enableModelPlugin(t, s, dir, "provider")
	list, err = s.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, ref, list[0].Ref)
	_, err = s.AddModelService(ctx, core.ModelServiceInput{Name: "provider-2", Kind: "openai", BaseURL: "https://example.com", Models: []string{"x"}})
	require.Error(t, err)
}

func TestPluginModelsCacheAndFailure(t *testing.T) {
	s := newTestService(t)
	dir := modelPluginFixture(t, "provider", `if(calls>1) throw {code:"upstream",message:"offline",retryable:true};return {models:[{id:"m",name:"M",transport:"http",protocol:"openai-responses",baseURL:"https://example.com"}]}`)
	enableModelPlugin(t, s, dir, "provider")
	first, err := s.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, first, 1)
	for range 3 {
		got, err := s.ListModels(ctx)
		require.NoError(t, err)
		require.Equal(t, first, got)
	}
	got, err := s.RefreshModels(ctx)
	require.NoError(t, err)
	require.Equal(t, first, got)
	ps, err := s.ListPlugins(ctx)
	require.NoError(t, err)
	found := false
	for _, p := range ps {
		if p.Name == "provider" {
			require.Equal(t, "upstream", p.ModelsRefreshError)
			found = true
		}
	}
	require.True(t, found)
}

func TestPluginModelsConcurrentRefresh(t *testing.T) {
	s := newTestService(t)
	dir := modelPluginFixture(t, "provider", `return {models:[{id:"m",name:"M",transport:"http",protocol:"openai-responses",baseURL:"https://example.com"}]}`)
	enableModelPlugin(t, s, dir, "provider")
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { m, err := s.ListModels(context.Background()); require.NoError(t, err); require.Len(t, m, 1) })
	}
	wg.Wait()
}

func TestPluginModelsManagedCatalog(t *testing.T) {
	s := nativeService(t, t.TempDir())
	dir := coretest.NativePluginFixture(t, "local", "0.1.0", map[string]any{"name": "local", "version": "0.1.0", "abi": 1, "capabilities": []string{"models"}, "methods": []string{"models.list", "models.load", "models.judge"}}, map[string]any{"models": []map[string]any{{"id": "4b", "name": "4B", "transport": "plugin", "operation": "judge", "assets": map[string]string{"model": "4b-q4"}}}})
	b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	m["capabilities"] = []string{"models"}
	b, err = json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0600))
	enableModelPlugin(t, s, dir, "local")
	models, err := s.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "local/4b", models[0].Ref)
	require.Equal(t, "update_required", models[0].Status.Error.Code)
}

func TestPluginModelsEmptyRefresh(t *testing.T) {
	s := newTestService(t)
	dir := modelPluginFixture(t, "provider", `return {models:calls>1?[]:[{id:"m",name:"M",transport:"http",protocol:"openai-responses",baseURL:"https://example.com"}]}`)
	enableModelPlugin(t, s, dir, "provider")
	first, err := s.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, first, 1)
	got, err := s.RefreshModels(ctx)
	require.NoError(t, err)
	require.Empty(t, got)
}

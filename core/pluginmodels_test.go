package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	m := fmt.Sprintf(`{"name":%q,"version":"1.0.0","main":"index.ts","capabilities":["models"],"permissions":{"net":[%q]},
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
	assert.Empty(t, got[0].Models)
}

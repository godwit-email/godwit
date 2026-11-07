package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useLoginPlugin installs and starts the fake native plugin "lg" with login.
func useLoginPlugin(t *testing.T, s *Service, extra map[string]any) (record string) {
	t.Helper()
	s.SetNativeHostForTest("GODWIT_CORETEST_NATIVE=1")
	record = filepath.Join(t.TempDir(), "record.json")
	lib := map[string]any{"record": record, "models": map[string]any{"models": []any{}},
		"describe": map[string]any{"name": "lg", "version": "0.1.0", "abi": 1, "capabilities": []string{"models", "login"}}}
	for k, v := range extra {
		lib[k] = v
	}
	dir := filepath.Join(t.TempDir(), "lg")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"lg","version":"0.1.0","native":{"abi":1},"capabilities":["models","login"]}`), 0o600))
	b, _ := json.Marshal(lib)
	require.NoError(t, os.WriteFile(filepath.Join(dir, plugins.LibraryName()), b, 0o600))
	_, err := s.InstallPlugin(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(t.Context(), "lg"))
	require.NoError(t, s.SetPluginEnabled(t.Context(), "lg", true))
	return record
}

func TestPluginLoginFlow(t *testing.T) {
	s := newMailContextService(t)
	ctx := t.Context()
	record := useLoginPlugin(t, s, map[string]any{
		"login": map[string]any{
			"login.status": map[string]any{"signedIn": false, "notice": map[string]any{"default": "Risky", "zh-Hans": "有风险"}},
			"login.start":  map[string]any{"flow": "f1"},
		},
		"loginPoll": []any{
			map[string]any{"state": "preparing"},
			map[string]any{"state": "open", "url": "https://accounts.example.com/auth?secret=1"},
			map[string]any{"state": "open", "url": "https://accounts.example.com/auth?secret=1"},
			map[string]any{"state": "done", "account": "a@example.com"},
		},
	})

	st, err := s.PluginLoginStatus(ctx, "lg")
	require.NoError(t, err)
	assert.False(t, st.SignedIn)
	assert.Equal(t, "有风险", st.Notice.For("zh-Hans"))

	flow, err := s.BeginPluginLogin(ctx, "lg")
	require.NoError(t, err)
	assert.Equal(t, "f1", flow)

	s.pluginsMu.RLock()
	rp := s.running["lg"]
	s.pluginsMu.RUnlock()
	rp.models.Store(&modelCatalog{rp.cfgKey(), []plugins.ProviderModel{{ID: "old"}}})

	for _, want := range []string{"preparing", "open", "open", "done"} {
		got, err := s.PollPluginLogin(ctx, "lg", flow)
		require.NoError(t, err)
		assert.Equal(t, want, got.State)
	}
	assert.Nil(t, rp.models.Load(), "the list was for the signed-out account")

	rows, err := s.db.Query(ctx, "SELECT source, destination FROM audit_log WHERE tool = 'plugin:lg:login'")
	require.NoError(t, err)
	require.Len(t, rows.Values, 1, "the page is audited once per flow")
	assert.Equal(t, "user", rows.Values[0][0])
	assert.Contains(t, rows.Values[0][1], "accounts.example.com")
	assert.NotContains(t, rows.Values[0][1], "secret", "the host alone")

	require.NoError(t, s.CancelPluginLogin(ctx, "lg", flow))
	require.NoError(t, s.PluginLogout(ctx, "lg"))
	got, err := os.ReadFile(record + ".login.cancel")
	require.NoError(t, err)
	assert.JSONEq(t, `{"flow":"f1"}`, string(got))
	assert.FileExists(t, record+".login.logout")

	var cfg struct {
		DataDir string `json:"dataDir"`
	}
	b, err := os.ReadFile(record)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &cfg))
	assert.Equal(t, filepath.Join("lg", "data"), filepath.Join(filepath.Base(filepath.Dir(cfg.DataDir)), filepath.Base(cfg.DataDir)))
	assert.DirExists(t, cfg.DataDir)
	assert.True(t, filepath.IsAbs(cfg.DataDir))
}

func TestPluginLoginRefusesWrongAnswers(t *testing.T) {
	s := newMailContextService(t)
	useLoginPlugin(t, s, map[string]any{"loginPoll": []any{map[string]any{"state": "open", "url": "http://evil.example.com/"}}})
	_, err := s.BeginPluginLogin(t.Context(), "lg")
	assert.ErrorIs(t, err, errPluginAnswer, "a flow is required")
	_, err = s.PollPluginLogin(t.Context(), "lg", "f")
	assert.ErrorIs(t, err, errPluginAnswer, "only an https page opens")
}

func TestPluginLoginNeedsTheCapability(t *testing.T) {
	s := newMailContextService(t)
	useNativeModels(t, s, nil, nil)
	_, err := s.PluginLoginStatus(t.Context(), "nm")
	assert.ErrorIs(t, err, ErrPluginNotFound)
	_, err = s.BeginPluginLogin(t.Context(), "gone")
	assert.ErrorIs(t, err, ErrPluginNotFound)
}

// The next process of a plugin listens on another port, so a list naming the
// old one goes when a process ends; a list of plugin-run models stays.
func TestDropLoopbackModels(t *testing.T) {
	rp := &runningPlugin{}
	rp.models.Store(&modelCatalog{nil, []plugins.ProviderModel{{ID: "j", Transport: "plugin"}, {ID: "r", BaseURL: "https://api.example.com/v1"}}})
	rp.dropLoopbackModels()
	assert.NotNil(t, rp.models.Load())
	rp.models.Store(&modelCatalog{nil, []plugins.ProviderModel{{ID: "l", BaseURL: "http://127.0.0.1:4242/tok/v1"}}})
	rp.dropLoopbackModels()
	assert.Nil(t, rp.models.Load())
}

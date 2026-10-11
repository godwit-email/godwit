package core_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

const testConfig = `[
 {"key":"mode","type":"select","label":"Mode","options":[{"value":"a","label":"A"},{"value":"b","label":"B"}],"default":"a"},
 {"key":"apiKey","type":"secret","label":"Key","required":true,"when":{"key":"mode","equals":"b"}},
 {"key":"count","type":"number","label":"N"},
 {"key":"flag","type":"boolean","label":"F","default":false},
 {"key":"host","type":"text","label":"H","required":true},
 {"key":"lang","type":"select","label":"L","options":[{"value":"en","label":"E"}],"allowCustom":true},
 {"key":"token","type":"secret","label":"T"}
]`

// configPlugin installs plugin "cfg" with testConfig.
func configPlugin(t *testing.T, s *core.Service) {
	t.Helper()
	dir := pluginSrc(t, "cfg", "1.0.0", readPerms, `export default function () {}`)
	m := fmt.Sprintf(`{"name":"cfg","version":"1.0.0","main":"index.ts","permissions":%s,"config":%s}`, readPerms, testConfig)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600))
	_, err := s.InstallPlugin(ctx, dir)
	require.NoError(t, err)
}

type raw = map[string]json.RawMessage

func cfgValues(t *testing.T, s *core.Service) map[string]core.ConfigValue {
	t.Helper()
	vs, err := s.GetPluginConfig(ctx, "cfg")
	require.NoError(t, err)
	out := map[string]core.ConfigValue{}
	for _, v := range vs {
		out[v.Key] = v
	}
	return out
}

func TestPluginConfigRoundTrip(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{
		"mode": json.RawMessage(`"b"`), "apiKey": json.RawMessage(`"sk-12345678"`), "count": json.RawMessage(`3.5`),
		"flag": json.RawMessage(`true`), "host": json.RawMessage(`"x.example"`), "lang": json.RawMessage(`"fr"`),
	}))
	got := cfgValues(t, s)
	assert.JSONEq(t, `"b"`, string(got["mode"].Value))
	assert.JSONEq(t, `3.5`, string(got["count"].Value))
	assert.JSONEq(t, `true`, string(got["flag"].Value))
	assert.JSONEq(t, `"fr"`, string(got["lang"].Value))
	assert.True(t, got["apiKey"].Set)
	assert.Empty(t, got["apiKey"].Value, "a secret is never echoed")
	assert.False(t, got["token"].Set)

	cfg, ver, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "b", "apiKey": "sk-12345678", "count": 3.5, "flag": true, "host": "x.example", "lang": "fr"}, cfg)
	assert.EqualValues(t, 2, ver)
	v, err := s.PluginSecretForTest("cfg", "apiKey")
	require.NoError(t, err)
	assert.Equal(t, "sk-12345678", v)
	assert.Contains(t, s.SecretsForTest(), "sk-12345678")

	// Defaults fill unset fields; mode "a" hides apiKey, so its null is ignored
	// and the kept secret is not part of the config.
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "apiKey": json.RawMessage(`null`)}))
	cfg, _, err = s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "a", "flag": false, "host": "h"}, cfg)
	assert.True(t, cfgValues(t, s)["apiKey"].Set)
}

func TestSecretsLoadedAtStart(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	configPlugin(t, s)
	require.NoError(t, s.GrantPermission(ctx, "cfg"))
	require.NoError(t, s.SetPluginEnabled(ctx, "cfg", true))
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-abcdefgh"`)}))
	s.Close()
	s2, err := core.New(ctx, core.Options{DataDir: dir}) // the mock keyring keeps its entries
	require.NoError(t, err)
	defer s2.Close()
	assert.Contains(t, s2.SecretsForTest(), "tok-abcdefgh")
}

func TestPluginConfigValidation(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	bad := []raw{
		{"host": json.RawMessage(`"h"`), "count": json.RawMessage(`"3"`)},
		{"host": json.RawMessage(`"h"`), "count": json.RawMessage(`1e999`)},
		{"host": json.RawMessage(`"h"`), "flag": json.RawMessage(`"true"`)},
		{"host": json.RawMessage(`"h"`), "mode": json.RawMessage(`"c"`)},
		{"host": json.RawMessage(`"h"`), "nope": json.RawMessage(`1`)},
		{"host": json.RawMessage(`5`)},
		{},                                                               // host is required
		{"host": json.RawMessage(`""`)},                                  // still missing
		{"host": json.RawMessage(`null`)},                                // still missing
		{"host": json.RawMessage(`"h"`), "mode": json.RawMessage(`"b"`)}, // apiKey is now visible and required
	}
	for i, v := range bad {
		assert.Error(t, s.SetPluginConfig(ctx, "cfg", v), "case %d", i)
	}
	assert.ErrorIs(t, s.SetPluginConfig(ctx, "missing", raw{}), core.ErrPluginNotFound)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "lang": json.RawMessage(`"custom"`)}))
}

func TestWhenHiddenFieldNotRequired(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	// mode defaults to "a": apiKey is hidden, so neither required nor validated.
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "apiKey": json.RawMessage(`"short"`)}))
	assert.False(t, cfgValues(t, s)["apiKey"].Set)
	cfg, _, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.NotContains(t, cfg, "apiKey")
}

func TestSecretKeepClearAndMinLength(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	base := func(tok string) raw { return raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(tok)} }
	assert.Error(t, s.SetPluginConfig(ctx, "cfg", base(`"1234567"`)))
	assert.Error(t, s.SetPluginConfig(ctx, "cfg", base(`5`)))
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", base(`"12345678"`)))
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", base(`""`)), "empty keeps")
	v, err := s.PluginSecretForTest("cfg", "token")
	require.NoError(t, err)
	assert.Equal(t, "12345678", v)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`)}), "absent keeps")
	assert.True(t, cfgValues(t, s)["token"].Set)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", base(`null`)), "null clears")
	_, err = s.PluginSecretForTest("cfg", "token")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	assert.False(t, cfgValues(t, s)["token"].Set)
	assert.Equal(t, []string{"12345678"}, s.SecretsForTest(), "redacted until the Service closes")
}

func TestSecretNeverInErrors(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	for _, v := range []raw{
		{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"secret-value-xyz"`), "count": json.RawMessage(`"secret-value-xyz"`)},
		{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"secret-value-xyz"`), "nope": json.RawMessage(`"secret-value-xyz"`)},
		{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"sec"`)},
	} {
		err := s.SetPluginConfig(ctx, "cfg", v)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret-value-xyz")
		assert.NotContains(t, err.Error(), `"sec"`)
	}
	keyring.MockInitWithError(fmt.Errorf("locked"))
	err := s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"secret-value-xyz"`)})
	require.ErrorIs(t, err, core.ErrKeychainUnavailable)
	assert.NotContains(t, err.Error(), "secret-value-xyz")
}

func TestConfigAtomic(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "count": json.RawMessage(`1`), "token": json.RawMessage(`"old-token-1"`)}))
	_, ver, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	// One bad field: nothing else is written, the version stays.
	assert.Error(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"new"`), "count": json.RawMessage(`2`),
		"token": json.RawMessage(`"new-token-2"`), "flag": json.RawMessage(`"x"`)}))
	cfg, ver2, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.Equal(t, ver, ver2)
	assert.Equal(t, "h", cfg["host"])
	assert.Equal(t, 1.0, cfg["count"])
	assert.Equal(t, "old-token-1", cfg["token"])
	assert.NotContains(t, s.SecretsForTest(), "new-token-2")
}

func TestConfigVersionBumps(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	_, v0, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.EqualValues(t, 1, v0)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`)}))
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h2"`)}))
	_, v2, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.EqualValues(t, 3, v2)
	assert.Error(t, s.SetPluginConfig(ctx, "cfg", raw{}))
	_, v3, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.EqualValues(t, 3, v3, "a failed write does not bump")
}

func TestUninstallRemovesSecrets(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-abcdefgh"`)}))
	require.NoError(t, s.UninstallPlugin(ctx, "cfg"))
	_, err := s.PluginSecretForTest("cfg", "token")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	assert.Equal(t, []string{"tok-abcdefgh"}, s.SecretsForTest(), "redacted until the Service closes")
	// Reinstalled, it starts unconfigured.
	configPlugin(t, s)
	assert.False(t, cfgValues(t, s)["token"].Set)
}

func TestUpdateDropsUndeclaredSecrets(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-abcdefgh"`)}))
	dir := pluginSrc(t, "cfg", "2.0.0", readPerms, `export default function () {}`)
	m := fmt.Sprintf(`{"name":"cfg","version":"2.0.0","main":"index.ts","permissions":%s,"config":[{"key":"token","type":"text","label":"T"}]}`, readPerms)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600))
	_, err := s.InstallPlugin(ctx, dir)
	require.NoError(t, err)
	_, err = s.PluginSecretForTest("cfg", "token")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	assert.Equal(t, []string{"tok-abcdefgh"}, s.SecretsForTest(), "redacted until the Service closes")
}

var errLocked = errors.New("locked")

func failAt(ns ...int) func(int) error {
	return func(n int) error {
		if slices.Contains(ns, n) {
			return errLocked
		}
		return nil
	}
}

func TestSecretReadFailureKeepsRedaction(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-abcdefgh"`)}))
	restore := core.KeyringFaultForTest("get", func(int) error { return errLocked })
	defer restore()
	err := s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-new-12345"`)})
	require.ErrorIs(t, err, core.ErrKeychainUnavailable)
	assert.Contains(t, s.SecretsForTest(), "tok-abcdefgh")
	require.Error(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`null`)}))
	assert.Contains(t, s.SecretsForTest(), "tok-abcdefgh")
}

func TestSecretsUnreadableAtStartDisable(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	configPlugin(t, s)
	require.NoError(t, s.GrantPermission(ctx, "cfg"))
	require.NoError(t, s.SetPluginEnabled(ctx, "cfg", true))
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-abcdefgh"`)}))
	s.Close()
	restore := core.KeyringFaultForTest("get", func(int) error { return errLocked })
	defer restore()
	s2, err := core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	defer s2.Close()
	_, running := s2.RunningPluginForTest("cfg")
	assert.False(t, running)
	p, ok := pluginByName(t, s2, "cfg")
	require.True(t, ok)
	assert.False(t, p.Enabled)
	assert.Equal(t, "keychain_unavailable", p.DisabledReason)
}

func TestSecretKeysListedForRetry(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", raw{"host": json.RawMessage(`"h"`), "token": json.RawMessage(`"tok-abcdefgh"`)}))
	keys, err := s.PluginSecretKeysForTest("cfg")
	require.NoError(t, err)
	assert.JSONEq(t, `["token"]`, keys)

	// An update drops the secret; its keychain delete fails, so it stays listed.
	restore := core.KeyringFaultForTest("delete", func(int) error { return errLocked })
	dir := pluginSrc(t, "cfg", "2.0.0", readPerms, `export default function () {}`)
	m := fmt.Sprintf(`{"name":"cfg","version":"2.0.0","main":"index.ts","permissions":%s}`, readPerms)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600))
	_, err = s.InstallPlugin(ctx, dir)
	assert.ErrorIs(t, err, core.ErrKeychainUnavailable)
	restore()
	keys, err = s.PluginSecretKeysForTest("cfg")
	require.NoError(t, err)
	assert.JSONEq(t, `["token"]`, keys)
	_, err = s.PluginSecretForTest("cfg", "token")
	require.NoError(t, err)

	// Uninstall deletes by the list, though the manifest no longer declares it.
	require.NoError(t, s.UninstallPlugin(ctx, "cfg"))
	_, err = s.PluginSecretForTest("cfg", "token")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	assert.Equal(t, []string{"tok-abcdefgh"}, s.SecretsForTest(), "redacted until the Service closes")
}

func TestPartialSecretWriteBumpsVersion(t *testing.T) {
	s := newTestService(t)
	configPlugin(t, s)
	both := func(a, b string) raw {
		return raw{"host": json.RawMessage(`"h"`), "mode": json.RawMessage(`"b"`), "apiKey": json.RawMessage(a), "token": json.RawMessage(b)}
	}
	require.NoError(t, s.SetPluginConfig(ctx, "cfg", both(`"api-old-1"`, `"tok-old-1"`)))
	_, v0, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	// apiKey is written, token fails, putting apiKey back fails too.
	restore := core.KeyringFaultForTest("set", failAt(2, 3))
	err = s.SetPluginConfig(ctx, "cfg", both(`"api-new-2"`, `"tok-new-2"`))
	restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apiKey", "the failed rollback is reported")
	_, v1, err := s.PluginConfigForTest("cfg")
	require.NoError(t, err)
	assert.Greater(t, v1, v0)
	// The keychain may hold either apiKey value: both stay redacted.
	assert.Contains(t, s.SecretsForTest(), "api-old-1")
	assert.Contains(t, s.SecretsForTest(), "api-new-2")
}

package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// nativeService is openService with the test binary as plugin-host.
func nativeService(t *testing.T, dir string) *core.Service {
	t.Helper()
	keyring.MockInit()
	s, err := core.New(context.Background(), core.Options{DataDir: dir})
	require.NoError(t, err)
	s.SetNativeHostForTest(coretest.NativeChildEnv + "=1")
	t.Cleanup(func() { s.Close() })
	return s
}

func nativeInfo(t *testing.T, s *core.Service, name string) core.PluginInfo {
	t.Helper()
	p, ok := pluginByName(t, s, name)
	require.True(t, ok, "plugin %s is listed", name)
	return p
}

// recorded waits for the fake library's configure record and decodes it.
func recorded(t *testing.T, path string) map[string]any {
	t.Helper()
	var got struct{ Config map[string]any }
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(b, &got) == nil
	}, 5*time.Second, 20*time.Millisecond)
	return got.Config
}

func TestNativeNeverStartsUngranted(t *testing.T) {
	s := nativeService(t, t.TempDir())
	record := filepath.Join(t.TempDir(), "configure.json")
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, map[string]any{"record": record}))
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.True(t, p.Native)
	assert.True(t, p.NeedsGrant)
	assert.Equal(t, "", p.Trust)
	assert.False(t, p.Enabled)
	assert.Len(t, p.LibraryHash, 64)
	assert.Equal(t, []string{"translate"}, p.Capabilities)
	assert.ErrorIs(t, s.SetPluginEnabled(ctx, "dlx", true), core.ErrNeedsGrant)
	time.Sleep(200 * time.Millisecond)
	assert.NoFileExists(t, record, "no process started for an ungranted file")
}

func TestNativeGrantEnable(t *testing.T) {
	s := nativeService(t, t.TempDir())
	record := filepath.Join(t.TempDir(), "configure.json")
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, map[string]any{"record": record}))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "dlx"))
	assert.Equal(t, "user", nativeInfo(t, s, "dlx").Trust)
	require.NoError(t, s.SetPluginEnabled(ctx, "dlx", true))
	p := nativeInfo(t, s, "dlx")
	assert.True(t, p.Enabled)
	assert.Equal(t, "", p.DisabledReason)
	cfg := recorded(t, record)
	assert.Equal(t, "https://default.example", cfg["endpoint"])
	assert.NotContains(t, cfg, "token")
}

func TestNativeConfigReachesPlugin(t *testing.T) {
	s := nativeService(t, t.TempDir())
	record := filepath.Join(t.TempDir(), "configure.json")
	installEnabled(t, s, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, map[string]any{"record": record}))
	recorded(t, record)
	require.NoError(t, os.Remove(record))
	require.NoError(t, s.SetPluginConfig(ctx, "dlx", map[string]json.RawMessage{"endpoint": json.RawMessage(`"https://x.example"`), "token": json.RawMessage(`"tok-12345678"`)}))
	cfg := recorded(t, record)
	assert.Equal(t, "https://x.example", cfg["endpoint"])
	assert.Equal(t, "tok-12345678", cfg["token"])
}

func TestNativeFileChangeNeedsRegrant(t *testing.T) {
	dir := t.TempDir()
	s := nativeService(t, dir)
	src := coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil)
	installEnabled(t, s, src)
	libs, err := filepath.Glob(filepath.Join(dir, "plugins", "dlx", "*", plugins.LibraryName()))
	require.NoError(t, err)
	require.Len(t, libs, 1)
	f, err := os.OpenFile(libs[0], os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(" ")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.NotEmpty(t, nativeInfo(t, s, "dlx").Error)
	require.NoError(t, s.Close())

	s = nativeService(t, dir)
	p := nativeInfo(t, s, "dlx")
	assert.False(t, p.Enabled)
	assert.Equal(t, "files_changed", p.DisabledReason)

	// A changed source file is a new file: its grant is not inherited.
	f, err = os.OpenFile(filepath.Join(src, plugins.LibraryName()), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(" ")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	p = nativeInfo(t, s, "dlx")
	assert.True(t, p.NeedsGrant)
	assert.Equal(t, "", p.Trust)
	assert.False(t, p.Enabled)
}

func TestNativeSameFileKeepsGrant(t *testing.T) {
	s := nativeService(t, t.TempDir())
	src := coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil)
	installEnabled(t, s, src)
	_, err := s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.True(t, p.Enabled)
	assert.False(t, p.NeedsGrant)
	assert.Equal(t, "user", p.Trust)
}

func TestNativeReplacesScriptNeedsGrant(t *testing.T) {
	s := nativeService(t, t.TempDir())
	installEnabled(t, s, pluginSrc(t, "dlx", "0.0.1", `{}`, `export default function () {}`))
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil))
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.True(t, p.Native)
	assert.True(t, p.NeedsGrant)
	assert.False(t, p.Enabled)
}

func TestNativeDescribeMismatchDisables(t *testing.T) {
	s := nativeService(t, t.TempDir())
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0",
		map[string]any{"name": "dlx", "version": "9.9.9", "abi": 1, "capabilities": []string{"translate"}}, nil))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "dlx"))
	err = s.SetPluginEnabled(ctx, "dlx", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "describes itself")
	assert.False(t, nativeInfo(t, s, "dlx").Enabled)
}

func TestNativeErrorRedactsSecret(t *testing.T) {
	s := nativeService(t, t.TempDir())
	const secret = "tok-SECRET-123"
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, map[string]any{"initFail": true, "echoStderr": "init failed, token " + secret}))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "dlx"))
	require.NoError(t, s.SetPluginConfig(ctx, "dlx", map[string]json.RawMessage{"token": json.RawMessage(`"` + secret + `"`)}))
	err = s.SetPluginEnabled(ctx, "dlx", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
	assert.NotContains(t, err.Error(), secret)
}

func TestNativeUninstall(t *testing.T) {
	dir := t.TempDir()
	s := nativeService(t, dir)
	installEnabled(t, s, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil))
	require.NoError(t, s.UninstallPlugin(ctx, "dlx"))
	assert.NoDirExists(t, filepath.Join(dir, "plugins", "dlx"))
	_, ok := pluginByName(t, s, "dlx")
	assert.False(t, ok)
}

func TestNativeTooLarge(t *testing.T) {
	s := nativeService(t, t.TempDir())
	src := coretest.NativePluginFixture(t, "big", "0.1.0", nil, nil)
	require.NoError(t, os.WriteFile(filepath.Join(src, "data.bin"), make([]byte, 65<<20), 0o600))
	_, err := s.InstallPlugin(ctx, src)
	assert.ErrorContains(t, err, "data.bin")

	script := pluginSrc(t, "fat", "0.0.1", `{}`, `export default function () {}`)
	require.NoError(t, os.WriteFile(filepath.Join(script, "data.bin"), make([]byte, 6<<20), 0o600))
	_, err = s.InstallPlugin(ctx, script)
	assert.ErrorContains(t, err, "data.bin", "a script plugin keeps the 5 MB limit")
	assert.True(t, strings.Contains(err.Error(), "5 MB"), err.Error())
}

// A library whose describe never answers fails to start within the limit, and is not left running.
func TestNativeDescribeHangFails(t *testing.T) {
	core.SetNativeStartForTest(t, 500*time.Millisecond)
	s := nativeService(t, t.TempDir())
	record := filepath.Join(t.TempDir(), "configure.json")
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, map[string]any{"describeSleepMs": 60000, "record": record}))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "dlx"))
	start := time.Now()
	err = s.SetPluginEnabled(ctx, "dlx", true)
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "start limit plus the close grace")
	assert.False(t, nativeInfo(t, s, "dlx").Enabled)
	list, err := s.ListPlugins(ctx) // the lock is free again
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.NoFileExists(t, record, "never configured")
}

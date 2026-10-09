package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

const readPerms = `{"mail":["read"]}`

// pluginSrc writes a plugin source directory and returns it.
func pluginSrc(t *testing.T, name, version, perms, code string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src-"+name) // the source directory's name does not matter
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.ts","permissions":%s}`, name, version, perms)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(code), 0o600))
	return dir
}

func openService(t *testing.T, dir string) *core.Service {
	t.Helper()
	keyring.MockInit()
	s, err := core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func pluginByName(t *testing.T, s *core.Service, name string) (core.PluginInfo, bool) {
	t.Helper()
	list, err := s.ListPlugins(ctx)
	require.NoError(t, err)
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return core.PluginInfo{}, false
}

func waitPluginsChanged(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == events.PluginsChanged {
				return
			}
		case <-timeout:
			t.Fatal("no plugins.changed event")
		}
	}
}

func TestInstallListGrantEnable(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	ch, unsub := s.Subscribe()
	defer unsub()

	// A plugin that does not build is not installed.
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "broken", "1.0.0", readPerms, `export default function ( {`))
	require.Error(t, err)
	assert.NoDirExists(t, filepath.Join(dir, "plugins", "broken"))

	info, err := s.InstallPlugin(ctx, pluginSrc(t, "hello", "1.0.0", readPerms, `export default function () {}`))
	require.NoError(t, err)
	waitPluginsChanged(t, ch)
	assert.Equal(t, "hello", info.Name)
	assert.Equal(t, "1.0.0", info.Version)
	assert.Equal(t, plugins.Permissions{Mail: []string{"read"}}, info.Permissions)
	assert.False(t, info.Enabled)
	assert.True(t, info.NeedsGrant)
	assert.FileExists(t, filepath.Join(dir, "plugins", "hello", "index.ts"))

	list, err := s.ListPlugins(ctx)
	require.NoError(t, err)
	assert.Equal(t, []core.PluginInfo{info}, list)

	assert.ErrorIs(t, s.SetPluginEnabled(ctx, "hello", true), core.ErrNeedsGrant)
	assert.ErrorIs(t, s.GrantPermission(ctx, "nope"), core.ErrPluginNotFound)
	require.NoError(t, s.GrantPermission(ctx, "hello"))
	waitPluginsChanged(t, ch)
	require.NoError(t, s.SetPluginEnabled(ctx, "hello", true))
	waitPluginsChanged(t, ch)
	p, _ := pluginByName(t, s, "hello")
	assert.True(t, p.Enabled)
	assert.False(t, p.NeedsGrant)
	assert.Empty(t, p.Error)
	_, running := s.RunningPluginForTest("hello")
	assert.True(t, running)

	require.NoError(t, s.SetPluginEnabled(ctx, "hello", false))
	p, _ = pluginByName(t, s, "hello")
	assert.False(t, p.Enabled)
	_, running = s.RunningPluginForTest("hello")
	assert.False(t, running)

	// Enabled plugins start with the Service.
	require.NoError(t, s.SetPluginEnabled(ctx, "hello", true))
	require.NoError(t, s.Close())
	s = openService(t, dir)
	_, running = s.RunningPluginForTest("hello")
	assert.True(t, running)
}

func TestUpdateWithNewPermissionNeedsRegrant(t *testing.T) {
	s := openService(t, t.TempDir())
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () {}`))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "p"))
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))

	info, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "2.0.0", `{"mail":["read"],"net":["api.example.com"]}`, `export default function () {}`))
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", info.Version)
	assert.False(t, info.Enabled)
	assert.True(t, info.NeedsGrant)
	_, running := s.RunningPluginForTest("p")
	assert.False(t, running)
	assert.ErrorIs(t, s.SetPluginEnabled(ctx, "p", true), core.ErrNeedsGrant)
}

func TestUpdateSamePermissionsReloads(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () { return "one" }`))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "p"))
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))
	h1, _ := s.RunningPluginForTest("p")

	// The same permissions, written differently: still granted.
	info, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.1.0", `{"mail":["read","read"]}`, `export default function () { return "two" }`))
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", info.Version)
	assert.True(t, info.Enabled)
	assert.False(t, info.NeedsGrant)
	h2, running := s.RunningPluginForTest("p")
	assert.True(t, running)
	assert.NotEqual(t, h1, h2, "the new code is loaded")
	b, err := os.ReadFile(filepath.Join(dir, "plugins", "p", "index.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "two")
}

func TestCrashMidUpdateDetected(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	src := pluginSrc(t, "p", "1.0.0", readPerms, `export default function () {}`)
	_, err := s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "p"))
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))
	require.NoError(t, s.Close())

	// The update replaced the files and crashed before writing the row.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugins", "p", "index.ts"), []byte(`export default function () { return 2 }`), 0o600))

	s = openService(t, dir)
	p, ok := pluginByName(t, s, "p")
	require.True(t, ok)
	assert.False(t, p.Enabled)
	assert.NotEmpty(t, p.DisabledReason)
	assert.Contains(t, p.Error, "reinstall")
	_, running := s.RunningPluginForTest("p")
	assert.False(t, running)
	assert.Error(t, s.SetPluginEnabled(ctx, "p", true))

	// Reinstalling repairs it.
	_, err = s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	p, _ = pluginByName(t, s, "p")
	assert.Empty(t, p.Error)
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))
}

func TestBrokenPluginListed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "plugins", "bad"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugins", "bad", "plugin.json"), []byte(`{not json`), 0o600))
	s := openService(t, dir)
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "good", "1.0.0", readPerms, `export default function () {}`))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, "plugins", "good", "plugin.json")))

	for _, n := range []string{"bad", "good"} {
		p, ok := pluginByName(t, s, n)
		require.True(t, ok, n)
		assert.NotEmpty(t, p.Error, n)
		assert.False(t, p.Enabled, n)
		assert.Error(t, s.SetPluginEnabled(ctx, n, true), n)
	}
	require.NoError(t, s.UninstallPlugin(ctx, "bad"))
	assert.NoDirExists(t, filepath.Join(dir, "plugins", "bad"))
}

func TestUninstall(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () {}`))
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, "p"))
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))
	ch, unsub := s.Subscribe()
	defer unsub()

	require.NoError(t, s.UninstallPlugin(ctx, "p"))
	waitPluginsChanged(t, ch)
	list, err := s.ListPlugins(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.NoDirExists(t, filepath.Join(dir, "plugins", "p"))
	_, running := s.RunningPluginForTest("p")
	assert.False(t, running)
	assert.ErrorIs(t, s.UninstallPlugin(ctx, "p"), core.ErrPluginNotFound)
}

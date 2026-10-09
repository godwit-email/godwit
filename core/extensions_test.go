package core_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// versionDirs lists DataDir/plugins/<name>/<hash> directories.
func versionDirs(t *testing.T, dir, name string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "plugins", name, "*"))
	require.NoError(t, err)
	return m
}

func installEnabled(t *testing.T, s *core.Service, src string) {
	t.Helper()
	_, err := s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(ctx, filepath.Base(src)[len("src-"):]))
	require.NoError(t, s.SetPluginEnabled(ctx, filepath.Base(src)[len("src-"):], true))
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
	vs := versionDirs(t, dir, "hello")
	require.Len(t, vs, 1)
	assert.FileExists(t, filepath.Join(vs[0], "index.ts"))
	left, _ := os.ReadDir(filepath.Join(dir, "plugins"))
	assert.Len(t, left, 1, "no staging directories left")

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
	vs := versionDirs(t, dir, "p")
	require.Len(t, vs, 1, "the old version is removed")
	assert.True(t, strings.HasPrefix(filepath.Base(vs[0]), h2+"-"), vs[0])
	b, err := os.ReadFile(filepath.Join(vs[0], "index.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "two")
}

func TestDamagedFilesDetected(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	src := pluginSrc(t, "p", "1.0.0", readPerms, `export default function () {}`)
	installEnabled(t, s, src)
	require.NoError(t, s.Close())

	vs := versionDirs(t, dir, "p")
	require.Len(t, vs, 1)
	require.NoError(t, os.WriteFile(filepath.Join(vs[0], "index.ts"), []byte(`export default function () { return 2 }`), 0o600))

	s = openService(t, dir)
	p, ok := pluginByName(t, s, "p")
	require.True(t, ok)
	assert.False(t, p.Enabled)
	assert.NotEmpty(t, p.DisabledReason)
	assert.Contains(t, p.Error, "reinstall")
	_, running := s.RunningPluginForTest("p")
	assert.False(t, running)
	assert.Error(t, s.SetPluginEnabled(ctx, "p", true))

	// Reinstalling the same files repairs it.
	_, err := s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	p, _ = pluginByName(t, s, "p")
	assert.Empty(t, p.Error)
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))
}

// An update that dies before the row points at the new version leaves the old
// one active and intact; the new directory is removed on the next start.
func TestCrashBeforeSwitch(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	installEnabled(t, s, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () { return "one" }`))
	h1, _ := s.RunningPluginForTest("p")

	restore := core.SetPluginHookForTest(func(stage, name string) error {
		if stage == "switch" {
			return errors.New("crash")
		}
		return nil
	})
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.1.0", readPerms, `export default function () { return "two" }`))
	restore()
	require.Error(t, err)
	h, running := s.RunningPluginForTest("p")
	assert.True(t, running)
	assert.Equal(t, h1, h, "the old version keeps running")
	require.NoError(t, s.Close())

	s = openService(t, dir)
	p, _ := pluginByName(t, s, "p")
	assert.Equal(t, "1.0.0", p.Version)
	assert.True(t, p.Enabled)
	assert.Empty(t, p.Error)
	h, running = s.RunningPluginForTest("p")
	assert.True(t, running)
	assert.Equal(t, h1, h)
	vs := versionDirs(t, dir, "p")
	require.Len(t, vs, 1)
	assert.True(t, strings.HasPrefix(filepath.Base(vs[0]), h1+"-"), vs[0])
}

// An update that dies after the switch is the new version on the next start;
// the old directory is removed then.
func TestCrashAfterSwitch(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	installEnabled(t, s, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () { return "one" }`))
	h1, _ := s.RunningPluginForTest("p")

	restore := core.SetPluginHookForTest(func(stage, name string) error {
		if stage == "switched" {
			return errors.New("crash")
		}
		return nil
	})
	_, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.1.0", readPerms, `export default function () { return "two" }`))
	restore()
	require.Error(t, err)
	require.Len(t, versionDirs(t, dir, "p"), 2)
	require.NoError(t, s.Close())

	s = openService(t, dir)
	p, _ := pluginByName(t, s, "p")
	assert.Equal(t, "1.1.0", p.Version)
	assert.True(t, p.Enabled)
	assert.Empty(t, p.Error)
	h2, running := s.RunningPluginForTest("p")
	assert.True(t, running)
	assert.NotEqual(t, h1, h2)
	vs := versionDirs(t, dir, "p")
	require.Len(t, vs, 1)
	assert.True(t, strings.HasPrefix(filepath.Base(vs[0]), h2+"-"), vs[0])
}

// The row switch failing changes nothing: the old version stays listed and running.
// Reinstalling the same files writes a new directory: the one the row points
// at is never deleted before the switch, even when it cannot be read.
func TestSameHashReinstallNeverDeletesReferenced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not make files unreadable on Windows")
	}
	dir := t.TempDir()
	s := openService(t, dir)
	src := pluginSrc(t, "p", "1.0.0", readPerms, `export default function () {}`)
	installEnabled(t, s, src)
	h1, _ := s.RunningPluginForTest("p")
	vs := versionDirs(t, dir, "p")
	require.Len(t, vs, 1)
	old := vs[0]
	unreadable := filepath.Join(old, "index.ts")
	require.NoError(t, os.Chmod(unreadable, 0))

	// Crash before the switch: the referenced directory is still there.
	restore := core.SetPluginHookForTest(func(stage, name string) error {
		if stage == "switch" {
			fi, err := os.Lstat(unreadable)
			if assert.NoError(t, err, "untouched until the switch") {
				assert.Zero(t, fi.Mode().Perm(), "the old files, not a new copy at the same path")
			}
			return errors.New("crash")
		}
		return nil
	})
	_, err := s.InstallPlugin(ctx, src)
	restore()
	require.Error(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.Chmod(unreadable, 0o600))
	s = openService(t, dir)
	assert.Equal(t, []string{old}, versionDirs(t, dir, "p"), "still referenced, intact; the new one is removed")
	p, _ := pluginByName(t, s, "p")
	assert.True(t, p.Enabled)
	assert.Empty(t, p.Error)

	// A completed reinstall switches to a new directory, then removes the old one.
	require.NoError(t, os.Chmod(unreadable, 0))
	defer os.Chmod(unreadable, 0o600)
	restore = core.SetPluginHookForTest(func(stage, name string) error {
		if stage == "switch" {
			fi, err := os.Lstat(unreadable)
			if assert.NoError(t, err, "untouched until the switch") {
				assert.Zero(t, fi.Mode().Perm(), "the old files, not a new copy at the same path")
			}
		}
		return nil
	})
	defer restore()
	_, err = s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	vs = versionDirs(t, dir, "p")
	require.Len(t, vs, 1)
	assert.NotEqual(t, old, vs[0])
	p, _ = pluginByName(t, s, "p")
	assert.True(t, p.Enabled)
	assert.Empty(t, p.Error)
	h, running := s.RunningPluginForTest("p")
	assert.True(t, running)
	assert.Equal(t, h1, h, "same files")
}

// Enabling a running plugin again leaves it running when the write fails.
func TestReenableDBFailureKeepsRunning(t *testing.T) {
	s := openService(t, t.TempDir())
	installEnabled(t, s, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () {}`))
	require.NoError(t, s.ExecForTest(`CREATE TRIGGER no_update BEFORE UPDATE ON plugins BEGIN SELECT RAISE(ABORT, 'disk full'); END`))
	require.Error(t, s.SetPluginEnabled(ctx, "p", true))
	_, running := s.RunningPluginForTest("p")
	assert.True(t, running)
}

func TestUpdateDBFailureKeepsOld(t *testing.T) {
	s := openService(t, t.TempDir())
	installEnabled(t, s, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () { return "one" }`))
	h1, _ := s.RunningPluginForTest("p")
	require.NoError(t, s.ExecForTest(`CREATE TRIGGER no_update BEFORE UPDATE ON plugins BEGIN SELECT RAISE(ABORT, 'disk full'); END`))

	_, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.1.0", readPerms, `export default function () { return "two" }`))
	require.Error(t, err)
	h, running := s.RunningPluginForTest("p")
	assert.True(t, running)
	assert.Equal(t, h1, h)
	p, _ := pluginByName(t, s, "p")
	assert.Equal(t, "1.0.0", p.Version)
	assert.True(t, p.Enabled)
	assert.Empty(t, p.Error)
}

// A new version that fails to start is recorded as disabled, not left "enabled".
func TestReloadFailureDisables(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	installEnabled(t, s, pluginSrc(t, "p", "1.0.0", readPerms, `export default function () { return "one" }`))
	ch, unsub := s.Subscribe()
	defer unsub()

	restore := core.SetPluginHookForTest(func(stage, name string) error {
		if stage == "load" {
			return errors.New("init threw")
		}
		return nil
	})
	defer restore()
	info, err := s.InstallPlugin(ctx, pluginSrc(t, "p", "1.1.0", readPerms, `export default function () { return "two" }`))
	require.Error(t, err)
	assert.Equal(t, "1.1.0", info.Version)
	assert.False(t, info.Enabled)
	assert.Equal(t, "load_failed", info.DisabledReason)
	waitPluginsChanged(t, ch)
	_, running := s.RunningPluginForTest("p")
	assert.False(t, running)
	assert.Error(t, s.SetPluginEnabled(ctx, "p", true), "still failing")
	restore()

	require.NoError(t, s.Close())
	s = openService(t, dir)
	p, _ := pluginByName(t, s, "p")
	assert.False(t, p.Enabled, "the disabled state is persisted")
	assert.Equal(t, "load_failed", p.DisabledReason)
	require.NoError(t, s.SetPluginEnabled(ctx, "p", true))
}

// ListPlugins never sees an update half done.
func TestListDuringUpdate(t *testing.T) {
	s := openService(t, t.TempDir())
	srcs := []string{
		pluginSrc(t, "p", "1.0.0", readPerms, `export default function () { return "one" }`),
		pluginSrc(t, "p", "1.1.0", readPerms, `export default function () { return "two" }`),
	}
	installEnabled(t, s, srcs[0])
	done := make(chan error)
	go func() {
		for i := range 20 {
			if _, err := s.InstallPlugin(ctx, srcs[i%2]); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return
		default:
		}
		p, ok := pluginByName(t, s, "p")
		require.True(t, ok)
		require.Empty(t, p.Error)
		require.True(t, p.Enabled)
	}
}

func TestBrokenPluginListed(t *testing.T) {
	dir := t.TempDir()
	s := openService(t, dir)
	for _, n := range []string{"bad", "gone"} {
		_, err := s.InstallPlugin(ctx, pluginSrc(t, n, "1.0.0", readPerms, `export default function () {}`))
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(versionDirs(t, dir, "bad")[0], "plugin.json"), []byte(`{not json`), 0o600))
	require.NoError(t, os.Remove(filepath.Join(versionDirs(t, dir, "gone")[0], "plugin.json")))
	require.NoError(t, s.GrantPermission(ctx, "bad"))

	for _, n := range []string{"bad", "gone"} {
		p, ok := pluginByName(t, s, n)
		require.True(t, ok, n)
		assert.Contains(t, p.Error, "reinstall", n)
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

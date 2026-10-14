package core_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

var t0 = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

// regService is a Service with registry rs (nil: none) in dir.
func regService(t *testing.T, rs *coretest.RegistryServer, dir string) *core.Service {
	t.Helper()
	keyring.MockInit()
	opts := core.Options{DataDir: dir, AppVersion: "1.2.0"}
	if rs != nil {
		opts.Registry = rs.Src
	}
	s, err := core.New(context.Background(), opts)
	require.NoError(t, err)
	s.SetNativeHostForTest(coretest.NativeChildEnv + "=1")
	t.Cleanup(func() { s.Close() })
	return s
}

func entry(t *testing.T, s *core.Service, name string) core.RegistryEntry {
	t.Helper()
	v, err := s.ListPluginRegistry(ctx)
	require.NoError(t, err)
	for _, e := range v.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no registry entry %s in %+v", name, v.Entries)
	return core.RegistryEntry{}
}

func plugin(name string, releases ...map[string]any) map[string]any {
	return map[string]any{"name": name, "title": name, "summary": "A test plugin", "releases": releases}
}

func TestRegistryOff(t *testing.T) {
	s := regService(t, nil, t.TempDir())
	assert.ErrorIs(t, s.RefreshPluginRegistry(ctx), core.ErrRegistryOff)
	v, err := s.ListPluginRegistry(ctx)
	require.NoError(t, err)
	assert.False(t, v.On)
}

func TestRegistryList(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	rs.Publish(t, coretest.Registry(t0,
		plugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", nil)),
		plugin("deepl", rs.ScriptRelease("deepl", "0.1.0", `{"net":["api.deepl.com"]}`, `export default function () {}`))))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	v, err := s.ListPluginRegistry(ctx)
	require.NoError(t, err)
	assert.True(t, v.On)
	assert.Equal(t, t0, v.Updated.UTC())
	assert.Equal(t, "", v.Stale)
	require.Len(t, v.Entries, 2)
	d := entry(t, s, "dlx")
	assert.True(t, d.Native)
	assert.Equal(t, "0.1.0", d.Version)
	assert.Equal(t, "", d.Unavailable)
	assert.Equal(t, "", d.Installed)
	assert.False(t, entry(t, s, "deepl").Native)
}

func TestRegistryUnavailablePlatform(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	rel := rs.NativeRelease(t, "dlx", "0.1.0", nil)
	arts := rel["artifacts"].(map[string]any)
	for k, a := range arts { // move this platform's file to another platform's key
		delete(arts, k)
		if k == "windows-arm64" {
			arts["linux-arm64"] = a
		} else {
			arts["windows-arm64"] = a
		}
	}
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", rel)))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	e := entry(t, s, "dlx")
	assert.Equal(t, "", e.Version)
	assert.Equal(t, "platform", e.Unavailable)
}

func TestRegistryRejectsBadSource(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	good := coretest.Registry(t0, plugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", nil)))
	rs.Publish(t, good)
	dir := t.TempDir()
	s := regService(t, rs, dir)
	require.NoError(t, s.RefreshPluginRegistry(ctx))

	stillGood := func(what string) {
		t.Helper()
		v, err := s.ListPluginRegistry(ctx)
		require.NoError(t, err, what)
		require.Len(t, v.Entries, 1, what)
		assert.Equal(t, t0, v.Updated.UTC(), what)
		assert.NotEmpty(t, v.Stale, what)
	}
	rs.Tamper(func(b []byte) []byte { return append(b[:len(b)-2], b[len(b)-1]) })
	assert.ErrorIs(t, s.RefreshPluginRegistry(ctx), core.ErrRegistrySignature)
	stillGood("tampered")

	rs.Publish(t, coretest.Registry(t0.Add(-time.Hour)))
	assert.ErrorIs(t, s.RefreshPluginRegistry(ctx), core.ErrRegistryStale)
	stillGood("older")

	rs.Publish(t, coretest.Registry(t0.Add(time.Hour)))
	rs.Serve("/registry.json.sig", nil)
	assert.Error(t, s.RefreshPluginRegistry(ctx))
	stillGood("no signature")

	// The cache is checked again on every read.
	cache := filepath.Join(dir, "plugin-registry.json")
	b, err := os.ReadFile(cache)
	require.NoError(t, err)
	b[len(b)-2] = ' '
	require.NoError(t, os.WriteFile(cache, b, 0o600))
	_, err = s.ListPluginRegistry(ctx)
	assert.ErrorIs(t, err, core.ErrRegistrySignature)
}

func TestRegistryPromotesUserFile(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	s := regService(t, rs, t.TempDir())
	installEnabled(t, s, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil))
	require.Equal(t, "user", nativeInfo(t, s, "dlx").Trust)
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", nil)))) // the same bytes
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	assert.Equal(t, "listed", nativeInfo(t, s, "dlx").Trust)
}
func TestRegistryRevokes(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	record := filepath.Join(t.TempDir(), "configure.json")
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", map[string]any{"record": record}))))
	dir := t.TempDir()
	s := regService(t, rs, dir)
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	_, err := s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	require.True(t, nativeInfo(t, s, "dlx").Enabled)

	rs.Publish(t, coretest.Registry(t0.Add(time.Hour)))
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	p := nativeInfo(t, s, "dlx")
	assert.False(t, p.Enabled)
	assert.Equal(t, "trust_revoked", p.DisabledReason)
	assert.True(t, p.NeedsGrant)
	assert.Equal(t, "", p.Trust)
	require.NoError(t, s.Close())

	require.NoError(t, os.Remove(record))
	s = regService(t, rs, dir)
	assert.False(t, nativeInfo(t, s, "dlx").Enabled)
	time.Sleep(200 * time.Millisecond)
	assert.NoFileExists(t, record, "a revoked plugin does not start again")
}

func TestInstallFromRegistryNative(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	record := filepath.Join(t.TempDir(), "configure.json")
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", map[string]any{"record": record}))))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	info, err := s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", info.Version)
	p := nativeInfo(t, s, "dlx")
	assert.True(t, p.Native)
	assert.True(t, p.Enabled)
	assert.False(t, p.NeedsGrant)
	assert.Equal(t, "listed", p.Trust)
	recorded(t, record)
	e := entry(t, s, "dlx")
	assert.Equal(t, "0.1.0", e.Installed)
	assert.False(t, e.Update)
}

func TestInstallFromRegistryScript(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	rs.Publish(t, coretest.Registry(t0, plugin("hello", rs.ScriptRelease("hello", "0.1.0", `{"net":["api.example.com"]}`, `export default function () {}`))))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	_, err := s.InstallPluginFromRegistry(ctx, "hello")
	require.NoError(t, err)
	p := nativeInfo(t, s, "hello")
	assert.False(t, p.Native)
	assert.True(t, p.NeedsGrant, "review does not grant a script plugin's permissions")
	assert.Equal(t, "listed", p.Trust)
	assert.False(t, p.Enabled)
	require.NoError(t, s.GrantPermission(ctx, "hello"))
	require.NoError(t, s.SetPluginEnabled(ctx, "hello", true))
	assert.True(t, nativeInfo(t, s, "hello").Enabled)
	assert.Equal(t, "listed", nativeInfo(t, s, "hello").Trust, "granting keeps the review")
}

func TestInstallFromRegistryMismatch(t *testing.T) {
	for name, spoil := range map[string]func(rs *coretest.RegistryServer, rel map[string]any){
		"sha": func(_ *coretest.RegistryServer, rel map[string]any) {
			for _, a := range rel["artifacts"].(map[string]any) {
				a.(map[string]any)["sha256"] = strings.Repeat("0", 64)
			}
		},
		"one byte more": func(rs *coretest.RegistryServer, rel map[string]any) {
			for _, a := range rel["artifacts"].(map[string]any) {
				u := a.(map[string]any)["url"].(string)
				path := u[strings.Index(u, "/files/"):]
				rs.Serve(path, append(fileAt(t, rs, u), ' '))
			}
		},
		"missing": func(rs *coretest.RegistryServer, rel map[string]any) {
			for _, a := range rel["artifacts"].(map[string]any) {
				u := a.(map[string]any)["url"].(string)
				rs.Serve(u[strings.Index(u, "/files/"):], nil)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rs := coretest.NewRegistryServer(t)
			rel := rs.NativeRelease(t, "dlx", "0.1.0", nil)
			spoil(rs, rel)
			rs.Publish(t, coretest.Registry(t0, plugin("dlx", rel)))
			dir := t.TempDir()
			s := regService(t, rs, dir)
			require.NoError(t, s.RefreshPluginRegistry(ctx))
			_, err := s.InstallPluginFromRegistry(ctx, "dlx")
			require.Error(t, err)
			if name != "missing" {
				assert.ErrorIs(t, err, core.ErrRegistryMismatch)
			}
			_, ok := pluginByName(t, s, "dlx")
			assert.False(t, ok)
			left, _ := os.ReadDir(filepath.Join(dir, "plugins"))
			for _, e := range left {
				t.Errorf("left behind: %s", e.Name())
			}
		})
	}
}

// fileAt fetches what the registry server serves at url.
func fileAt(t *testing.T, rs *coretest.RegistryServer, url string) []byte {
	t.Helper()
	resp, err := rs.Src.Client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}

func TestPublicInstallIsNeverListed(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	s := regService(t, rs, t.TempDir())
	_, err := s.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil))
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.Equal(t, "", p.Trust)
	assert.True(t, p.NeedsGrant)
}

func TestRegistryUpdate(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	v1 := rs.NativeRelease(t, "dlx", "0.1.0", nil)
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", v1)))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	_, err := s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	require.NoError(t, s.SetPluginConfig(ctx, "dlx", map[string]json.RawMessage{"endpoint": json.RawMessage(`"https://mine.example"`)}))

	rs.Publish(t, coretest.Registry(t0.Add(time.Hour), plugin("dlx", v1, rs.NativeRelease(t, "dlx", "0.2.0", nil))))
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	e := entry(t, s, "dlx")
	assert.True(t, e.Update)
	assert.Equal(t, "0.2.0", e.Version)
	_, err = s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.Equal(t, "0.2.0", p.Version)
	assert.True(t, p.Enabled)
	assert.Equal(t, "listed", p.Trust)
	cfg, err := s.GetPluginConfig(ctx, "dlx")
	require.NoError(t, err)
	assert.JSONEq(t, `"https://mine.example"`, string(cfg[0].Value), "settings survive the update")
	assert.False(t, entry(t, s, "dlx").Update)
}

func TestRegistryUpdateKeepsUserDisabled(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	v1 := rs.NativeRelease(t, "dlx", "0.1.0", nil)
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", v1)))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	_, err := s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	require.NoError(t, s.SetPluginEnabled(ctx, "dlx", false))
	rs.Publish(t, coretest.Registry(t0.Add(time.Hour), plugin("dlx", v1, rs.NativeRelease(t, "dlx", "0.2.0", nil))))
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	_, err = s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.Equal(t, "0.2.0", p.Version)
	assert.False(t, p.Enabled, "the user turned it off")
}

func TestRegistryReplacesUserInstall(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	s := regService(t, rs, t.TempDir())
	installEnabled(t, s, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, map[string]any{"echoStderr": "another build"}))
	rs.Publish(t, coretest.Registry(t0, plugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", nil))))
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	require.Equal(t, "user", nativeInfo(t, s, "dlx").Trust, "another file: not promoted")
	_, err := s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	p := nativeInfo(t, s, "dlx")
	assert.Equal(t, "listed", p.Trust)
	assert.True(t, p.Enabled)
}

// Sideloading other code under a listed script plugin's name, with the same
// permissions, must not keep the review mark.
func TestSideloadOverListedScriptIsNotListed(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	const perms = `{"net":["api.example.com"]}`
	rs.Publish(t, coretest.Registry(t0, plugin("hello", rs.ScriptRelease("hello", "0.1.0", perms, `export default function () {}`))))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(ctx))
	_, err := s.InstallPluginFromRegistry(ctx, "hello")
	require.NoError(t, err)
	require.Equal(t, "listed", nativeInfo(t, s, "hello").Trust)

	src := filepath.Join(t.TempDir(), "hello")
	require.NoError(t, os.MkdirAll(src, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"name":"hello","version":"0.1.0","main":"index.js","permissions":`+perms+`}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "index.js"), []byte(`export default function () { /* not reviewed */ }`), 0o600))
	_, err = s.InstallPlugin(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, "", nativeInfo(t, s, "hello").Trust)
}

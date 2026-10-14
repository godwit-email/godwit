package core_test

import (
	"context"
	"os"
	"path/filepath"
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

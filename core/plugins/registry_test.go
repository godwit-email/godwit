package plugins

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sha = strings.Repeat("a", 64)

func art(size int64) map[string]any {
	return map[string]any{"url": "https://example.com/f", "sha256": sha, "size": size}
}

func nativeRel(version string, arts map[string]any) map[string]any {
	return map[string]any{"version": version,
		"manifest":  map[string]any{"name": "dlx", "version": version, "native": map[string]any{"abi": 1}, "capabilities": []string{"translate"}},
		"artifacts": arts}
}

func scriptRel(version, main string, arts map[string]any) map[string]any {
	return map[string]any{"version": version,
		"manifest":  map[string]any{"name": "deepl", "version": version, "main": main, "permissions": map[string]any{"net": []string{"api.deepl.com"}}},
		"artifacts": arts}
}

func registryJSON(t *testing.T, plugins ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"schema": 1, "updated": "2026-10-14T08:00:00Z", "plugins": plugins})
	require.NoError(t, err)
	return b
}

func validPlugins() []map[string]any {
	return []map[string]any{
		{"name": "dlx", "title": map[string]any{"default": "DLX", "zh-CN": "DLX 翻译"}, "summary": "Translate", "homepage": "https://example.com",
			"releases": []any{nativeRel("0.1.0", map[string]any{"darwin-arm64": art(10), "linux-amd64": art(10)}), nativeRel("0.2.0", map[string]any{"darwin-arm64": art(10)})}},
		{"name": "deepl", "title": "DeepL", "summary": "With your key", "releases": []any{scriptRel("0.1.0", "index.js", map[string]any{"any": art(100)})}},
	}
}

func TestParseRegistryValid(t *testing.T) {
	r, err := ParseRegistry(registryJSON(t, validPlugins()...))
	require.NoError(t, err)
	require.Len(t, r.Plugins, 2)
	assert.Equal(t, "DLX 翻译", r.Plugins[0].Title.For("zh-CN"))
	assert.True(t, r.Plugins[0].Releases[0].Parsed.IsNative())
	assert.False(t, r.Plugins[1].Releases[0].Parsed.IsNative())
	assert.Equal(t, "index.js", r.Plugins[1].Releases[0].Parsed.Main)
	assert.Equal(t, int64(10), r.Plugins[0].Releases[0].Artifacts["darwin-arm64"].Size)
}

// futureRegistry has what a newer Godwit may publish: fields this build does
// not know at every level, a dlx release with a new release field, and a
// plugin whose only release declares a capability this build does not know.
func futureRegistry(t *testing.T, laya map[string]any) []byte {
	t.Helper()
	ps := validPlugins()
	ps[0]["category"] = "translation"
	next := nativeRel("0.3.0", map[string]any{"darwin-arm64": art(10)})
	next["minGodwit"], next["assets"] = "1.3.0", []any{map[string]any{"name": "model"}}
	ps[0]["releases"] = append(ps[0]["releases"].([]any), next)
	ps = append(ps, map[string]any{"name": "laya", "title": "Laya", "summary": "Local judge", "releases": []any{laya}})
	b, err := json.Marshal(map[string]any{"schema": 1, "updated": "2026-10-14T08:00:00Z", "plugins": ps, "mirrors": []string{"https://example.com"}})
	require.NoError(t, err)
	return b
}

func layaRel(minGodwit string) map[string]any {
	rel := map[string]any{"version": "0.1.0",
		"manifest":  map[string]any{"name": "laya", "version": "0.1.0", "native": map[string]any{"abi": 1}, "capabilities": []string{"judge"}},
		"artifacts": map[string]any{"darwin-arm64": art(10)}}
	if minGodwit != "" {
		rel["minGodwit"] = minGodwit
	}
	return rel
}

func TestParseRegistryForwardCompatible(t *testing.T) {
	r, err := ParseRegistry(futureRegistry(t, layaRel("1.3.0")))
	require.NoError(t, err, "what this build cannot read sets aside only that release")
	require.Len(t, r.Plugins, 3)
	dlx, laya := r.Plugins[0], r.Plugins[2]
	require.Len(t, dlx.Releases, 2)
	require.Len(t, dlx.Unsupported, 1)
	assert.Equal(t, RegistryRelease{Version: "0.3.0", MinGodwit: "1.3.0"}, dlx.Unsupported[0])
	assert.Empty(t, laya.Releases)

	rel, _, reason := dlx.Pick("darwin-arm64", "1.2.1")
	assert.Equal(t, "", reason)
	assert.Equal(t, "0.2.0", rel.Version, "the newest release this build reads")
	_, _, reason = laya.Pick("darwin-arm64", "1.2.1")
	assert.Equal(t, "godwit:1.3.0", reason)
	_, _, reason = laya.Pick("darwin-arm64", "1.3.0")
	assert.Equal(t, "unsupported", reason, "a release this build should read but cannot")
	_, _, reason = laya.Pick("darwin-arm64", "dev")
	assert.Equal(t, "unsupported", reason, "a dev build cannot install what it cannot read either")

	r, err = ParseRegistry(futureRegistry(t, layaRel("")))
	require.NoError(t, err)
	_, _, reason = r.Plugins[2].Pick("darwin-arm64", "1.2.1")
	assert.Equal(t, "unsupported", reason)

	_, err = CheckRegistry(futureRegistry(t, layaRel("1.3.0")))
	assert.Error(t, err, "publishing reads every release and field")
}

func TestParseRegistryRejects(t *testing.T) {
	mutate := func(f func(ps []map[string]any) []map[string]any) []byte {
		return registryJSON(t, f(validPlugins())...)
	}
	setRel := func(i int, rel map[string]any) func([]map[string]any) []map[string]any {
		return func(ps []map[string]any) []map[string]any { ps[i]["releases"] = []any{rel}; return ps }
	}
	// These fail the whole registry for every reader.
	fatal := map[string][]byte{
		"schema 2": func() []byte {
			b, _ := json.Marshal(map[string]any{"schema": 2, "updated": "2026-10-14T08:00:00Z", "plugins": []any{}})
			return b
		}(),
		"duplicate plugin": mutate(func(ps []map[string]any) []map[string]any { return append(ps, ps[0]) }),
		"duplicate version": mutate(func(ps []map[string]any) []map[string]any {
			ps[0]["releases"] = []any{nativeRel("0.1.0", map[string]any{"darwin-arm64": art(1)}), nativeRel("0.1.0", map[string]any{"darwin-arm64": art(1)})}
			return ps
		}),
		"http homepage": mutate(func(ps []map[string]any) []map[string]any {
			ps[0]["homepage"] = "http://example.com"
			return ps
		}),
		"data after": append(registryJSON(t, validPlugins()...), []byte("{}")...),
	}
	for name, data := range fatal {
		_, err := ParseRegistry(data)
		assert.Error(t, err, name)
		_, err = CheckRegistry(data)
		assert.Error(t, err, name)
	}
	// These fail publishing; a client sets the release aside instead.
	cases := map[string][]byte{
		"manifest name": mutate(func(ps []map[string]any) []map[string]any {
			ps[0]["name"] = "other"
			return ps
		}),
		"manifest version": mutate(setRel(0, func() map[string]any {
			r := nativeRel("0.1.0", map[string]any{"darwin-arm64": art(1)})
			r["version"] = "0.3.0"
			return r
		}())),
		"native any":          mutate(setRel(0, nativeRel("0.1.0", map[string]any{"any": art(1)}))),
		"unparsable version":  mutate(setRel(0, nativeRel("1.x", map[string]any{"darwin-arm64": art(1)}))),
		"native no artifacts": mutate(setRel(0, nativeRel("0.1.0", map[string]any{}))),
		"unknown platform":    mutate(setRel(0, nativeRel("0.1.0", map[string]any{"plan9-amd64": art(1)}))),
		"script platform":     mutate(setRel(1, scriptRel("0.1.0", "index.js", map[string]any{"linux-amd64": art(1)}))),
		"script nested main":  mutate(setRel(1, scriptRel("0.1.0", "lib/x.js", map[string]any{"any": art(1)}))),
		"script ts main":      mutate(setRel(1, scriptRel("0.1.0", "index.ts", map[string]any{"any": art(1)}))),
		"http url":            mutate(setRel(0, nativeRel("0.1.0", map[string]any{"darwin-arm64": map[string]any{"url": "http://x", "sha256": sha, "size": 1}}))),
		"short sha":           mutate(setRel(0, nativeRel("0.1.0", map[string]any{"darwin-arm64": map[string]any{"url": "https://x", "sha256": sha[1:], "size": 1}}))),
		"upper sha":           mutate(setRel(0, nativeRel("0.1.0", map[string]any{"darwin-arm64": map[string]any{"url": "https://x", "sha256": strings.ToUpper(sha), "size": 1}}))),
		"size 0":              mutate(setRel(0, nativeRel("0.1.0", map[string]any{"darwin-arm64": art(0)}))),
		"native too big":      mutate(setRel(0, nativeRel("0.1.0", map[string]any{"darwin-arm64": art(65 << 20)}))),
		"script too big":      mutate(setRel(1, scriptRel("0.1.0", "index.js", map[string]any{"any": art(6 << 20)}))),
		"unknown release field": mutate(setRel(0, func() map[string]any {
			r := nativeRel("0.1.0", map[string]any{"darwin-arm64": art(1)})
			r["extra"] = 1
			return r
		}())),
		"unknown plugin field": mutate(func(ps []map[string]any) []map[string]any {
			ps[0]["extra"] = 1
			return ps
		}),
	}
	for name, data := range cases {
		_, err := CheckRegistry(data)
		assert.Error(t, err, name)
		_, err = ParseRegistry(data)
		assert.NoError(t, err, name)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.10.0", -1}, {"v1.2", "1.2.0", 0}, {"1.0.0-beta", "1.0.0", -1}, {"1.0.0-alpha", "1.0.0-beta", -1},
		{"0.0.0-p0", "0.0.1", -1}, {"2.0.0", "1.9.9", 1},
	} {
		assert.Equal(t, c.want, CompareVersions(c.a, c.b), "%s vs %s", c.a, c.b)
	}
}

func TestPick(t *testing.T) {
	r, err := ParseRegistry(registryJSON(t, validPlugins()...))
	require.NoError(t, err)
	dlx, deepl := r.Plugins[0], r.Plugins[1]

	rel, a, reason := dlx.Pick("darwin-arm64", "1.2.0")
	assert.Equal(t, "", reason)
	assert.Equal(t, "0.2.0", rel.Version, "the newest that fits")
	assert.Equal(t, int64(10), a.Size)
	rel, _, reason = dlx.Pick("linux-amd64", "1.2.0")
	assert.Equal(t, "", reason)
	assert.Equal(t, "0.1.0", rel.Version, "0.2.0 has no linux build")
	_, _, reason = dlx.Pick("windows-amd64", "1.2.0")
	assert.Equal(t, "platform", reason)
	rel, _, reason = deepl.Pick("windows-amd64", "1.2.0")
	assert.Equal(t, "", reason)
	assert.Equal(t, "0.1.0", rel.Version, "a script plugin runs anywhere")

	dlx.Releases[1].MinGodwit = "9.0.0"
	rel, _, _ = dlx.Pick("darwin-arm64", "1.2.0")
	assert.Equal(t, "0.1.0", rel.Version)
	dlx.Releases[0].MinGodwit = "9.0.0"
	_, _, reason = dlx.Pick("darwin-arm64", "1.2.0")
	assert.Equal(t, "godwit:9.0.0", reason)
	rel, _, reason = dlx.Pick("darwin-arm64", "dev")
	assert.Equal(t, "", reason, "a dev build takes any release")
	assert.Equal(t, "0.2.0", rel.Version)

	dlx.Releases[0].MinGodwit, dlx.Releases[1].MinGodwit = "", ""
	dlx.Releases[0].Parsed.Native.ABI, dlx.Releases[1].Parsed.Native = 2, &NativeSpec{ABI: 2}
	_, _, reason = dlx.Pick("darwin-arm64", "1.2.0")
	assert.Equal(t, "abi", reason)
}

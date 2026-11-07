package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func release(version string) map[string]any {
	return map[string]any{"version": version,
		"manifest":  map[string]any{"name": "hello", "version": version, "main": "index.js", "permissions": map[string]any{}},
		"artifacts": map[string]any{"any": map[string]any{"url": "https://example.com/" + version + ".js", "sha256": strings.Repeat("a", 64), "size": 10}}}
}

func entry() map[string]any {
	return map[string]any{
		"name": "hello", "title": map[string]any{"default": "Hello", "zh-Hans": "你好"}, "summary": "Says hello",
		"category": "other", "publisher": "Godwit", "dataFlow": "local", "homepage": "https://example.com/hello",
		"source": map[string]any{"repo": "godwit-email/plugin-hello", "path": "godwit-plugin.json", "sha": testCommit},
	}
}

// buildWith serves doc at the entry's pinned commit and builds the directory.
func buildWith(t *testing.T, doc any, entries ...map[string]any) (string, int, string) {
	t.Helper()
	body, isRaw := doc.([]byte)
	if !isRaw {
		var err error
		body, err = json.Marshal(doc)
		require.NoError(t, err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/godwit-email/plugin-hello/"+testCommit+"/godwit-plugin.json" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	oldClient, oldBase, oldNow := client, sourceBase, buildNow
	client, sourceBase = srv.Client(), srv.URL
	buildNow = func() time.Time { return time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { client, sourceBase, buildNow = oldClient, oldBase, oldNow })

	dir := t.TempDir()
	in, out := filepath.Join(dir, "directory.json"), filepath.Join(dir, "registry.json")
	b, err := json.Marshal(map[string]any{"plugins": entries})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(in, b, 0o600))
	code, _, errw := call("build", in, out)
	return out, code, errw
}

func TestBuildJoinsTheDirectoryAndThePluginFile(t *testing.T) {
	out, code, errw := buildWith(t, map[string]any{"name": "hello", "releases": []any{release("0.1.0"), release("0.2.0")}}, entry())
	require.Zero(t, code, errw)
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	reg, err := plugins.CheckRegistry(b) // what passes here is what the app accepts
	require.NoError(t, err)
	assert.Equal(t, "2026-11-07T08:00:00Z", reg.Updated.Format(time.RFC3339))
	require.Len(t, reg.Plugins, 1)
	p := reg.Plugins[0]
	assert.Equal(t, "你好", p.Title.For("zh-Hans"))
	assert.Equal(t, plugins.CategoryOther, p.Category)
	assert.Equal(t, "Godwit", p.Publisher)
	assert.Equal(t, plugins.DataFlowLocal, p.DataFlow)
	assert.Len(t, p.Releases, 2)
}

func TestBuildDropsRevokedVersions(t *testing.T) {
	e := entry()
	e["revoked"] = []string{"0.1.0"}
	out, code, errw := buildWith(t, map[string]any{"name": "hello", "releases": []any{release("0.1.0"), release("0.2.0")}}, e)
	require.Zero(t, code, errw)
	b, _ := os.ReadFile(out)
	reg, err := plugins.CheckRegistry(b)
	require.NoError(t, err)
	require.Len(t, reg.Plugins[0].Releases, 1)
	assert.Equal(t, "0.2.0", reg.Plugins[0].Releases[0].Version)
}

func TestBuildRejects(t *testing.T) {
	good := map[string]any{"name": "hello", "releases": []any{release("0.1.0")}}
	change := func(k string, v any) map[string]any {
		e := entry()
		e[k] = v
		return e
	}
	sourceWith := func(k, v string) map[string]any {
		s := map[string]any{"repo": "godwit-email/plugin-hello", "path": "godwit-plugin.json", "sha": testCommit}
		s[k] = v
		return change("source", s)
	}
	cases := []struct {
		name  string
		doc   any
		entry map[string]any
		want  string
	}{
		{"unknown directory field", good, change("extra", 1), "extra"},
		{"no title", good, change("title", ""), "title"},
		{"unknown category", good, change("category", "games"), "category"},
		{"no publisher", good, change("publisher", " "), "publisher"},
		{"unknown data flow", good, change("dataFlow", "cloud"), "dataFlow"},
		{"http homepage", good, change("homepage", "http://example.com"), "homepage"},
		{"branch instead of commit", good, sourceWith("sha", "main"), "commit"},
		{"short commit", good, sourceWith("sha", "0123456"), "commit"},
		{"path out of the repository", good, sourceWith("path", "../other/godwit-plugin.json"), "commit"},
		{"another plugin's file", map[string]any{"name": "other", "releases": []any{release("0.1.0")}}, entry(), "names plugin"},
		{"unknown plugin file field", map[string]any{"name": "hello", "title": "x", "releases": []any{release("0.1.0")}}, entry(), "title"},
		{"mistyped revocation", good, change("revoked", []string{"9.9.9"}), "9.9.9"},
		{"everything revoked", good, change("revoked", []string{"0.1.0"}), "remove the entry"},
		{"release the app cannot read", map[string]any{"name": "hello", "releases": []any{map[string]any{"version": "0.1.0"}}}, entry(), "0.1.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code, errw := buildWith(t, c.doc, c.entry)
			assert.Equal(t, 1, code)
			assert.Contains(t, errw, c.want)
			assert.NoFileExists(t, out) // nothing to sign
		})
	}
}

// A second document must not be dropped: publishing what is left of the
// directory would revoke every plugin missing from it.
func TestBuildRejectsTrailingData(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		in, out := filepath.Join(dir, "directory.json"), filepath.Join(dir, "registry.json")
		require.NoError(t, os.WriteFile(in, []byte(`{"plugins":[]} {"plugins":[{"name":"hello"}]}`), 0o600))
		code, _, errw := call("build", in, out)
		assert.Equal(t, 1, code)
		assert.Contains(t, errw, "trailing data")
		assert.NoFileExists(t, out)
	})
	t.Run("plugin file", func(t *testing.T) {
		good, err := json.Marshal(map[string]any{"name": "hello", "releases": []any{release("0.1.0")}})
		require.NoError(t, err)
		out, code, errw := buildWith(t, append(good, []byte(` {"name":"hello","releases":[]}`)...), entry())
		assert.Equal(t, 1, code)
		assert.Contains(t, errw, "trailing data")
		assert.NoFileExists(t, out)
	})
}

func TestBuildFailsWhenACommitIsGone(t *testing.T) {
	gone := entry()
	gone["source"] = map[string]any{"repo": "godwit-email/deleted", "path": "godwit-plugin.json", "sha": testCommit}
	out, code, errw := buildWith(t, map[string]any{"name": "hello", "releases": []any{release("0.1.0")}}, gone)
	assert.Equal(t, 1, code)
	assert.Contains(t, errw, "404")
	assert.NoFileExists(t, out)
}

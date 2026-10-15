package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifestValid(t *testing.T) {
	data, err := os.ReadFile("testdata/deepl-translate/plugin.json")
	require.NoError(t, err)
	m, err := ParseManifest(data, "deepl-translate")
	require.NoError(t, err)
	assert.Equal(t, "deepl-translate", m.Name)
	assert.Equal(t, "0.1.0", m.Version)
	assert.Equal(t, "index.ts", m.Main)
	assert.Equal(t, Permissions{Mail: []string{"read"}, Net: []string{"api-free.deepl.com", "api.deepl.com"}}, m.Permissions)
	require.Len(t, m.Config, 3)
	assert.Equal(t, "secret", m.Config[0].Type)
	assert.Equal(t, "DeepL API Key", m.Config[0].Label.For("zh-CN"))
	assert.Equal(t, []Option{{Value: "free", Label: Text{Default: "Free"}}, {Value: "pro", Label: Text{Default: "Pro"}}}, m.Config[1].Options)
	assert.JSONEq(t, `"free"`, string(m.Config[1].Default))
	require.NotNil(t, m.Config[2].When)
	assert.Equal(t, "plan", m.Config[2].When.Key)
	assert.True(t, m.Config[2].When.Match(json.RawMessage(`"pro"`)))
	assert.False(t, m.Config[2].When.Match(json.RawMessage(`"free"`)))
}

func TestTextLanguages(t *testing.T) {
	var x Text
	require.NoError(t, json.Unmarshal([]byte(`{"default":"Plan","zh-CN":"套餐","de":"Tarif"}`), &x))
	assert.Equal(t, "套餐", x.For("zh-CN"))
	assert.Equal(t, "套餐", x.For("zh-Hans"), "script tags still find older region-tagged translations")
	assert.Equal(t, "Tarif", x.For("de-AT"), "falls back to the base language")
	assert.Equal(t, "Plan", x.For("fr"))
	b, err := json.Marshal(x)
	require.NoError(t, err)
	var y Text
	require.NoError(t, json.Unmarshal(b, &y))
	assert.Equal(t, x, y, "round trips")
	assert.Error(t, json.Unmarshal([]byte(`{"zh-CN":"套餐"}`), &y), "an object needs a default")
	for _, bad := range []string{`null`, `{"default":null}`, `{"default":"Plan","zh-CN":null}`} {
		assert.Error(t, json.Unmarshal([]byte(bad), &y), bad)
	}
	_, err = ParseManifest(manifest(`"demo"`, `"index.ts"`, okPerms, `[{"key":"x","type":"text","label":null}]`), "demo")
	assert.Error(t, err, "a null label")
}

// manifest builds a plugin.json named demo with the given parts replaced.
func manifest(name, main, perms, config string) []byte {
	return []byte(`{"name":` + name + `,"version":"1.0.0","main":` + main + `,"permissions":` + perms + `,"config":` + config + `}`)
}

const okPerms = `{"mail":["read"],"net":["api.x.com"],"llm":false}`

func TestParseManifestRejects(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"name differs from dir", string(manifest(`"other"`, `"index.ts"`, okPerms, `[]`))},
		{"name not lowercase", string(manifest(`"Demo"`, `"index.ts"`, okPerms, `[]`))},
		{"mail send", string(manifest(`"demo"`, `"index.ts"`, `{"mail":["send"]}`, `[]`))},
		{"net wildcard", string(manifest(`"demo"`, `"index.ts"`, `{"net":["*.x.com"]}`, `[]`))},
		{"net ip", string(manifest(`"demo"`, `"index.ts"`, `{"net":["1.2.3.4"]}`, `[]`))},
		{"net ipv6", string(manifest(`"demo"`, `"index.ts"`, `{"net":["[::1]:443"]}`, `[]`))},
		{"net path", string(manifest(`"demo"`, `"index.ts"`, `{"net":["x.com/path"]}`, `[]`))},
		{"net scheme", string(manifest(`"demo"`, `"index.ts"`, `{"net":["https://x.com"]}`, `[]`))},
		{"net bad port", string(manifest(`"demo"`, `"index.ts"`, `{"net":["x.com:99999"]}`, `[]`))},
		{"main parent", string(manifest(`"demo"`, `"../x.ts"`, okPerms, `[]`))},
		{"main absolute", string(manifest(`"demo"`, `"/x.ts"`, okPerms, `[]`))},
		{"main drive letter slash", string(manifest(`"demo"`, `"C:/outside.ts"`, okPerms, `[]`))},
		{"main drive letter relative", string(manifest(`"demo"`, `"C:outside.ts"`, okPerms, `[]`))},
		{"main backslash", string(manifest(`"demo"`, `"a\\b.ts"`, okPerms, `[]`))},
		{"main not ts or js", string(manifest(`"demo"`, `"x.py"`, okPerms, `[]`))},
		{"unknown field", `{"name":"demo","version":"1","main":"index.ts","exec":"rm"}`},
		{"duplicate key", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"a","type":"text","label":"A"},{"key":"a","type":"number","label":"A"}]`))},
		{"when refers to a later field", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"a","type":"text","label":"A","when":{"key":"b","equals":true}},{"key":"b","type":"boolean","label":"B"}]`))},
		{"when refers to itself", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"a","type":"boolean","label":"A","when":{"key":"a","equals":true}}]`))},
		{"when value type mismatch", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"b","type":"boolean","label":"B"},{"key":"a","type":"text","label":"A","when":{"key":"b","equals":"true"}}]`))},
		{"when both equals and notEquals", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"b","type":"boolean","label":"B"},{"key":"a","type":"text","label":"A","when":{"key":"b","equals":true,"notEquals":false}}]`))},
		{"when on a secret", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"s","type":"secret","label":"S"},{"key":"a","type":"text","label":"A","when":{"key":"s","equals":"x"}}]`))},
		{"duplicate select option", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"p","type":"select","label":"P","options":[{"value":"a","label":"A"},{"value":"a","label":"B"}]}]`))},
		{"select without options", string(manifest(`"demo"`, `"index.ts"`, okPerms, `[{"key":"p","type":"select","label":"P"}]`))},
		{"select default not an option", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"p","type":"select","label":"P","options":[{"value":"a","label":"A"}],"default":"b"}]`))},
		{"secret with default", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"s","type":"secret","label":"S","default":"hunter2hunter2"}]`))},
		{"number default is a string", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"n","type":"number","label":"N","default":"5"}]`))},
		{"boolean default is a number", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"b","type":"boolean","label":"B","default":1}]`))},
		{"unknown type", string(manifest(`"demo"`, `"index.ts"`, okPerms, `[{"key":"x","type":"file","label":"X"}]`))},
		{"options on text", string(manifest(`"demo"`, `"index.ts"`, okPerms,
			`[{"key":"x","type":"text","label":"X","options":[{"value":"a","label":"A"}]}]`))},
	} {
		_, err := ParseManifest([]byte(c.data), "demo")
		assert.Error(t, err, c.name)
	}
	_, err := ParseManifest(manifest(`"demo"`, `"src/index.js"`, `{"mail":["read","write"],"net":["API.X.com:8443"],"llm":true}`,
		`[{"key":"n","type":"number","label":"N","default":5},{"key":"p","type":"select","label":"P","options":[{"value":"a","label":"A"}],"allowCustom":true,"default":"zz"}]`), "demo")
	assert.NoError(t, err, "the template itself is valid")
}

func TestWhenTyped(t *testing.T) {
	m, err := ParseManifest(manifest(`"demo"`, `"index.ts"`, okPerms, `[
		{"key":"on","type":"boolean","label":"On"},
		{"key":"n","type":"number","label":"N"},
		{"key":"a","type":"text","label":"A","when":{"key":"on","equals":true}},
		{"key":"b","type":"text","label":"B","when":{"key":"n","notEquals":3}}]`), "demo")
	require.NoError(t, err)
	a, b := m.Config[2].When, m.Config[3].When
	assert.True(t, a.Match(json.RawMessage(`true`)))
	assert.False(t, a.Match(json.RawMessage(`false`)))
	assert.False(t, a.Match(json.RawMessage(`"true"`)), "a string never equals a boolean")
	assert.False(t, a.Match(nil), "unset is not true")
	assert.False(t, b.Match(json.RawMessage(`3`)))
	assert.False(t, b.Match(json.RawMessage(`3.0`)), "numbers compare by value")
	assert.True(t, b.Match(json.RawMessage(`4`)))
	assert.True(t, b.Match(nil), "unset is not 3")

	for _, bad := range []string{
		`{"key":"on","equals":1}`, `{"key":"n","equals":"3"}`, `{"key":"n","notEquals":true}`, `{"key":"on"}`, `{"key":"on","equals":null}`,
	} {
		_, err := ParseManifest(manifest(`"demo"`, `"index.ts"`, okPerms, `[
			{"key":"on","type":"boolean","label":"On"},{"key":"n","type":"number","label":"N"},
			{"key":"a","type":"text","label":"A","when":`+bad+`}]`), "demo")
		assert.Error(t, err, bad)
	}
}

func TestPermissionHashStable(t *testing.T) {
	parse := func(perms, config string) Manifest {
		m, err := ParseManifest(manifest(`"demo"`, `"index.ts"`, perms, config), "demo")
		require.NoError(t, err)
		return m
	}
	base := parse(`{"mail":["read","write"],"net":["a.com","b.com"],"llm":true}`, `[]`)
	h := base.PermissionHash()
	assert.Len(t, h, 64)
	assert.Equal(t, h, base.PermissionHash())
	assert.Equal(t, h, parse(`{"llm":true,"net":["B.com","a.com"],"mail":["write","read"]}`, `[{"key":"x","type":"text","label":"X"}]`).PermissionHash(),
		"order, case and config do not matter")
	assert.Equal(t, h, parse(`{"mail":["read","write"],"net":["a.com:443","b.com"],"llm":true}`, `[]`).PermissionHash(),
		"443 is the default port")
	assert.Equal(t, []string{"a.com", "b.com:8443"}, parse(`{"net":["A.com:443","b.com:8443"]}`, `[]`).Permissions.Net)
	for _, p := range []string{
		`{"mail":["read"],"net":["a.com","b.com"],"llm":true}`,
		`{"mail":["read","write"],"net":["a.com"],"llm":true}`,
		`{"mail":["read","write"],"net":["a.com","b.com:8443"],"llm":true}`,
		`{"mail":["read","write"],"net":["a.com","b.com"],"llm":false}`,
	} {
		assert.NotEqual(t, h, parse(p, `[]`).PermissionHash(), p)
	}
}

func TestPermissionError(t *testing.T) {
	err := error(&PermissionError{Perm: "mail:write"})
	assert.True(t, errors.Is(err, ErrPermission))
	assert.Contains(t, err.Error(), "mail:write")
}

// TestSDKTypesCompile parses sdk/godwit.d.ts and builds a plugin that only
// imports types from it, as the installer will.
func TestSDKTypesCompile(t *testing.T) {
	dts, err := os.ReadFile(filepath.Join("..", "..", "sdk", "godwit.d.ts"))
	require.NoError(t, err)
	tr := api.Transform(string(dts), api.TransformOptions{Loader: api.LoaderTS, Sourcefile: "godwit.d.ts"})
	require.Empty(t, tr.Errors, "%v", tr.Errors)
	for _, want := range []string{"interface Ctx", "on(", "actions", "cards", "tools", "config", "mail", "llm", "net", "storage"} {
		assert.Contains(t, string(dts), want)
	}
	assert.NotRegexp(t, `\bsend\s*[(:]`, string(dts), "no send capability")

	res := api.Build(api.BuildOptions{
		EntryPoints: []string{filepath.Join("testdata", "sdk-types", "index.ts")},
		Bundle:      true, Write: false, Format: api.FormatIIFE, GlobalName: "plugin",
		Target: api.ES2022, Platform: api.PlatformNeutral,
	})
	require.Empty(t, res.Errors, "%v", res.Errors)
	require.Len(t, res.OutputFiles, 1)
	assert.Contains(t, string(res.OutputFiles[0].Contents), "translate")
}

func TestParseManifestNative(t *testing.T) {
	m, err := ParseManifest([]byte(`{"name":"dlx","version":"0.1.0","native":{"abi":1},"capabilities":["translate"],
		"config":[{"key":"endpoint","type":"text","label":"Endpoint"}]}`), "dlx")
	require.NoError(t, err)
	assert.True(t, m.IsNative())
	assert.Equal(t, "", m.Main)
	assert.Equal(t, NativeABI, m.Native.ABI)
	assert.Equal(t, []string{"translate"}, m.Capabilities)
	b, err := json.Marshal(m)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"main"`, "a native manifest stores no main")
}

func TestParseManifestNativeRejects(t *testing.T) {
	for name, body := range map[string]string{
		"main too":        `"main":"index.ts","native":{"abi":1},"capabilities":["translate"]`,
		"permissions":     `"native":{"abi":1},"capabilities":["translate"],"permissions":{"net":["api.x.com"]}`,
		"llm permission":  `"native":{"abi":1},"capabilities":["translate"],"permissions":{"llm":true}`,
		"abi 2":           `"native":{"abi":2},"capabilities":["translate"]`,
		"abi 0":           `"native":{"abi":0},"capabilities":["translate"]`,
		"no capabilities": `"native":{"abi":1}`,
		"unknown":         `"native":{"abi":1},"capabilities":["judge"]`,
		"duplicate":       `"native":{"abi":1},"capabilities":["translate","translate"]`,
	} {
		_, err := ParseManifest([]byte(`{"name":"dlx","version":"0.1.0",`+body+`}`), "dlx")
		assert.Error(t, err, name)
	}
}

func TestParseManifestScriptCapabilities(t *testing.T) {
	with := func(caps string) []byte {
		return []byte(`{"name":"demo","version":"1.0.0","main":"index.ts","permissions":` + okPerms + `,"capabilities":` + caps + `}`)
	}
	m, err := ParseManifest(with(`["translate"]`), "demo")
	require.NoError(t, err)
	assert.False(t, m.IsNative())
	assert.Equal(t, []string{"translate"}, m.Capabilities)
	_, err = ParseManifest(with(`["judge"]`), "demo")
	assert.Error(t, err)
	_, err = ParseManifest(manifest(`"demo"`, `"index.ts"`, okPerms, `[]`), "demo")
	assert.NoError(t, err, "capabilities are optional for a script plugin")
}

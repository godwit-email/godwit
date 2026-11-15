package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func externalSvc(t *testing.T) (*Service, string) {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	s, err := New(context.Background(), Options{DataDir: t.TempDir(), ExternalDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s, dir
}

var externalTick = time.Now()

// writeExternal writes file name as the gateway would, with a newer mtime
// each time so the next sync sees it whatever the file system's resolution.
func writeExternal(t *testing.T, dir, name string, v any) {
	t.Helper()
	b, ok := v.([]byte)
	if !ok {
		var err error
		b, err = json.Marshal(v)
		require.NoError(t, err)
	}
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	externalTick = externalTick.Add(time.Second)
	require.NoError(t, os.Chtimes(p, externalTick, externalTick))
}

// magpieEntry is models.json as the plugin writes it: models as magpie
// describes them, more fields than Godwit reads among them.
func magpieEntry(key, model string, models ...string) map[string]any {
	list := []map[string]any{}
	for _, id := range models {
		m := map[string]any{"id": id, "efforts": []string{"low", "high"}, "pricing": map[string]string{"prompt": "0.000001"}}
		switch id {
		case "anthropic/claude":
			m["name"], m["context"] = "Claude (magpie)", 200000
		case "openai/gpt":
			m["name"], m["context"] = "openai/gpt", 12 // its id for a name, and no believable window
		}
		list = append(list, m)
	}
	if len(models) > 0 {
		list = append(list, map[string]any{"id": models[0], "name": "listed twice"})
	}
	return map[string]any{"magpie": map[string]any{"by": "magpie", "baseURL": "http://127.0.0.1:3425/v1", "apiKey": key,
		"kind": "openai", "generateModels": list, "defaultModel": model}}
}

func managedService(t *testing.T, s *Service, name string) (ModelService, bool) {
	t.Helper()
	list, err := s.ListModelServices(t.Context())
	require.NoError(t, err)
	for _, m := range list {
		if m.Name == name {
			return m, true
		}
	}
	return ModelService{}, false
}

func defaultModel(t *testing.T, s *Service) string {
	t.Helper()
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	return st.DefaultModel
}

func TestExternalModelService(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	own, err := s.AddModelService(ctx, ModelServiceInput{Name: "mine", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "k", Models: []string{"x"}})
	require.NoError(t, err)
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.DefaultModel = "mine/x"
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	// Connected: the service appears, read-only, and becomes the default.
	writeExternal(t, dir, externalModels, magpieEntry("key1", "anthropic/claude", "anthropic/claude", "openai/gpt"))
	s.syncExternal(ctx)
	m, ok := managedService(t, s, "magpie")
	require.True(t, ok)
	assert.Equal(t, "magpie", m.ManagedBy)
	assert.Equal(t, "http://127.0.0.1:3425/v1", m.BaseURL)
	assert.Equal(t, []string{"anthropic/claude", "openai/gpt"}, m.Models)
	assert.Equal(t, "key1", storedKey(t, s, m.ID))
	assert.Equal(t, "magpie/anthropic/claude", defaultModel(t, s))
	assert.Equal(t, map[string]string{"anthropic/claude": "Claude (magpie)"}, m.Names, "the gateway's own names; an id for a name is none")
	assert.Equal(t, "Claude (magpie)", s.ModelName(ctx, ""))
	assert.Equal(t, 160000, s.historyBudget(m.BaseURL, "anthropic/claude"), "80% of the gateway's context window")
	assert.Equal(t, defaultHistoryBudget, s.historyBudget(m.BaseURL, "openai/gpt"), "an implausible window is dropped")
	r, _, err := s.resolveRef(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude", r.Model)
	assert.Equal(t, "key1", r.APIKey)

	_, err = s.UpdateModelService(ctx, m.ID, m.Input())
	assert.ErrorIs(t, err, ErrModelServiceManaged)
	assert.ErrorIs(t, s.RemoveModelService(ctx, m.ID), ErrModelServiceManaged)

	// The default is read from the file, never saved: the user's own stays beneath it.
	st, _ = s.GetSettings(ctx)
	assert.Equal(t, "magpie", st.DefaultModelBy)
	stored, err := s.storedSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "mine/x", stored.DefaultModel)
	// Saving the settings saves the rest; the default is changed in the gateway.
	st.DefaultModel, st.Theme = "mine/x", "dark"
	saved, err := s.SetSettings(ctx, st)
	require.NoError(t, err)
	assert.Equal(t, "magpie/anthropic/claude", saved.DefaultModel)
	st, _ = s.GetSettings(ctx)
	assert.Equal(t, "dark", st.Theme)
	assert.Equal(t, "magpie/anthropic/claude", st.DefaultModel)

	// A new key and model list update it.
	writeExternal(t, dir, externalModels, magpieEntry("key2", "anthropic/claude", "anthropic/claude", "openai/gpt", "x/y"))
	s.syncExternal(ctx)
	m, _ = managedService(t, s, "magpie")
	assert.Equal(t, "key2", storedKey(t, s, m.ID))
	assert.Len(t, m.Models, 3)
	assert.Equal(t, "magpie/anthropic/claude", defaultModel(t, s))

	// A new choice in the file moves it again.
	writeExternal(t, dir, externalModels, magpieEntry("key2", "x/y", "anthropic/claude", "openai/gpt", "x/y"))
	s.syncExternal(ctx)
	assert.Equal(t, "magpie/x/y", defaultModel(t, s))
	// The file stops choosing: the user's own again, the service kept.
	writeExternal(t, dir, externalModels, magpieEntry("key2", "", "anthropic/claude", "openai/gpt", "x/y"))
	s.syncExternal(ctx)
	assert.Equal(t, "mine/x", defaultModel(t, s))
	writeExternal(t, dir, externalModels, magpieEntry("key2", "x/y", "anthropic/claude", "openai/gpt", "x/y"))
	s.syncExternal(ctx)
	assert.Equal(t, "magpie/x/y", defaultModel(t, s))

	// A file halfway written changes nothing.
	writeExternal(t, dir, externalModels, []byte(`{"magpie": {"baseURL": `))
	s.syncExternal(ctx)
	_, ok = managedService(t, s, "magpie")
	assert.True(t, ok)

	// Disconnected (the gateway leaves an empty entry): it goes, and the user's default comes back.
	writeExternal(t, dir, externalModels, map[string]any{"magpie": map[string]any{}})
	s.syncExternal(ctx)
	_, ok = managedService(t, s, "magpie")
	assert.False(t, ok)
	assert.Equal(t, "mine/x", defaultModel(t, s))
	_, ok = managedService(t, s, own.Name)
	assert.True(t, ok)
}

func TestExternalModelServiceRefused(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	_, err := s.AddModelService(ctx, ModelServiceInput{Name: "taken", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "k", Models: []string{"x"}})
	require.NoError(t, err)
	remote := magpieEntry("k", "", "m")
	remote["magpie"].(map[string]any)["baseURL"] = "https://gateway.example.com/v1"
	files := map[string]any{
		"oldstyle": map[string]any{"baseURL": "http://127.0.0.1:3425/v1", "apiKey": "k", "kind": "openai", "generateModels": []string{"m"}}, // not objects: unreadable
		"magpie":   remote["magpie"],                                                                                                        // not on this computer
		"taken":    magpieEntry("k", "", "m")["magpie"],
		"Bad Name": magpieEntry("k", "", "m")["magpie"],
		"nomodel":  magpieEntry("k", "zzz", "m")["magpie"],
	}
	writeExternal(t, dir, externalModels, files)
	s.syncExternal(ctx)
	list, err := s.ListModelServices(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "taken", list[0].Name)
	assert.Empty(t, list[0].ManagedBy, "the user's own service is left alone")

	// The user removes theirs: the gateway's takes the name on the next sync, the file unchanged.
	require.NoError(t, s.RemoveModelService(ctx, list[0].ID))
	s.syncExternal(ctx)
	m, ok := managedService(t, s, "taken")
	require.True(t, ok)
	assert.Equal(t, "magpie", m.ManagedBy)
}

func TestExternalDirCreated(t *testing.T) {
	keyring.MockInit()
	dir := filepath.Join(t.TempDir(), ".godwit", "external")
	s, err := New(context.Background(), Options{DataDir: t.TempDir(), ExternalDir: dir})
	require.NoError(t, err)
	defer s.Close()
	st, err := os.Stat(dir)
	require.NoError(t, err, "a gateway finds Godwit installed by the folder")
	assert.True(t, st.IsDir())
}

func TestExternalSurvivesRestart(t *testing.T) {
	keyring.MockInit()
	data, dir := t.TempDir(), t.TempDir()
	writeExternal(t, dir, externalModels, magpieEntry("k", "m", "m"))
	s, err := New(context.Background(), Options{DataDir: data, ExternalDir: dir})
	require.NoError(t, err)
	s.syncExternal(t.Context())
	s.Close()
	s, err = New(context.Background(), Options{DataDir: data, ExternalDir: dir})
	require.NoError(t, err)
	defer s.Close()
	s.syncExternal(t.Context())
	list, err := s.ListModelServices(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "magpie/m", defaultModel(t, s))
}

func TestExternalMCPServers(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	_, err := s.AddMCPServer(ctx, MCPServerInput{Name: "mine", Transport: "stdio", Command: "true"})
	require.NoError(t, err)
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	servers := map[string]any{"mcpServers": map[string]any{
		"magpie-search": map[string]any{"type": "http", "url": "http://127.0.0.1:3425/mcp/search", "headers": map[string]string{"Authorization": "Bearer secret-1"}},
		"mine":          map[string]any{"command": "evil"},
		"old-sse":       map[string]any{"type": "sse", "url": "http://127.0.0.1:1/sse"},
	}}
	writeExternal(t, dir, externalMCP, servers)
	s.syncExternal(ctx)
	byName := func() map[string]MCPServer {
		list, err := s.ListMCPServers(ctx)
		require.NoError(t, err)
		out := map[string]MCPServer{}
		for _, srv := range list {
			out[srv.Name] = srv
		}
		return out
	}
	got := byName()
	require.Len(t, got, 2)
	assert.Equal(t, "true", got["mine"].Command, "the user's server is left alone")
	search := got["magpie-search"]
	assert.Equal(t, "magpie", search.ManagedBy)
	assert.False(t, search.Granted, "the user grants it")
	assert.Equal(t, []string{"Authorization"}, search.HeaderKeys)

	_, err = s.UpdateMCPServer(ctx, search.ID, MCPServerInput{Name: "x", Transport: "http", URL: search.URL})
	assert.ErrorIs(t, err, ErrMCPManaged)
	assert.ErrorIs(t, s.RemoveMCPServer(ctx, search.ID), ErrMCPManaged)

	// Granted, then the same file again: the grant stays.
	require.NoError(t, s.GrantMCPServer(ctx, search.ID))
	writeExternal(t, dir, externalMCP, servers)
	s.syncExternal(ctx)
	assert.True(t, byName()["magpie-search"].Granted)

	// What it runs changed: the grant goes.
	servers["mcpServers"].(map[string]any)["magpie-search"].(map[string]any)["headers"] = map[string]string{"Authorization": "Bearer secret-2"}
	writeExternal(t, dir, externalMCP, servers)
	s.syncExternal(ctx)
	assert.False(t, byName()["magpie-search"].Granted)

	// Disconnected: it goes, the user's stays.
	require.NoError(t, os.Remove(filepath.Join(dir, externalMCP)))
	s.syncExternal(ctx)
	got = byName()
	assert.Len(t, got, 1)
	assert.Contains(t, got, "mine")
}

func TestExternalSkillsAndInstructions(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	s.syncExternal(ctx)
	skill := func(root, name, desc string) string {
		d := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(d, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+desc+"\n---\nDo it.\n"), 0o600))
		return d
	}
	ext := filepath.Join(dir, externalSkills)
	require.NoError(t, os.MkdirAll(ext, 0o700))
	skill(ext, "copied", "from the library")
	library := t.TempDir()
	if err := os.Symlink(skill(library, "linked", "linked from the library"), filepath.Join(ext, "linked")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	skill(filepath.Join(s.dataDir, "skills"), "mine", "the user's")
	skill(ext, "mine", "the gateway's")

	list, err := s.ListSkills(ctx)
	require.NoError(t, err)
	got := map[string]SkillInfo{}
	for _, sk := range list {
		got[sk.Name] = sk
	}
	assert.Equal(t, "magpie", got["copied"].ManagedBy)
	assert.Equal(t, "magpie", got["linked"].ManagedBy)
	assert.Equal(t, "the user's", got["mine"].Description, "the user's skill wins")
	assert.Empty(t, got["mine"].ManagedBy)

	// Disconnected, the gateway leaves its entry empty but keeps the library's files: they stay its.
	writeExternal(t, dir, externalModels, map[string]any{"magpie": map[string]any{}})
	s.syncExternal(ctx)
	list, err = s.ListSkills(ctx)
	require.NoError(t, err)
	for _, sk := range list {
		if sk.Name == "copied" {
			assert.Equal(t, "magpie", sk.ManagedBy)
		}
	}

	assert.Empty(t, s.externalInstructionsBlock())
	writeExternal(t, dir, externalInstructions, []byte("<!-- magpie -->\nAlways answer <briefly>.\n"+strings.Repeat("é", maxInstructions)))
	b := s.externalInstructionsBlock()
	assert.Contains(t, b, "Always answer &lt;briefly&gt;.")
	assert.Contains(t, b, "kept in magpie")
	assert.Less(t, len(b), maxInstructions+500)
	assert.True(t, strings.HasSuffix(b, "</user_instructions>"))
}

func TestExternalRetriesAfterKeychainFailure(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	orig := keyringSet
	setFor(t, &keyringSet, func(service, user, pass string) error { return errors.New("keychain locked") })
	writeExternal(t, dir, externalModels, magpieEntry("k", "m", "m"))
	s.syncExternal(ctx)
	_, ok := managedService(t, s, "magpie")
	require.False(t, ok, "the key could not be saved")

	// The keychain is back: the next poll applies the same file again.
	setFor(t, &keyringSet, orig)
	s.syncExternal(ctx)
	m, ok := managedService(t, s, "magpie")
	require.True(t, ok)
	assert.Equal(t, "k", storedKey(t, s, m.ID))
}

func TestExternalUnreadableDirKeepsSettings(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	s, dir := externalSvc(t)
	ctx := t.Context()
	writeExternal(t, dir, externalModels, magpieEntry("k", "m", "m"))
	s.syncExternal(ctx)
	_, ok := managedService(t, s, "magpie")
	require.True(t, ok)

	require.NoError(t, os.Chmod(dir, 0))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	s.syncExternal(ctx)
	_, ok = managedService(t, s, "magpie")
	assert.True(t, ok, "a folder Godwit cannot read is not a folder emptied")
	assert.Equal(t, "magpie/m", defaultModel(t, s))
}

func TestExternalMCPRetriesAfterSecretsAndCleanupFail(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	set, del := keyringSet, keyringDelete
	setFor(t, &keyringSet, func(service, user, pass string) error { return errors.New("keychain locked") })
	setFor(t, &keyringDelete, func(service, user string) error { return errors.New("keychain locked") })
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	writeExternal(t, dir, externalMCP, map[string]any{"mcpServers": map[string]any{
		"search": map[string]any{"type": "http", "url": "http://127.0.0.1:3425/mcp", "headers": map[string]string{"Authorization": "Bearer secret-1"}}}})
	s.syncExternal(ctx)

	// The keychain is back: the row the failed cleanup left is completed, its secret saved.
	setFor(t, &keyringSet, set)
	setFor(t, &keyringDelete, del)
	s.syncExternal(ctx)
	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	sec, err := keychainGet(s.mcpSecretUser(list[0].ID, "header", "Authorization"))
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-1", sec)
}

func TestExternalDefaultOfSeveralGateways(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	entry := func(name, model string) map[string]any {
		return magpieEntry("k-"+name, model, "m")["magpie"].(map[string]any)
	}
	writeExternal(t, dir, externalModels, map[string]any{"b": entry("b", "m"), "a": entry("a", "m")})
	s.syncExternal(ctx)
	assert.Equal(t, "a/m", defaultModel(t, s), "the first by name")
	writeExternal(t, dir, externalModels, map[string]any{"b": entry("b", "m")})
	s.syncExternal(ctx)
	assert.Equal(t, "b/m", defaultModel(t, s))
	writeExternal(t, dir, externalModels, map[string]any{})
	s.syncExternal(ctx)
	assert.Empty(t, defaultModel(t, s), "the user's own: none")
}

func TestExternalStaleSnapshotKeepsTheUsersDefault(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	_, err := s.AddModelService(ctx, ModelServiceInput{Name: "mine", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "k", Models: []string{"x"}})
	require.NoError(t, err)
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.DefaultModel = "mine/x"
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	writeExternal(t, dir, externalModels, magpieEntry("k", "m", "m"))
	s.syncExternal(ctx)

	snapshot, err := s.GetSettings(ctx) // read while magpie chooses
	require.NoError(t, err)
	require.Equal(t, "magpie/m", snapshot.DefaultModel)
	writeExternal(t, dir, externalModels, map[string]any{"magpie": map[string]any{}}) // then magpie disconnects
	s.syncExternal(ctx)
	snapshot.Theme = "dark" // and the snapshot is saved
	_, err = s.SetSettings(ctx, snapshot)
	require.NoError(t, err)
	st, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "mine/x", st.DefaultModel)
	assert.Equal(t, "dark", st.Theme)
}

func TestExternalMCPPartialSecretsRepairedWhenTheFileGoesBack(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	server := func(one, two string) map[string]any {
		return map[string]any{"mcpServers": map[string]any{"search": map[string]any{"type": "http", "url": "http://127.0.0.1:3425/mcp",
			"headers": map[string]string{"X-One": one, "X-Two": two}}}}
	}
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	writeExternal(t, dir, externalMCP, server("secret-a1", "secret-a2"))
	s.syncExternal(ctx)

	// The file changes; the keychain saves one new value, then refuses the other.
	set := keyringSet
	setFor(t, &keyringSet, func(service, user, pass string) error {
		if pass == "secret-b2" {
			return errors.New("keychain locked")
		}
		return set(service, user, pass)
	})
	writeExternal(t, dir, externalMCP, server("secret-b1", "secret-b2"))
	s.syncExternal(ctx)

	// The gateway goes back to the first config: its secrets are written again.
	setFor(t, &keyringSet, set)
	writeExternal(t, dir, externalMCP, server("secret-a1", "secret-a2"))
	s.syncExternal(ctx)
	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	one, err := keychainGet(s.mcpSecretUser(list[0].ID, "header", "X-One"))
	require.NoError(t, err)
	assert.Equal(t, "secret-a1", one)
}

func TestExternalSkillsChangeNotifies(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	s.syncExternal(ctx)
	ch, stop := s.Subscribe()
	defer stop()
	d := filepath.Join(dir, externalSkills, "fresh")
	require.NoError(t, os.MkdirAll(d, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: fresh\ndescription: new\n---\nDo it.\n"), 0o600))
	s.syncExternal(ctx)
	for {
		select {
		case ev := <-ch:
			if ev.Type == events.SkillsChanged {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no skills.changed after a skill was added")
		}
	}
}

func TestExternalMCPEmptyValueRemovesTheSecret(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	server := func(two string) map[string]any {
		return map[string]any{"mcpServers": map[string]any{"search": map[string]any{"type": "http", "url": "http://127.0.0.1:3425/mcp",
			"headers": map[string]string{"X-One": "secret-one", "X-Two": two}}}}
	}
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	writeExternal(t, dir, externalMCP, server("secret-two"))
	s.syncExternal(ctx)
	writeExternal(t, dir, externalMCP, server("")) // emptied in the file
	s.syncExternal(ctx)
	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, []string{"X-One"}, list[0].HeaderKeys, "an empty value is no value, not the old one kept")
	_, err = keychainGet(s.mcpSecretUser(list[0].ID, "header", "X-Two"))
	assert.Error(t, err)
}

func TestExternalMCPShortValue(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	writeExternal(t, dir, externalMCP, map[string]any{"mcpServers": map[string]any{
		"tool": map[string]any{"command": "magpie-tool", "env": map[string]string{"DEBUG": "1"}}}})
	s.syncExternal(ctx)
	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1, "a short config value does not keep the server out")
	assert.Equal(t, []string{"DEBUG"}, list[0].EnvKeys)
	_, err = s.AddMCPServer(ctx, MCPServerInput{Name: "mine", Transport: "stdio", Command: "x", Env: map[string]string{"TOKEN": "short"}})
	assert.Error(t, err, "the form still asks for a secret long enough to redact")
}

func TestExternalLastSkillRemovedAfterAConfigChangeNotifies(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	d := filepath.Join(dir, externalSkills, "only")
	require.NoError(t, os.MkdirAll(d, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: only\ndescription: x\n---\nDo it.\n"), 0o600))
	s.syncExternal(ctx)
	ch, stop := s.Subscribe()
	defer stop()
	s.extDirty.Store(true) // the user changed a service of their own
	require.NoError(t, os.RemoveAll(d))
	s.syncExternal(ctx)
	for {
		select {
		case ev := <-ch:
			if ev.Type == events.SkillsChanged {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no skills.changed after the last skill went")
		}
	}
}

func TestExternalMCPNameWithSpacesSkipped(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	writeExternal(t, dir, externalMCP, map[string]any{"mcpServers": map[string]any{
		" search ": map[string]any{"command": "magpie-search"},
		"tool":     map[string]any{"command": "magpie-tool"}}})
	s.syncExternal(ctx)
	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "tool", list[0].Name)
}

func TestExternalJudgeModels(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	write := func(decision ...map[string]any) {
		e := magpieEntry("k", "", "m")
		e["magpie"].(map[string]any)["judgeModels"] = decision
		writeExternal(t, dir, externalModels, e)
		s.syncExternal(ctx)
	}
	judge := func() (string, string) {
		m, ok := managedService(t, s, "magpie")
		require.True(t, ok)
		return m.JudgeFormat, m.JudgeModel
	}

	write(map[string]any{"id": "typesafe/jev-latest", "name": "jev-latest", "provider": "typesafe", "endpoints": []string{"/v1/systemone", "/v1/decisions"}})
	f, m := judge()
	assert.Equal(t, string(ai.FormatSystemOne), f, "System One where the model answers it")
	assert.Equal(t, "typesafe/jev-latest", m)

	write(map[string]any{"id": "openai/gpt-6-luna", "endpoints": []string{"/v1/decisions"}})
	f, m = judge()
	assert.Equal(t, string(ai.FormatDecisions), f)
	assert.Equal(t, "openai/gpt-6-luna", m)

	write() // none: no judging
	f, m = judge()
	assert.Empty(t, f)
	assert.Empty(t, m)
}

func TestExternalMCPNamedOnceTheGatewayIs(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	// mcp.json is read before models.json names the gateway, as when both are written at once
	writeExternal(t, dir, externalMCP, map[string]any{"mcpServers": map[string]any{"tool": map[string]any{"command": "magpie-tool"}}})
	s.syncExternal(ctx)
	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NoError(t, s.GrantMCPServer(ctx, list[0].ID))

	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m"))
	s.syncExternal(ctx)
	list, err = s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "magpie", list[0].ManagedBy)
	assert.True(t, list[0].Granted, "a new label keeps the grant")
}

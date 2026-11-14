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

func magpieEntry(key, model string, models ...string) map[string]any {
	return map[string]any{"magpie": map[string]any{"by": "magpie", "baseURL": "http://127.0.0.1:3425/v1", "apiKey": key,
		"kind": "openai", "models": models, "model": model, "names": map[string]string{"anthropic/claude": "Claude (magpie)", "nowhere": "x"},
		"contexts": map[string]int{"anthropic/claude": 200000, "openai/gpt": 12}}}
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
	assert.Equal(t, map[string]string{"anthropic/claude": "Claude (magpie)"}, m.Names, "the gateway's names; one for a model it does not list is dropped")
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

	// A new key and model list update it; the same model leaves a default the user chose since.
	st, _ = s.GetSettings(ctx)
	st.DefaultModel = "magpie/openai/gpt"
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	writeExternal(t, dir, externalModels, magpieEntry("key2", "anthropic/claude", "anthropic/claude", "openai/gpt", "x/y"))
	s.syncExternal(ctx)
	m, _ = managedService(t, s, "magpie")
	assert.Equal(t, "key2", storedKey(t, s, m.ID))
	assert.Len(t, m.Models, 3)
	assert.Equal(t, "magpie/openai/gpt", defaultModel(t, s))

	// A new choice in the file moves it again.
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
	remote := magpieEntry("k", "")
	remote["magpie"].(map[string]any)["baseURL"] = "https://gateway.example.com/v1"
	remote["magpie"].(map[string]any)["models"] = []string{"m"}
	files := map[string]any{
		"magpie":   remote["magpie"], // not on this computer
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

func TestExternalRemovalReleasesUsersDefault(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	writeExternal(t, dir, externalModels, magpieEntry("k", "", "m")) // the file chooses no model
	s.syncExternal(ctx)
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.DefaultModel = "magpie/m" // the user picks it in Godwit
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	writeExternal(t, dir, externalModels, map[string]any{"magpie": map[string]any{}})
	s.syncExternal(ctx)
	_, ok := managedService(t, s, "magpie")
	require.False(t, ok)
	assert.Empty(t, defaultModel(t, s), "no default left naming the removed service")
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

func TestExternalRemovalKeepsTheChainToTheUsersDefault(t *testing.T) {
	s, dir := externalSvc(t)
	ctx := t.Context()
	_, err := s.AddModelService(ctx, ModelServiceInput{Name: "mine", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "k", Models: []string{"x"}})
	require.NoError(t, err)
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.DefaultModel = "mine/x"
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	entry := func(name string) map[string]any { return magpieEntry("k-"+name, "m", "m")["magpie"].(map[string]any) }

	writeExternal(t, dir, externalModels, map[string]any{"a": entry("a")})
	s.syncExternal(ctx)
	require.Equal(t, "a/m", defaultModel(t, s))
	writeExternal(t, dir, externalModels, map[string]any{"a": entry("a"), "b": entry("b")})
	s.syncExternal(ctx)
	require.Equal(t, "b/m", defaultModel(t, s))

	// The first one goes while the second is the default: the second now gives back the user's.
	writeExternal(t, dir, externalModels, map[string]any{"b": entry("b")})
	s.syncExternal(ctx)
	assert.Equal(t, "b/m", defaultModel(t, s))
	writeExternal(t, dir, externalModels, map[string]any{})
	s.syncExternal(ctx)
	assert.Equal(t, "mine/x", defaultModel(t, s))
}

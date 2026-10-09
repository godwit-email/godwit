package core

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/skills"
	"github.com/godwit-email/godwit/core/store"
	"github.com/zalando/go-keyring"
)

var (
	ErrNeedsGrant     = errors.New("plugin permissions are not granted")
	ErrPluginNotFound = errors.New("plugin not found")
)

// PluginInfo is an installed plugin. Error is why it cannot run (its files are
// damaged or an update was interrupted); such a plugin is listed, not hidden.
type PluginInfo struct {
	Name, Version         string
	Permissions           plugins.Permissions
	Enabled, NeedsGrant   bool
	DisabledReason, Error string
	Config                []plugins.ConfigField
}

// disabled_reason values set here (Task 5 adds failures).
const (
	disabledFiles = "files_changed" // the version directory is damaged
	disabledLoad  = "load_failed"   // the plugin did not start
)

// pluginHook, if set, runs at "switch" (before the row points at a new
// version), "switched" (after it) and "load"; an error stops there (tests).
var pluginHook func(stage, name string) error

type pluginRow struct {
	m                             plugins.Manifest
	sourceHash, permHash, granted string
	installDir                    string
	enabled                       bool
	reason                        string
}

// Each install writes a fresh directory DataDir/plugins/<name>/<source hash>-<random>/
// and never changes it; the row's install_dir points at the active one, so
// switching versions is a single row update on every platform, and a directory
// the row points at is never deleted.
func (s *Service) pluginsDir() string { return filepath.Join(s.dataDir, "plugins") }

func (s *Service) versionDir(name, installDir string) string {
	return filepath.Join(s.pluginsDir(), name, installDir)
}

var versionRE = regexp.MustCompile(`^[0-9a-f]{64}-[0-9a-f]{16}$`)

func isVersion(n string) bool { return versionRE.MatchString(n) }

// removeOldVersions removes the version directories of name other than keep
// ("" removes all), and the plugin's directory if nothing else is left in it.
func (s *Service) removeOldVersions(name, keep string) error {
	dir := filepath.Join(s.pluginsDir(), name)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if isVersion(e.Name()) && e.Name() != keep {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	os.Remove(dir) // only if empty
	return nil
}

func (s *Service) pluginRows(ctx context.Context, name string) (map[string]pluginRow, error) {
	q := "SELECT name, manifest, source_hash, permission_hash, granted_hash, enabled, disabled_reason, install_dir FROM plugins"
	var args []any
	if name != "" {
		q += " WHERE name = ?"
		args = append(args, name)
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := map[string]pluginRow{}
	for _, r := range rows.Values {
		var m plugins.Manifest
		if err := json.Unmarshal([]byte(store.Str(r[1])), &m); err != nil {
			return nil, err
		}
		out[store.Str(r[0])] = pluginRow{m: m, sourceHash: store.Str(r[2]), permHash: store.Str(r[3]), granted: store.Str(r[4]),
			enabled: store.Int(r[5]) != 0, reason: store.Str(r[6]), installDir: store.Str(r[7])}
	}
	return out, nil
}

// checkPluginFiles reports whether the installed files are the ones the row
// records: a mismatch is an update that crashed between replacing the files and
// writing the row, or files changed behind our back.
func (s *Service) checkPluginFiles(name string, r pluginRow) error {
	if r.installDir == "" {
		return errors.New("plugin files are missing; reinstall the plugin")
	}
	dir := s.versionDir(name, r.installDir)
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return errors.New("plugin.json is missing; reinstall the plugin")
	}
	if _, err := plugins.ParseManifest(data, name); err != nil {
		return fmt.Errorf("%w; reinstall the plugin", err)
	}
	if h, err := plugins.HashDir(dir); err != nil || h != r.sourceHash {
		return errors.New("the installed files were changed or damaged; reinstall the plugin")
	}
	return nil
}

func (s *Service) pluginInfo(name string, r pluginRow) PluginInfo {
	info := PluginInfo{Name: name, Version: r.m.Version, Permissions: r.m.Permissions, Enabled: r.enabled,
		NeedsGrant: r.granted != r.permHash, DisabledReason: r.reason, Config: r.m.Config}
	if err := s.checkPluginFiles(name, r); err != nil {
		info.Error = err.Error()
	}
	return info
}

// ListPlugins lists the installed plugins, including ones that cannot run.
func (s *Service) ListPlugins(ctx context.Context) ([]PluginInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	s.pluginsMu.RLock() // no update is half done while we look
	defer s.pluginsMu.RUnlock()
	rows, err := s.pluginRows(ctx, "")
	if err != nil {
		return nil, err
	}
	out := []PluginInfo{}
	// ponytail: hashes every plugin's files per call (20 MB cap each); cache by mtime if lists get slow.
	for n, r := range rows {
		out = append(out, s.pluginInfo(n, r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InstallPlugin installs or updates the plugin in srcDir: it snapshots the
// directory, builds the snapshot (a plugin that does not build is not
// installed), moves it into a new version directory and points the row at it.
// An update with other permissions is disabled until they are granted; with the
// same ones a running plugin restarts on the new code, and is disabled if that fails.
func (s *Service) InstallPlugin(ctx context.Context, srcDir string) (PluginInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return PluginInfo{}, err
	}
	defer end()
	if err := os.MkdirAll(s.pluginsDir(), 0o700); err != nil {
		return PluginInfo{}, err
	}
	staging, err := os.MkdirTemp(s.pluginsDir(), ".staging-")
	if err != nil {
		return PluginInfo{}, err
	}
	defer os.RemoveAll(staging)
	snap := filepath.Join(staging, "p")
	hash, err := plugins.Snapshot(srcDir, snap)
	if err != nil {
		return PluginInfo{}, err
	}
	// Everything from here on reads the snapshot, never srcDir.
	data, err := os.ReadFile(filepath.Join(snap, "plugin.json"))
	if err != nil {
		return PluginInfo{}, errors.New("plugin.json is missing")
	}
	var head struct{ Name string } // the source directory may have any name
	_ = json.Unmarshal(data, &head)
	m, err := plugins.ParseManifest(data, head.Name)
	if err != nil {
		return PluginInfo{}, err
	}
	if _, err := plugins.Build(snap, m.Main); err != nil {
		return PluginInfo{}, err
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return PluginInfo{}, err
	}

	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	before, err := s.pluginRows(ctx, m.Name)
	if err != nil {
		return PluginInfo{}, err
	}
	installDir := hash + "-" + hex.EncodeToString(binary.BigEndian.AppendUint64(nil, rand.Uint64()))
	ver := s.versionDir(m.Name, installDir)
	if err := os.MkdirAll(filepath.Dir(ver), 0o700); err != nil {
		return PluginInfo{}, err
	}
	if err := os.Rename(snap, ver); err != nil { // a fresh path: nothing is replaced
		return PluginInfo{}, err
	}
	if pluginHook != nil {
		if err := pluginHook("switch", m.Name); err != nil {
			return PluginInfo{}, err
		}
	}
	// The switch. If it fails nothing changed: the old version keeps running and
	// the new directory is removed on the next start or install.
	_, err = s.db.Execute(ctx, `INSERT INTO plugins(name, version, manifest, source_hash, install_dir, permission_hash, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, unixepoch())
		ON CONFLICT(name) DO UPDATE SET version = excluded.version, manifest = excluded.manifest,
			source_hash = excluded.source_hash, install_dir = excluded.install_dir, permission_hash = excluded.permission_hash,
			enabled = enabled AND permission_hash = excluded.permission_hash,
			disabled_reason = '', installed_at = excluded.installed_at`,
		m.Name, m.Version, string(mj), hash, installDir, m.PermissionHash())
	if err != nil {
		return PluginInfo{}, err
	}
	if pluginHook != nil {
		if err := pluginHook("switched", m.Name); err != nil {
			return PluginInfo{}, err
		}
	}
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	// Secrets the new version no longer declares would outlive an uninstall.
	var dropped []string
	for _, k := range secretKeys(before[m.Name].m.Config) {
		if !slices.Contains(secretKeys(m.Config), k) {
			dropped = append(dropped, k)
		}
	}
	secretErr := s.deletePluginSecrets(m.Name, dropped)
	s.loadPluginSecrets(m.Name, m.Config)
	s.stopPlugin(m.Name)
	rows, err := s.pluginRows(ctx, m.Name)
	if err != nil {
		return PluginInfo{}, err
	}
	r := rows[m.Name]
	var loadErr error
	if r.enabled {
		if loadErr = s.startOrDisable(ctx, m.Name, &r); loadErr != nil {
			loadErr = fmt.Errorf("plugin %s was installed but did not start: %w", m.Name, loadErr)
		}
	}
	if err := s.removeOldVersions(m.Name, installDir); err != nil {
		loadErr = errors.Join(loadErr, err)
	}
	loadErr = errors.Join(loadErr, secretErr)
	return s.pluginInfo(m.Name, r), loadErr
}

// startOrDisable loads an enabled plugin; if it does not start it is disabled
// (persisted, announced) and r updated.
func (s *Service) startOrDisable(ctx context.Context, name string, r *pluginRow) error {
	reason := disabledFiles
	err := s.checkPluginFiles(name, *r)
	if err == nil {
		reason = disabledLoad
		if err = s.loadPlugin(name, *r); err == nil {
			return nil
		}
	}
	if _, dbErr := s.db.Execute(ctx, "UPDATE plugins SET enabled = 0, disabled_reason = ? WHERE name = ?", reason, name); dbErr != nil {
		return errors.Join(err, dbErr)
	}
	r.enabled, r.reason = false, reason
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	return err
}

// GrantPermission grants the plugin's current permissions.
func (s *Service) GrantPermission(ctx context.Context, name string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	r, err := s.db.Execute(ctx, "UPDATE plugins SET granted_hash = permission_hash WHERE name = ?", name)
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return ErrPluginNotFound
	}
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	return nil
}

// SetPluginEnabled starts or stops a plugin. Starting needs its current
// permissions granted (ErrNeedsGrant) and intact files; it resets the failure count.
func (s *Service) SetPluginEnabled(ctx context.Context, name string, enabled bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	rows, err := s.pluginRows(ctx, name)
	if err != nil {
		return err
	}
	r, ok := rows[name]
	if !ok {
		return ErrPluginNotFound
	}
	if !enabled {
		if _, err := s.db.Execute(ctx, "UPDATE plugins SET enabled = 0 WHERE name = ?", name); err != nil {
			return err
		}
		s.stopPlugin(name)
	} else {
		if r.granted != r.permHash {
			return ErrNeedsGrant
		}
		if err := s.checkPluginFiles(name, r); err != nil {
			return err
		}
		_, running := s.running[name]
		if !running {
			if err := s.loadPlugin(name, r); err != nil {
				return err
			}
		}
		if _, err := s.db.Execute(ctx, "UPDATE plugins SET enabled = 1, failures = 0, disabled_reason = '' WHERE name = ?", name); err != nil {
			if !running {
				s.stopPlugin(name) // only what this call started
			}
			return err
		}
	}
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	return nil
}

// UninstallPlugin stops the plugin and removes its row (with its config,
// storage and cursor) and its files.
func (s *Service) UninstallPlugin(ctx context.Context, name string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if !skills.ValidName(name) { // name becomes a path below
		return ErrPluginNotFound
	}
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	rows, err := s.pluginRows(ctx, name)
	if err != nil {
		return err
	}
	// Secrets first: if the keychain is unavailable nothing is removed yet.
	if err := s.deletePluginSecrets(name, secretKeys(rows[name].m.Config)); err != nil {
		return err
	}
	s.secrets.RemoveOwner(pluginOwner(name))
	s.stopPlugin(name)
	r, err := s.db.Execute(ctx, "DELETE FROM plugins WHERE name = ?", name)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.pluginsDir(), name)
	_, statErr := os.Lstat(dir)
	if r.Changes == 0 && statErr != nil {
		return ErrPluginNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	return nil
}

// startPlugins runs at New: it removes staging directories and versions no row
// points at (an update that died before or after its switch), then starts the
// enabled plugins, disabling those that are damaged or do not start.
func (s *Service) startPlugins(ctx context.Context) error {
	rows, err := s.pluginRows(ctx, "")
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(s.pluginsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range ents {
		var err error
		if strings.HasPrefix(e.Name(), ".") { // .staging-*
			err = os.RemoveAll(filepath.Join(s.pluginsDir(), e.Name()))
		} else if e.IsDir() {
			err = s.removeOldVersions(e.Name(), rows[e.Name()].installDir) // no row: "", all of them
		}
		if err != nil {
			return err
		}
	}
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	for n, r := range rows {
		s.loadPluginSecrets(n, r.m.Config)
		if r.enabled {
			s.startOrDisable(ctx, n, &r) // a failure is recorded on the row
		}
	}
	return nil
}

// loadPlugin (re)starts an enabled plugin on its installed code; stopPlugin
// stops it. Both are called with pluginsMu held.
// ponytail: records the running code's hash only; Task 5 starts and stops the VM here.
func (s *Service) loadPlugin(name string, r pluginRow) error {
	if pluginHook != nil {
		if err := pluginHook("load", name); err != nil {
			return err
		}
	}
	if s.running == nil {
		s.running = map[string]string{}
	}
	s.running[name] = r.sourceHash
	return nil
}

func (s *Service) stopPlugin(name string) { delete(s.running, name) }

// ConfigValue is a plugin setting as the settings form shows it: the stored
// value, or with Set false the default. A secret's Value is always empty.
type ConfigValue struct {
	Key   string
	Value json.RawMessage
	Set   bool
}

// Secrets live in the keychain as <install-id>/plugin:<name>:<key>; the rest
// in plugin_config as JSON.
func (s *Service) pluginSecretUser(name, key string) string {
	return s.installID + "/plugin:" + name + ":" + key
}

func pluginOwner(name string) string { return "plugin:" + name }

// secretValue reads a plugin secret; ok is false when it is not set.
func (s *Service) secretValue(name, key string) (v string, ok bool, err error) {
	v, err = keychainGet(s.pluginSecretUser(name, key))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	return v, err == nil, err
}

// loadPluginSecrets puts the plugin's secret values in the secret set; an
// unreadable keychain leaves them out.
func (s *Service) loadPluginSecrets(name string, fields []plugins.ConfigField) {
	var vs []string
	for _, f := range fields {
		if f.Type == "secret" {
			if v, ok, _ := s.secretValue(name, f.Key); ok {
				vs = append(vs, v)
			}
		}
	}
	s.secrets.Set(pluginOwner(name), vs...)
}

// deletePluginSecrets deletes the keychain entries of keys; missing ones are fine.
func (s *Service) deletePluginSecrets(name string, keys []string) error {
	for _, k := range keys {
		if err := keychainDelete(s.pluginSecretUser(name, k)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return err
		}
	}
	return nil
}

func secretKeys(fields []plugins.ConfigField) []string {
	var out []string
	for _, f := range fields {
		if f.Type == "secret" {
			out = append(out, f.Key)
		}
	}
	return out
}

// storedConfig returns the plugin's manifest, config version and the stored
// non-secret values that still fit their field. The caller holds pluginsMu.
func (s *Service) storedConfig(ctx context.Context, name string) (plugins.Manifest, int64, map[string]json.RawMessage, error) {
	var m plugins.Manifest
	rows, err := s.db.Query(ctx, "SELECT manifest, config_version FROM plugins WHERE name = ?", name)
	if err != nil {
		return m, 0, nil, err
	}
	if len(rows.Values) == 0 {
		return m, 0, nil, ErrPluginNotFound
	}
	if err := json.Unmarshal([]byte(store.Str(rows.Values[0][0])), &m); err != nil {
		return m, 0, nil, err
	}
	ver := store.Int(rows.Values[0][1])
	kv, err := s.db.Query(ctx, "SELECT key, value FROM plugin_config WHERE plugin = ?", name)
	if err != nil {
		return m, 0, nil, err
	}
	fields := map[string]plugins.ConfigField{}
	for _, f := range m.Config {
		fields[f.Key] = f
	}
	stored := map[string]json.RawMessage{}
	for _, r := range kv.Values {
		k, v := store.Str(r[0]), json.RawMessage(store.Str(r[1]))
		if f, ok := fields[k]; ok && f.Type != "secret" && f.Accepts(v) { // an update may have changed the field
			stored[k] = v
		}
	}
	return m, ver, stored, nil
}

// withDefaults adds the defaults of the unset fields to vals.
func withDefaults(fields []plugins.ConfigField, vals map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range fields {
		if v, ok := vals[f.Key]; ok {
			out[f.Key] = v
		} else if f.Default != nil {
			out[f.Key] = f.Default
		}
	}
	return out
}

// shownFields reports which fields their when shows; a hidden field counts as unset.
func shownFields(fields []plugins.ConfigField, vals map[string]json.RawMessage) map[string]bool {
	shown := map[string]bool{}
	for _, f := range fields {
		var ref json.RawMessage
		if f.When != nil && shown[f.When.Key] {
			ref = vals[f.When.Key]
		}
		shown[f.Key] = f.When == nil || f.When.Match(ref)
	}
	return shown
}

// unset: absent, null or "".
func unset(v json.RawMessage) bool {
	var x any
	return v == nil || json.Unmarshal(v, &x) == nil && (x == nil || x == "")
}

// GetPluginConfig returns the plugin's settings in declaration order.
func (s *Service) GetPluginConfig(ctx context.Context, name string) ([]ConfigValue, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	m, _, stored, err := s.storedConfig(ctx, name)
	if err != nil {
		return nil, err
	}
	out := []ConfigValue{}
	for _, f := range m.Config {
		v := ConfigValue{Key: f.Key}
		if f.Type == "secret" {
			if _, v.Set, err = s.secretValue(name, f.Key); err != nil {
				return nil, err
			}
		} else if raw, ok := stored[f.Key]; ok {
			v.Value, v.Set = raw, true
		} else {
			v.Value = f.Default
		}
		out = append(out, v)
	}
	return out, nil
}

// SetPluginConfig saves the settings form. Non-secret fields take the given
// value; absent, null or "" unsets one (its default applies). A secret absent or
// "" keeps its value and null clears it. Fields their when hides are ignored and
// keep what they had. Nothing is written unless every shown field is valid and
// every shown required one has a value; a write bumps config_version.
func (s *Service) SetPluginConfig(ctx context.Context, name string, values map[string]json.RawMessage) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	m, _, stored, err := s.storedConfig(ctx, name)
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, f := range m.Config {
		declared[f.Key] = true
	}
	for k := range values {
		if !declared[k] {
			return fmt.Errorf("config: %q is not a setting of plugin %s", k, name)
		}
	}
	next := map[string]json.RawMessage{}
	for _, f := range m.Config {
		if f.Type != "secret" && !unset(values[f.Key]) {
			next[f.Key] = values[f.Key]
		}
	}
	shown := shownFields(m.Config, withDefaults(m.Config, next))

	// Validate everything first. Errors never quote values.
	final := map[string]json.RawMessage{}
	secretSet := map[string]string{}
	var secretClear []string
	for _, f := range m.Config {
		v := values[f.Key]
		switch {
		case !shown[f.Key]:
			if old, ok := stored[f.Key]; ok {
				final[f.Key] = old
			}
		case f.Type == "secret":
			var str *string
			if v != nil && json.Unmarshal(v, &str) != nil {
				return fmt.Errorf("config %s: a secret must be a string", f.Key)
			}
			switch {
			case v != nil && str == nil:
				if f.Required {
					return fmt.Errorf("config %s is required", f.Key)
				}
				secretClear = append(secretClear, f.Key)
			case v == nil || *str == "":
				if f.Required {
					if _, ok, err := s.secretValue(name, f.Key); err != nil {
						return err
					} else if !ok {
						return fmt.Errorf("config %s is required", f.Key)
					}
				}
			case utf8.RuneCountInString(*str) < 8:
				return fmt.Errorf("config %s: a secret must be at least 8 characters", f.Key)
			default:
				secretSet[f.Key] = *str
			}
		case unset(v):
			if f.Required && f.Default == nil {
				return fmt.Errorf("config %s is required", f.Key)
			}
		case !f.Accepts(v):
			return fmt.Errorf("config %s: not a valid %s value", f.Key, f.Type)
		default:
			final[f.Key] = v
		}
	}

	// Keychain first, then the rows; a failure puts the old secrets back.
	type prev struct {
		v  string
		ok bool
	}
	done := map[string]prev{}
	restore := func() {
		for k, p := range done {
			if p.ok {
				keychainSet(s.pluginSecretUser(name, k), p.v)
			} else {
				keychainDelete(s.pluginSecretUser(name, k))
			}
		}
	}
	err = func() error {
		for _, k := range secretKeys(m.Config) {
			v, set := secretSet[k]
			if !set && !slices.Contains(secretClear, k) {
				continue
			}
			old, ok, err := s.secretValue(name, k)
			if err != nil {
				return err
			}
			done[k] = prev{old, ok}
			if set {
				err = keychainSet(s.pluginSecretUser(name, k), v)
			} else {
				err = s.deletePluginSecrets(name, []string{k})
			}
			if err != nil {
				return fmt.Errorf("save plugin secret %s: %w", k, err)
			}
		}
		stmts := []sqlite.Statement{{SQL: "DELETE FROM plugin_config WHERE plugin = ?", Args: []any{name}}}
		for k, v := range final {
			stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO plugin_config(plugin, key, value) VALUES (?, ?, ?)", Args: []any{name, k, string(v)}})
		}
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE plugins SET config_version = config_version + 1 WHERE name = ?", Args: []any{name}})
		_, err := s.db.Transaction(ctx, stmts)
		return err
	}()
	if err != nil {
		restore()
	}
	s.loadPluginSecrets(name, m.Config)
	return err
}

// pluginConfig is what the plugin sees as ctx.config: the shown fields' values
// or defaults, secrets in plain text, and the config version they belong to.
// The caller must not hold pluginsMu.
func (s *Service) pluginConfig(ctx context.Context, name string) (map[string]any, int64, error) {
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	m, ver, stored, err := s.storedConfig(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	vals := withDefaults(m.Config, stored)
	shown := shownFields(m.Config, vals)
	out := map[string]any{}
	for _, f := range m.Config {
		if !shown[f.Key] {
			continue
		}
		if f.Type == "secret" {
			v, ok, err := s.secretValue(name, f.Key)
			if err != nil {
				return nil, 0, err
			}
			if ok {
				out[f.Key] = v
			}
		} else if raw, ok := vals[f.Key]; ok {
			var x any
			if err := json.Unmarshal(raw, &x); err != nil {
				return nil, 0, err
			}
			out[f.Key] = x
		}
	}
	return out, ver, nil
}

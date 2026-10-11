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

	"github.com/dop251/goja"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/skills"
	"github.com/godwit-email/godwit/core/store"
	"github.com/zalando/go-keyring"
)

var (
	ErrNeedsGrant     = errors.New("permissions are not granted")
	ErrPluginNotFound = errors.New("plugin not found")
)

// PluginInfo is an installed plugin. Error is why it cannot run (its files are
// damaged or an update was interrupted); such a plugin is listed, not hidden.
type PluginInfo struct {
	Name, Version         string
	Description           plugins.Text
	Permissions           plugins.Permissions
	Enabled, NeedsGrant   bool
	DisabledReason, Error string
	Config                []plugins.ConfigField
}

// disabled_reason values.
const (
	disabledFailures = "failures"             // maxFailures calls in a row failed
	disabledFiles    = "files_changed"        // the version directory is damaged
	disabledLoad     = "load_failed"          // the plugin did not start
	disabledKeychain = "keychain_unavailable" // its secrets could not be read
)

// pluginHook, if set, runs at "switch" (before the row points at a new
// version), "switched" (after it) and "load"; an error stops there (tests).
var pluginHook func(stage, name string) error

type pluginRow struct {
	m                             plugins.Manifest
	sourceHash, permHash, granted string
	installDir                    string
	secretKeys                    []string // keys that may have a keychain entry
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
	q := "SELECT name, manifest, source_hash, permission_hash, granted_hash, enabled, disabled_reason, install_dir, secret_keys FROM plugins"
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
		var keys []string
		if err := json.Unmarshal([]byte(store.Str(r[8])), &keys); err != nil {
			return nil, err
		}
		out[store.Str(r[0])] = pluginRow{secretKeys: keys, m: m, sourceHash: store.Str(r[2]), permHash: store.Str(r[3]), granted: store.Str(r[4]),
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
	info := PluginInfo{Name: name, Version: r.m.Version, Description: r.m.Description, Permissions: r.m.Permissions, Enabled: r.enabled,
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
	// Secrets the new version no longer declares are deleted (or stay listed for uninstall).
	var dropped []string
	for _, k := range before[m.Name].secretKeys {
		if !slices.Contains(secretKeys(m.Config), k) {
			dropped = append(dropped, k)
		}
	}
	secretErr := s.dropSecrets(ctx, m.Name, before[m.Name].secretKeys, dropped)
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
		if err = s.loadPlugin(ctx, name, *r); err == nil {
			return nil
		}
		if errors.Is(err, ErrKeychainUnavailable) {
			reason = disabledKeychain
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
			if err := s.loadPlugin(ctx, name, r); err != nil {
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
	// Secrets first: a key the keychain could not delete stays listed, and
	// nothing else is removed yet.
	keys := rows[name].secretKeys
	if err := s.dropSecrets(ctx, name, keys, keys); err != nil {
		return err
	}
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
	s.cleanEvents(ctx) // best effort; dispatchers clean after each batch too
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	for n, r := range rows {
		if !r.enabled {
			s.loadPluginSecrets(n, r.secretKeys) // best effort; a running plugin needs them (loadPlugin)
		}
		if r.enabled {
			s.startOrDisable(ctx, n, &r) // a failure is recorded on the row
		}
	}
	return nil
}

// runningPlugin is a started plugin: the code it runs and its VM.
type runningPlugin struct {
	hash string // source hash of the code it runs
	gen  uint64 // this instance, among every instance started by the Service
	host *pluginHost
	p    *plugins.Plugin
	row  pluginRow // the row it was started from: manifest and permission hashes

	stopHooks func() // cancels its message.received dispatcher, if any
}

// loadPlugin (re)starts an enabled plugin on its installed code: it builds the
// bundle (cached in the version directory), creates the VM and runs the
// plugin's initialization. stopPlugin stops it. Both are called with pluginsMu held.
func (s *Service) loadPlugin(ctx context.Context, name string, r pluginRow) error {
	if err := s.loadPluginSecrets(name, r.secretKeys); err != nil {
		return err // never run with secrets the redaction does not know
	}
	if pluginHook != nil {
		if err := pluginHook("load", name); err != nil {
			return err
		}
	}
	code, err := plugins.Build(s.versionDir(name, r.installDir), r.m.Main)
	if err != nil {
		return err
	}
	prog, err := goja.Compile(name+".js", string(code), false)
	if err != nil {
		return fmt.Errorf("plugin %s: %s", name, s.secrets.Redact(err.Error()))
	}
	// ctx.config is a snapshot read here (under our lock) and replaced by
	// SetPluginConfig, so reading it never waits for pluginsMu.
	netOpts := s.pluginNet
	netOpts.UserAgent = "Godwit-Plugin/" + r.m.Version
	h := &pluginHost{s: s, name: name, net: plugins.NewClient(netOpts), decide: make(chan struct{}, 2)}
	if err := h.refreshConfig(ctx); err != nil {
		return err
	}
	vm := plugins.NewVM(name, prog, plugins.VMOptions{Clock: s.pluginClock, Redact: s.secrets.Redact})
	p, err := plugins.Load(ctx, vm, r.m, h, h.snapshot())
	if err != nil {
		vm.Close()
		return err
	}
	s.stopPlugin(name)
	if s.running == nil {
		s.running = map[string]*runningPlugin{}
	}
	s.pluginGen++
	rp := &runningPlugin{hash: r.sourceHash, gen: s.pluginGen, p: p, host: h, row: r}
	s.running[name] = rp
	if err := s.registerPluginTools(name, rp); err != nil {
		s.stopPlugin(name)
		return err
	}
	if err := s.startHooks(ctx, rp, r); err != nil {
		s.stopPlugin(name)
		return err
	}
	return nil
}

func (s *Service) stopPlugin(name string) {
	if rp := s.running[name]; rp != nil {
		if rp.stopHooks != nil {
			rp.stopHooks() // it ends on its own; Close waits for it (s.begin)
		}
		rp.p.VM().Close()
		delete(s.running, name)
		s.unregisterPluginTools(name)
	}
}

// stopPlugins stops every plugin (Close).
func (s *Service) stopPlugins() {
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	for n := range s.running {
		s.stopPlugin(n)
	}
}

// maxFailures consecutive failed calls disable a plugin.
const maxFailures = 3

// pluginFailed counts a failed call (an error, a timeout, the watchdog) of the
// running instance gen (runningPlugin.gen) of an enabled plugin; the third in a row disables and stops it. Never call it
// from the plugin's own VM goroutine or holding pluginsMu.
func (s *Service) pluginFailed(ctx context.Context, name string, gen uint64) error {
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	if rp := s.running[name]; rp == nil || rp.gen != gen {
		return nil // an instance since stopped or replaced
	}
	if _, err := s.db.Execute(ctx, "UPDATE plugins SET failures = failures + 1 WHERE name = ? AND enabled = 1", name); err != nil {
		return err
	}
	r, err := s.db.Execute(ctx, "UPDATE plugins SET enabled = 0, disabled_reason = ? WHERE name = ? AND enabled = 1 AND failures >= ?",
		disabledFailures, name, maxFailures)
	if err != nil {
		return err
	}
	if r.Changes > 0 {
		s.stopPlugin(name)
		s.bus.Publish(events.Event{Type: events.PluginsChanged})
	}
	return nil
}

// pluginSucceeded resets the failure count if instance gen still runs.
func (s *Service) pluginSucceeded(ctx context.Context, name string, gen uint64) error {
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	if rp := s.running[name]; rp == nil || rp.gen != gen {
		return nil
	}
	_, err := s.db.Execute(ctx, "UPDATE plugins SET failures = 0 WHERE name = ? AND failures <> 0", name)
	return err
}

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

// secretValue reads a plugin secret; ok is false when it is not set.
func (s *Service) secretValue(name, key string) (v string, ok bool, err error) {
	v, err = keychainGet(s.pluginSecretUser(name, key))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	return v, err == nil, err
}

// loadPluginSecrets puts the stored secrets in the secret set. If any cannot
// be read it changes nothing (the set never shrinks on a read failure).
func (s *Service) loadPluginSecrets(name string, keys []string) error {
	vals := map[string]string{}
	for _, k := range keys {
		v, ok, err := s.secretValue(name, k)
		if err != nil {
			return fmt.Errorf("read plugin secret %s: %w", k, err)
		}
		if ok {
			vals[k] = v
		}
	}
	for _, v := range vals {
		s.secrets.Add(v)
	}
	return nil
}

// dropSecrets deletes the keychain entries of drop (missing ones are fine)
// and records listed minus the deleted ones as the plugin's secret_keys; a key
// whose delete failed stays listed, for the next try. Values stay redacted
// until the Service closes (secretSet only grows).
func (s *Service) dropSecrets(ctx context.Context, name string, listed, drop []string) error {
	var errs error
	keep := []string{}
	for _, k := range listed {
		if !slices.Contains(drop, k) {
			keep = append(keep, k)
			continue
		}
		if err := keychainDelete(s.pluginSecretUser(name, k)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = errors.Join(errs, fmt.Errorf("delete plugin secret %s: %w", k, err))
			keep = append(keep, k)
			continue
		}
	}
	if len(drop) == 0 {
		return nil
	}
	kj, _ := json.Marshal(keep)
	_, err := s.db.Execute(ctx, "UPDATE plugins SET secret_keys = ? WHERE name = ?", string(kj), name)
	return errors.Join(errs, err)
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
	rows, err := s.pluginRows(ctx, name)
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

	// Read the old secrets before changing anything.
	type prev struct {
		v  string
		ok bool
	}
	olds := map[string]prev{}
	listed := slices.Clone(rows[name].secretKeys)
	for _, k := range secretKeys(m.Config) {
		if _, set := secretSet[k]; set || slices.Contains(secretClear, k) {
			v, ok, err := s.secretValue(name, k)
			if err != nil {
				return fmt.Errorf("read plugin secret %s: %w", k, err)
			}
			olds[k] = prev{v, ok}
			if !slices.Contains(listed, k) {
				listed = append(listed, k)
			}
		}
	}
	// The version goes up before the keychain changes, so whatever happens
	// below, nothing bound to the old config stays valid.
	lj, _ := json.Marshal(listed)
	if _, err := s.db.Execute(ctx, "UPDATE plugins SET config_version = config_version + 1, secret_keys = ? WHERE name = ?", string(lj), name); err != nil {
		return err
	}
	done := []string{}
	for _, k := range secretKeys(m.Config) {
		if _, ok := olds[k]; !ok {
			continue
		}
		done = append(done, k)
		if v, set := secretSet[k]; set {
			s.secrets.Add(v) // redacted before it is stored
			err = keychainSet(s.pluginSecretUser(name, k), v)
		} else if err = keychainDelete(s.pluginSecretUser(name, k)); errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
		if err != nil {
			err = fmt.Errorf("save plugin secret %s: %w", k, err)
			break
		}
	}
	if err == nil {
		keep := slices.DeleteFunc(listed, func(k string) bool { return slices.Contains(secretClear, k) })
		kj, _ := json.Marshal(keep)
		stmts := []sqlite.Statement{{SQL: "DELETE FROM plugin_config WHERE plugin = ?", Args: []any{name}}}
		for k, v := range final {
			stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO plugin_config(plugin, key, value) VALUES (?, ?, ?)", Args: []any{name, k, string(v)}})
		}
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE plugins SET secret_keys = ? WHERE name = ?", Args: []any{string(kj), name}})
		_, err = s.db.Transaction(ctx, stmts)
	}
	if err == nil {
		// The old values stay redacted: the running instance may hold them.
		if rp := s.running[name]; rp != nil {
			rp.host.refreshConfig(ctx) // the next call sees the saved config
		}
		return nil
	}
	// Put the old secrets back; both values stay redacted either way.
	for _, k := range done {
		p, user := olds[k], s.pluginSecretUser(name, k)
		var rerr error
		if p.ok {
			rerr = keychainSet(user, p.v)
		} else if rerr = keychainDelete(user); errors.Is(rerr, keyring.ErrNotFound) {
			rerr = nil
		}
		if rerr != nil {
			err = errors.Join(err, fmt.Errorf("restore plugin secret %s: %w", k, rerr))
		}
	}
	return err
}

// pluginConfig is what the plugin sees as ctx.config: the shown fields' values
// or defaults, secrets in plain text, and the config version they belong to.
// The caller must not hold pluginsMu.
func (s *Service) pluginConfig(ctx context.Context, name string) (map[string]any, int64, error) {
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	return s.pluginConfigLocked(ctx, name)
}

// pluginConfigLocked is pluginConfig for a caller holding pluginsMu.
func (s *Service) pluginConfigLocked(ctx context.Context, name string) (map[string]any, int64, error) {
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

// SetPluginNetForTest replaces the network plugins and MCP servers over HTTP
// use (resolver, dialer, plain HTTP and loopback allowed or not). Tests only:
// it lets fixture plugins reach a local server. Call it right after New,
// before any plugin or MCP server starts.
func (s *Service) SetPluginNetForTest(n plugins.NetOptions) {
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	s.pluginNet = n
}

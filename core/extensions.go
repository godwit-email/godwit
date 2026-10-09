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
	"sort"
	"strings"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/skills"
	"github.com/godwit-email/godwit/core/store"
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
	s.stopPlugin(name)
	// TODO(Task 3): delete the plugin's keychain secrets here.
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

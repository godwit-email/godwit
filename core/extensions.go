package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// disabledFiles is disabled_reason for a plugin whose files do not match its row.
const disabledFiles = "files_changed"

type pluginRow struct {
	m                             plugins.Manifest
	sourceHash, permHash, granted string
	enabled                       bool
	reason                        string
}

func (s *Service) pluginsDir() string { return filepath.Join(s.dataDir, "plugins") }

func (s *Service) pluginRows(ctx context.Context, name string) (map[string]pluginRow, error) {
	q := "SELECT name, manifest, source_hash, permission_hash, granted_hash, enabled, disabled_reason FROM plugins"
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
			enabled: store.Int(r[5]) != 0, reason: store.Str(r[6])}
	}
	return out, nil
}

// checkPluginFiles reports whether the installed files are the ones the row
// records: a mismatch is an update that crashed between replacing the files and
// writing the row, or files changed behind our back.
func (s *Service) checkPluginFiles(name string, r pluginRow) error {
	dir := filepath.Join(s.pluginsDir(), name)
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return errors.New("plugin.json is missing; reinstall the plugin")
	}
	if _, err := plugins.ParseManifest(data, name); err != nil {
		return fmt.Errorf("%w; reinstall the plugin", err)
	}
	if h, err := plugins.HashDir(dir); err != nil || h != r.sourceHash {
		return errors.New("the installed files do not match this version (an update was interrupted?); reinstall the plugin")
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

// ListPlugins lists the installed plugins, including ones that cannot run and
// directories in DataDir/plugins an install left without a row.
func (s *Service) ListPlugins(ctx context.Context) ([]PluginInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.pluginRows(ctx, "")
	if err != nil {
		return nil, err
	}
	out := []PluginInfo{}
	// ponytail: hashes every plugin's files per call (20 MB cap each); cache by mtime if lists get slow.
	for n, r := range rows {
		out = append(out, s.pluginInfo(n, r))
	}
	ents, err := os.ReadDir(s.pluginsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range ents {
		if _, ok := rows[e.Name()]; ok || !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info := PluginInfo{Name: e.Name(), Error: "the installation did not finish; reinstall the plugin"}
		if data, err := os.ReadFile(filepath.Join(s.pluginsDir(), e.Name(), "plugin.json")); err != nil {
			info.Error = "plugin.json is missing; reinstall the plugin"
		} else if m, err := plugins.ParseManifest(data, e.Name()); err != nil {
			info.Error = fmt.Sprintf("%v; reinstall the plugin", err)
		} else {
			info.Version, info.Permissions, info.Config = m.Version, m.Permissions, m.Config
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InstallPlugin installs or updates the plugin in srcDir: it snapshots the
// directory, builds the snapshot (a plugin that does not build is not installed)
// and swaps it in as DataDir/plugins/<name>. An update with other permissions
// is disabled until they are granted; with the same ones a running plugin
// restarts on the new code.
func (s *Service) InstallPlugin(ctx context.Context, srcDir string) (PluginInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return PluginInfo{}, err
	}
	defer end()
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	if err := os.MkdirAll(s.pluginsDir(), 0o700); err != nil {
		return PluginInfo{}, err
	}
	staging, err := os.MkdirTemp(s.pluginsDir(), ".incoming-")
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
	s.stopPlugin(m.Name)
	if err := plugins.Replace(snap, filepath.Join(s.pluginsDir(), m.Name)); err != nil {
		return PluginInfo{}, err
	}
	// A crash before this row is written leaves files that do not match it:
	// startPlugins disables the plugin and ListPlugins says so.
	_, err = s.db.Execute(ctx, `INSERT INTO plugins(name, version, manifest, source_hash, permission_hash, installed_at)
		VALUES (?, ?, ?, ?, ?, unixepoch())
		ON CONFLICT(name) DO UPDATE SET version = excluded.version, manifest = excluded.manifest,
			source_hash = excluded.source_hash, permission_hash = excluded.permission_hash,
			enabled = enabled AND permission_hash = excluded.permission_hash,
			disabled_reason = '', installed_at = excluded.installed_at`,
		m.Name, m.Version, string(mj), hash, m.PermissionHash())
	if err != nil {
		return PluginInfo{}, err
	}
	rows, err := s.pluginRows(ctx, m.Name)
	if err != nil {
		return PluginInfo{}, err
	}
	r := rows[m.Name]
	if r.enabled {
		s.loadPlugin(m.Name, r)
	}
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	return s.pluginInfo(m.Name, r), nil
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
		if _, err := s.db.Execute(ctx, "UPDATE plugins SET enabled = 1, failures = 0, disabled_reason = '' WHERE name = ?", name); err != nil {
			return err
		}
		s.loadPlugin(name, r)
	}
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
	return nil
}

// UninstallPlugin stops the plugin and removes its files and row (with its
// config, storage and cursor), or the files an unfinished install left.
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

// startPlugins runs at New: it removes leftovers of interrupted installs and
// starts the enabled plugins, disabling those whose files do not match their row.
func (s *Service) startPlugins(ctx context.Context) error {
	ents, err := os.ReadDir(s.pluginsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") { // .incoming-*, .snapshot-*, .old-*
			if err := os.RemoveAll(filepath.Join(s.pluginsDir(), e.Name())); err != nil {
				return err
			}
		}
	}
	rows, err := s.pluginRows(ctx, "")
	if err != nil {
		return err
	}
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	for n, r := range rows {
		if !r.enabled {
			continue
		}
		if s.checkPluginFiles(n, r) != nil {
			if _, err := s.db.Execute(ctx, "UPDATE plugins SET enabled = 0, disabled_reason = ? WHERE name = ?", disabledFiles, n); err != nil {
				return err
			}
			continue
		}
		s.loadPlugin(n, r)
	}
	return nil
}

// loadPlugin (re)starts an enabled plugin on its installed code; stopPlugin
// stops it. Both are called with pluginsMu held.
// ponytail: records the running code's hash only; Task 5 starts and stops the VM here.
func (s *Service) loadPlugin(name string, r pluginRow) {
	if s.running == nil {
		s.running = map[string]string{}
	}
	s.running[name] = r.sourceHash
}

func (s *Service) stopPlugin(name string) { delete(s.running, name) }

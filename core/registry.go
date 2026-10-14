package core

// The plugin registry's source (docs/superpowers/specs/2026-10-14-native-plugins-design.md
// 2.4, 3.1, 3.5, 3.7): a registry.json signed with ed25519, fetched over
// https, kept in the data directory and verified again on every read.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// RegistrySource is where the registry comes from and the key it is signed with.
type RegistrySource struct {
	URL       string // registry.json; its signature is URL + ".sig"
	PublicKey ed25519.PublicKey
	Client    *http.Client // nil: a client with registryTimeout
}

const (
	registryURL = "https://dl.godwit.email/plugins/registry.json"
	// registryPublicKey verifies registry.json (base64 ed25519). Its private key
	// is the plugins repository's secret GODWIT_REGISTRY_KEY; rotating it
	// needs a Godwit release (docs/registry/README.md).
	registryPublicKey = "bm291vHdLh6gATDFvUsm3tQ1aWd0qGZOjxfsnC79drs="

	maxRegistry     = 4 << 20
	maxRegistrySig  = 1 << 10
	registryTimeout = 5 * time.Minute
	registryEvery   = 24 * time.Hour
)

// registryFirst is how long after start the first background refresh waits.
var registryFirst = time.Minute

// DefaultRegistry is Godwit's own registry, nil while it has no key.
var DefaultRegistry = func() *RegistrySource {
	key, err := base64.StdEncoding.DecodeString(registryPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil
	}
	return &RegistrySource{URL: registryURL, PublicKey: key}
}()

var (
	ErrRegistryOff       = errors.New("no plugin registry is configured")
	ErrRegistrySignature = errors.New("the plugin registry's signature does not match")
	ErrRegistryStale     = errors.New("the plugin registry is older than the one Godwit has")
	ErrRegistryMismatch  = errors.New("the downloaded file does not match the plugin registry; it was discarded")

	errRegistryTooLarge = errors.New("larger than the plugin registry allows")
)

// RegistryView is what the extensions page shows of the registry. Stale is
// why the last refresh failed ("" once one succeeds); the entries are then
// the cached registry's.
type RegistryView struct {
	On      bool
	Updated time.Time
	Stale   string
	Entries []RegistryEntry
}

// RegistryEntry is one registry plugin for this build: Version is the
// release it would install ("" none, Unavailable says why: "platform", "abi"
// or "godwit:<min>"); Installed and InstalledTrust describe what is installed
// under its name; Update is a newer listed release for a listed install.
type RegistryEntry struct {
	Name                                            string
	Title, Summary                                  plugins.Text
	Homepage                                        string
	Native                                          bool
	Version, Unavailable, Installed, InstalledTrust string
	Update                                          bool
}

// registryCache is the cached registry with its signature, in one file so a
// failed write never pairs a registry with another one's signature.
func (s *Service) registryCache() string { return filepath.Join(s.dataDir, "plugin-registry.cache") }

type cachedRegistryFile struct{ Body, Sig []byte }

// RefreshPluginRegistry fetches the registry, verifies it and, if it is not
// older than the cached one, caches it and applies it to the installed
// plugins: files it no longer lists stop (trust_revoked), and files the
// user allowed that it lists become listed. On any failure the cache and
// the plugins stay as they were.
func (s *Service) RefreshPluginRegistry(ctx context.Context) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	src := s.opts.Registry
	if src == nil {
		return ErrRegistryOff
	}
	err = s.refreshRegistry(ctx, src)
	s.registryMu.Lock()
	s.registryErr = ""
	if err != nil {
		s.registryErr = err.Error()
	}
	s.registryMu.Unlock()
	return err
}

func (s *Service) refreshRegistry(ctx context.Context, src *RegistrySource) error {
	body, err := fetchRegistryFile(ctx, src, src.URL, maxRegistry)
	if err != nil {
		return err
	}
	sig, err := fetchRegistryFile(ctx, src, src.URL+".sig", maxRegistrySig)
	if err != nil {
		return err
	}
	reg, err := verifyRegistry(src.PublicKey, body, sig)
	if err != nil {
		return err
	}
	s.registryMu.Lock() // one refresh writes the cache at a time
	defer s.registryMu.Unlock()
	if old, err := s.cachedRegistry(src); err == nil && reg.Updated.Before(old.Updated) {
		return ErrRegistryStale
	}
	// Applied first: a failure keeps the old cache, and the next refresh tries again.
	if err := s.applyRegistry(ctx, reg); err != nil {
		return err
	}
	b, _ := json.Marshal(cachedRegistryFile{Body: body, Sig: sig})
	return store.WriteFileAtomic(s.registryCache(), b)
}

func fetchRegistryFile(ctx context.Context, src *RegistrySource, url string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("plugin registry: %s is not https", url)
	}
	c := src.Client
	if c == nil {
		c = &http.Client{Timeout: registryTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plugin registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin registry: %s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("plugin registry: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("plugin registry: %s: %w (%d bytes)", url, errRegistryTooLarge, limit)
	}
	return b, nil
}

func verifyRegistry(pub ed25519.PublicKey, body, sig []byte) (plugins.Registry, error) {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || !ed25519.Verify(pub, body, raw) {
		return plugins.Registry{}, ErrRegistrySignature
	}
	return plugins.ParseRegistry(body)
}

// cachedRegistry reads and verifies the cached registry; fs.ErrNotExist when there is none.
func (s *Service) cachedRegistry(src *RegistrySource) (plugins.Registry, error) {
	b, err := os.ReadFile(s.registryCache())
	if err != nil {
		return plugins.Registry{}, err
	}
	var c cachedRegistryFile
	if err := json.Unmarshal(b, &c); err != nil {
		return plugins.Registry{}, ErrRegistrySignature
	}
	return verifyRegistry(src.PublicKey, c.Body, c.Sig)
}

// ListPluginRegistry is the cached registry as this build sees it.
func (s *Service) ListPluginRegistry(ctx context.Context) (RegistryView, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return RegistryView{}, err
	}
	defer end()
	src := s.opts.Registry
	if src == nil {
		return RegistryView{}, nil
	}
	s.registryMu.Lock()
	v := RegistryView{On: true, Stale: s.registryErr, Entries: []RegistryEntry{}}
	reg, err := s.cachedRegistry(src)
	s.registryMu.Unlock()
	if errors.Is(err, fs.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return RegistryView{}, err
	}
	s.pluginsMu.RLock()
	rows, err := s.pluginRows(ctx, "")
	s.pluginsMu.RUnlock()
	if err != nil {
		return RegistryView{}, err
	}
	v.Updated = reg.Updated
	for _, p := range reg.Plugins {
		e := RegistryEntry{Name: p.Name, Title: p.Title, Summary: p.Summary, Homepage: p.Homepage}
		rel, _, why := p.Pick(plugins.PlatformKey(), s.opts.AppVersion)
		e.Version, e.Unavailable = rel.Version, why
		if why == "" {
			e.Native = rel.Parsed.IsNative()
		} else if len(p.Releases) > 0 {
			e.Native = p.Releases[0].Parsed.IsNative()
		}
		if r, ok := rows[p.Name]; ok {
			e.Installed, e.InstalledTrust = r.m.Version, r.trust
			e.Update = r.trust == "listed" && e.Version != "" && plugins.CompareVersions(e.Version, r.m.Version) > 0
		}
		v.Entries = append(v.Entries, e)
	}
	return v, nil
}

// applyRegistry revokes listed files reg no longer lists and promotes
// user-allowed files it lists at their version (spec 3.5, 3.7).
func (s *Service) applyRegistry(ctx context.Context, reg plugins.Registry) error {
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	rows, err := s.pluginRows(ctx, "")
	if err != nil {
		return err
	}
	byName := map[string]plugins.RegistryPlugin{}
	for _, p := range reg.Plugins {
		byName[p.Name] = p
	}
	changed := false
	for name, r := range rows {
		listed := func(version string) bool {
			key := plugins.PlatformKey()
			if !r.m.IsNative() {
				key = plugins.AnyPlatform
			}
			for _, rel := range byName[name].Releases {
				if a, ok := rel.Artifacts[key]; ok && r.libraryHash != "" && a.SHA256 == r.libraryHash && (version == "" || rel.Version == version) {
					return true
				}
			}
			return false
		}
		switch {
		case r.trust == "listed" && !listed(""):
			if _, err := s.db.Execute(ctx, "UPDATE plugins SET enabled = 0, disabled_reason = ?, granted_hash = '', trust = '' WHERE name = ?", disabledRevoked, name); err != nil {
				return err
			}
			s.stopPlugin(name)
			changed = true
		case r.trust == "user" && listed(r.m.Version) && s.isReviewedPackage(name, r, byName[name]):
			if _, err := s.db.Execute(ctx, "UPDATE plugins SET trust = 'listed' WHERE name = ?", name); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		s.bus.Publish(events.Event{Type: events.PluginsChanged})
	}
	return nil
}

// isReviewedPackage reports whether the installed files of r are exactly a
// release of p at r's version: its manifest and its one file, nothing else.
func (s *Service) isReviewedPackage(name string, r pluginRow, p plugins.RegistryPlugin) bool {
	i := slices.IndexFunc(p.Releases, func(rel plugins.RegistryRelease) bool { return rel.Version == r.m.Version })
	if i < 0 {
		return false
	}
	installed, _ := json.Marshal(r.m)
	reviewed, _ := json.Marshal(p.Releases[i].Parsed)
	if !bytes.Equal(installed, reviewed) {
		return false
	}
	file := plugins.LibraryName()
	if !r.m.IsNative() {
		file = r.m.Main
	}
	ents, err := os.ReadDir(s.versionDir(name, r.installDir))
	if err != nil {
		return false
	}
	var names []string
	for _, e := range ents {
		if e.Name() != plugins.BuildDir {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	want := []string{"plugin.json", file}
	slices.Sort(want)
	return slices.Equal(names, want)
}

// startRegistryRefresh refreshes the registry registryFirst after start and
// then every registryEvery, until Close. Failures show as RegistryView.Stale.
func (s *Service) startRegistryRefresh() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		t := time.NewTimer(registryFirst)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s.RefreshPluginRegistry(ctx)
			t.Reset(registryEvery)
		}
	}()
}

// InstallPluginFromRegistry installs or updates plugin name from the cached
// registry: the newest release this build can take, its file downloaded and
// checked against the registry's size and SHA-256 (a mismatch is
// ErrRegistryMismatch and nothing is installed), then installed as listed.
func (s *Service) InstallPluginFromRegistry(ctx context.Context, name string) (PluginInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return PluginInfo{}, err
	}
	defer end()
	src := s.opts.Registry
	if src == nil {
		return PluginInfo{}, ErrRegistryOff
	}
	s.registryMu.Lock()
	reg, err := s.cachedRegistry(src)
	s.registryMu.Unlock()
	if errors.Is(err, fs.ErrNotExist) {
		return PluginInfo{}, errors.New("the plugin registry has not been read yet")
	}
	if err != nil {
		return PluginInfo{}, err
	}
	i := slices.IndexFunc(reg.Plugins, func(p plugins.RegistryPlugin) bool { return p.Name == name })
	if i < 0 {
		return PluginInfo{}, fmt.Errorf("plugin %s is not in the plugin registry", name)
	}
	rel, art, why := reg.Plugins[i].Pick(plugins.PlatformKey(), s.opts.AppVersion)
	if why != "" {
		return PluginInfo{}, fmt.Errorf("plugin %s cannot be installed here (%s)", name, why)
	}
	if err := os.MkdirAll(s.pluginsDir(), 0o700); err != nil {
		return PluginInfo{}, err
	}
	dir, err := os.MkdirTemp(s.pluginsDir(), ".download-") // removed at start if this dies
	if err != nil {
		return PluginInfo{}, err
	}
	defer os.RemoveAll(dir)
	file := plugins.LibraryName()
	if !rel.Parsed.IsNative() {
		file = rel.Parsed.Main
	}
	if err := downloadArtifact(ctx, src, art, filepath.Join(dir, file)); err != nil {
		return PluginInfo{}, fmt.Errorf("plugin %s %s: %w", name, rel.Version, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), rel.Manifest, 0o600); err != nil {
		return PluginInfo{}, err
	}
	return s.installPlugin(ctx, dir, "listed")
}

// downloadArtifact writes a's file to path if it is exactly a.Size bytes with a.SHA256.
func downloadArtifact(ctx context.Context, src *RegistrySource, a plugins.Artifact, path string) error {
	b, err := fetchRegistryFile(ctx, src, a.URL, a.Size)
	if err != nil {
		if errors.Is(err, errRegistryTooLarge) {
			return ErrRegistryMismatch
		}
		return err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != a.Size || hex.EncodeToString(sum[:]) != a.SHA256 {
		return ErrRegistryMismatch
	}
	return os.WriteFile(path, b, 0o600)
}

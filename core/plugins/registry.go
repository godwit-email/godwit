package plugins

// The plugin registry (docs/adr/0016-signed-plugin-registry.md):
// reviewed releases, each with its manifest and the exact files it
// ships. This file is the format; where it comes from and its signature are
// core's.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/skills"
)

// Platforms are the artifact keys of native releases; a script release has only AnyPlatform.
var Platforms = []string{"darwin-arm64", "darwin-amd64", "windows-amd64", "windows-arm64", "linux-amd64", "linux-arm64"}

const AnyPlatform = "any"

type Registry struct {
	Schema  int              `json:"schema"`
	Updated time.Time        `json:"updated"`
	Plugins []RegistryPlugin `json:"plugins"`
}

type RegistryPlugin struct {
	Name     string            `json:"name"`
	Title    Text              `json:"title"`
	Summary  Text              `json:"summary"`
	Homepage string            `json:"homepage,omitempty"`
	Releases []RegistryRelease `json:"releases"`
	// Unsupported are the releases this build cannot read, with only their
	// Version and MinGodwit: Pick's reason when none of Releases fits.
	Unsupported []RegistryRelease `json:"-"`
}

type RegistryRelease struct {
	Version   string              `json:"version"`
	MinGodwit string              `json:"minGodwit,omitempty"`
	Manifest  json.RawMessage     `json:"manifest"`
	Artifacts map[string]Artifact `json:"artifacts"`
	Assets    []Asset             `json:"assets,omitempty"` // native only; see assets.go
	Parsed    Manifest            `json:"-"`                // Manifest, validated
}

// Artifact is one released file: exactly Size bytes with this SHA-256.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseRegistry parses and validates a registry.json as this build reads it.
// The format grows without a new schema: fields this build does not know are
// ignored outside releases, and a release it cannot read whole (new fields,
// a new capability, or invalid) goes to Unsupported instead of failing the
// registry, so older clients keep the rest of it, revocations included.
func ParseRegistry(data []byte) (Registry, error) { return parseRegistry(data, false) }

// CheckRegistry is ParseRegistry for publishing: an unknown field anywhere or
// a release this build cannot read is an error.
func CheckRegistry(data []byte) (Registry, error) { return parseRegistry(data, true) }

func parseRegistry(data []byte, strict bool) (Registry, error) {
	var raw struct {
		Schema  int       `json:"schema"`
		Updated time.Time `json:"updated"`
		Plugins []struct {
			Name     string            `json:"name"`
			Title    Text              `json:"title"`
			Summary  Text              `json:"summary"`
			Homepage string            `json:"homepage,omitempty"`
			Releases []json.RawMessage `json:"releases"`
		} `json:"plugins"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(&raw); err != nil {
		return Registry{}, fmt.Errorf("registry: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Registry{}, errors.New("registry: data after the registry")
	}
	if raw.Schema != 1 {
		return Registry{}, fmt.Errorf("registry: schema %d is not 1", raw.Schema)
	}
	r := Registry{Schema: raw.Schema, Updated: raw.Updated, Plugins: make([]RegistryPlugin, 0, len(raw.Plugins))}
	seen := map[string]bool{}
	for _, rp := range raw.Plugins {
		if !skills.ValidName(rp.Name) || seen[rp.Name] {
			return Registry{}, fmt.Errorf("registry: plugin name %q is invalid or repeated", rp.Name)
		}
		seen[rp.Name] = true
		if rp.Homepage != "" && !strings.HasPrefix(rp.Homepage, "https://") {
			return Registry{}, fmt.Errorf("registry: %s: homepage must be https", rp.Name)
		}
		p := RegistryPlugin{Name: rp.Name, Title: rp.Title, Summary: rp.Summary, Homepage: rp.Homepage}
		versions := map[string]bool{}
		for _, b := range rp.Releases {
			rel, err := parseRelease(rp.Name, b)
			if err != nil && strict {
				return Registry{}, fmt.Errorf("registry: %s %s: %w", rp.Name, rel.Version, err)
			}
			if err != nil && !parsableVersion(rel.Version) {
				continue
			}
			if versions[rel.Version] {
				return Registry{}, fmt.Errorf("registry: %s: version %s is repeated", rp.Name, rel.Version)
			}
			versions[rel.Version] = true
			if err != nil {
				p.Unsupported = append(p.Unsupported, rel)
				continue
			}
			p.Releases = append(p.Releases, rel)
		}
		r.Plugins = append(r.Plugins, p)
	}
	return r, nil
}

// parseRelease reads one release whole. On error it returns only the
// release's version and, if it is one, its minGodwit.
func parseRelease(name string, b json.RawMessage) (RegistryRelease, error) {
	var rel RegistryRelease
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	err := dec.Decode(&rel)
	if err == nil {
		err = checkRelease(name, &rel)
	}
	if err == nil {
		return rel, nil
	}
	var head struct {
		Version   string `json:"version"`
		MinGodwit string `json:"minGodwit"`
	}
	json.Unmarshal(b, &head)
	if !parsableVersion(head.MinGodwit) {
		head.MinGodwit = ""
	}
	return RegistryRelease{Version: head.Version, MinGodwit: head.MinGodwit}, err
}

func checkRelease(name string, rel *RegistryRelease) error {
	if !parsableVersion(rel.Version) { // releases are ordered by it
		return fmt.Errorf("version %q is not a dotted number version", rel.Version)
	}
	m, err := ParseManifest(rel.Manifest, name)
	if err != nil {
		return err
	}
	if m.Version != rel.Version {
		return fmt.Errorf("the manifest says version %s", m.Version)
	}
	if rel.MinGodwit != "" && !parsableVersion(rel.MinGodwit) {
		return fmt.Errorf("minGodwit %q is not a version", rel.MinGodwit)
	}
	lim := ScriptLimits
	if m.IsNative() {
		lim = NativeLimits
		if len(rel.Artifacts) == 0 {
			return errors.New("a native release needs an artifact")
		}
		for k := range rel.Artifacts {
			if !slices.Contains(Platforms, k) {
				return fmt.Errorf("artifact %q is not a platform", k)
			}
		}
	} else {
		if _, ok := rel.Artifacts[AnyPlatform]; !ok || len(rel.Artifacts) != 1 {
			return errors.New(`a script release has one artifact, "any"`)
		}
		if strings.Contains(m.Main, "/") || !strings.HasSuffix(m.Main, ".js") {
			return fmt.Errorf("main %q must be one bundled .js file", m.Main)
		}
		if len(rel.Assets) > 0 {
			return errors.New("a script release has no assets")
		}
	}
	if err := checkAssets(rel.Assets); err != nil {
		return err
	}
	for k, a := range rel.Artifacts {
		switch {
		case !strings.HasPrefix(a.URL, "https://"):
			return fmt.Errorf("%s: the url must be https", k)
		case !sha256RE.MatchString(a.SHA256):
			return fmt.Errorf("%s: sha256 must be 64 lowercase hex digits", k)
		case a.Size <= 0 || a.Size > lim.File:
			return fmt.Errorf("%s: size must be 1 to %d bytes", k, lim.File)
		}
	}
	rel.Parsed = m
	return nil
}

// PlatformKey is this build's artifact key.
func PlatformKey() string { return runtime.GOOS + "-" + runtime.GOARCH }

// Pick returns the newest release this build can install on platform, and
// its file; reason, when there is none, is why the newest release does not
// fit: "abi", "platform", "godwit:<minGodwit>", or "unsupported" for one
// this build cannot read. appVersion "" or "dev" (a development build) takes
// any minGodwit.
func (p RegistryPlugin) Pick(platform, appVersion string) (RegistryRelease, Artifact, string) {
	rel, a, reason := p.pick(platform, appVersion)
	if reason == "" {
		return rel, a, ""
	}
	var newest *RegistryRelease
	for i, u := range p.Unsupported {
		if newest == nil || CompareVersions(u.Version, newest.Version) > 0 {
			newest = &p.Unsupported[i]
		}
	}
	if newest != nil && !slices.ContainsFunc(p.Releases, func(r RegistryRelease) bool { return CompareVersions(r.Version, newest.Version) >= 0 }) {
		reason = "unsupported"
		if newest.MinGodwit != "" && appVersion != "" && appVersion != "dev" && CompareVersions(appVersion, newest.MinGodwit) < 0 {
			reason = "godwit:" + newest.MinGodwit
		}
	}
	return rel, a, reason
}

func (p RegistryPlugin) pick(platform, appVersion string) (RegistryRelease, Artifact, string) {
	rels := slices.Clone(p.Releases)
	slices.SortFunc(rels, func(a, b RegistryRelease) int { return CompareVersions(b.Version, a.Version) })
	reason := "platform"
	for i, rel := range rels {
		why := ""
		key := platform
		if !rel.Parsed.IsNative() {
			key = AnyPlatform
		}
		a, ok := rel.Artifacts[key]
		switch {
		case rel.Parsed.IsNative() && rel.Parsed.Native.ABI != NativeABI:
			why = "abi"
		case !ok:
			why = "platform"
		case rel.MinGodwit != "" && appVersion != "" && appVersion != "dev" && CompareVersions(appVersion, rel.MinGodwit) < 0:
			why = "godwit:" + rel.MinGodwit
		}
		if why == "" {
			return rel, a, ""
		}
		if i == 0 {
			reason = why
		}
	}
	return RegistryRelease{}, Artifact{}, reason
}

// CompareVersions orders versions like 1.2.10 and v1.2.0-beta: numeric dot
// parts (missing ones are 0), then a release after its prereleases, which
// compare as strings.
func CompareVersions(a, b string) int {
	na, pa := splitVersion(a)
	nb, pb := splitVersion(b)
	for i := range max(len(na), len(nb)) {
		x, y := part(na, i), part(nb, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	return strings.Compare(pa, pb)
}

func splitVersion(v string) ([]string, string) {
	v = strings.TrimPrefix(v, "v")
	num, pre, _ := strings.Cut(v, "-")
	return strings.Split(num, "."), pre
}

func part(ps []string, i int) int {
	if i >= len(ps) {
		return 0
	}
	n, _ := strconv.Atoi(ps[i])
	return n
}

func parsableVersion(v string) bool {
	num, _ := splitVersion(v)
	for _, p := range num {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

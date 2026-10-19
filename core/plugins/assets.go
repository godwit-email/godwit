package plugins

// Registry-hosted assets of a native release (docs/superpowers/specs/2026-10-17-plugin-model-provider-design.md
// 2, 7): large files such as a model's weights or an inference engine's
// libraries, which Godwit downloads and checks itself; the plugin only ever
// gets the verified directories. This file is their format.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core/skills"
)

// Asset is a group of files a release needs, in one or more variants (a
// model in several sizes); Platforms empty means every platform.
type Asset struct {
	Name      string         `json:"name"`
	Platforms []string       `json:"platforms,omitempty"`
	Variants  []AssetVariant `json:"variants"`
}

// AssetVariant is one choice of an asset: its files, and the memory a
// machine needs to use it.
type AssetVariant struct {
	ID          string            `json:"id"`
	MinMemoryGB MemoryRequirement `json:"minMemoryGB,omitempty"`
	Files       []AssetFile       `json:"files"`
}

// AssetFile is one file of a variant: exactly Size bytes with this SHA-256,
// saved under Name.
type AssetFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// maxAssetFile caps one asset file: model weights, not a library.
const maxAssetFile = 64 << 30

// MemoryRequirement is the physical memory, in GB, a variant needs: by
// "default", an exact platform ("windows-arm64") or an OS ("windows-*"). The
// registry may give a plain number for every platform.
type MemoryRequirement map[string]float64

func (m *MemoryRequirement) UnmarshalJSON(b []byte) error {
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*m = MemoryRequirement{"default": n}
		return nil
	}
	var mm map[string]float64
	if err := json.Unmarshal(b, &mm); err != nil {
		return errors.New("minMemoryGB must be a number or an object of numbers")
	}
	*m = mm
	return nil
}

// For is the requirement on platform: exact platform, then its OS, then
// default; 0 when there is none.
func (m MemoryRequirement) For(platform string) float64 {
	os, _, _ := strings.Cut(platform, "-")
	for _, k := range []string{platform, os + "-*", "default"} {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return 0
}

func (m MemoryRequirement) check() error {
	if len(m) == 0 {
		return nil
	}
	if _, ok := m["default"]; !ok {
		return errors.New(`minMemoryGB needs a "default"`)
	}
	for k, v := range m {
		os, rest, _ := strings.Cut(k, "-")
		if k != "default" && !slices.Contains(Platforms, k) && !(rest == "*" && slices.ContainsFunc(Platforms, func(p string) bool { return strings.HasPrefix(p, os+"-") })) {
			return fmt.Errorf("minMemoryGB key %q is not default, a platform or <os>-*", k)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return fmt.Errorf("minMemoryGB %s must be a positive number", k)
		}
	}
	return nil
}

// AssetsFor is the assets of rel that apply on platform.
func (rel RegistryRelease) AssetsFor(platform string) []Asset {
	var out []Asset
	for _, a := range rel.Assets {
		if len(a.Platforms) == 0 || slices.Contains(a.Platforms, platform) {
			out = append(out, a)
		}
	}
	return out
}

// AssetsDigest identifies a set of asset descriptions: a release's assets on
// one platform. Installed plugins keep it, so a registry that changes or
// drops any of them (even a variant never downloaded) revokes the release.
func AssetsDigest(as []Asset) string {
	if len(as) == 0 {
		return "" // as installed plugins without assets recorded it
	}
	b, _ := json.Marshal(as)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func checkAssets(as []Asset) error {
	perPlatform := map[string]bool{} // name + platform: one asset of a name on each platform
	for _, a := range as {
		if !skills.ValidName(a.Name) {
			return fmt.Errorf("asset name %q is invalid", a.Name)
		}
		plats := a.Platforms
		if len(plats) == 0 {
			plats = Platforms
		}
		for i, p := range plats {
			if !slices.Contains(Platforms, p) || slices.Contains(plats[:i], p) && len(a.Platforms) > 0 {
				return fmt.Errorf("asset %s: platform %q is unknown or repeated", a.Name, p)
			}
			if perPlatform[a.Name+" "+p] {
				return fmt.Errorf("asset %s is given twice for %s", a.Name, p)
			}
			perPlatform[a.Name+" "+p] = true
		}
		if len(a.Variants) == 0 {
			return fmt.Errorf("asset %s has no variants", a.Name)
		}
		ids := map[string]bool{}
		for _, v := range a.Variants {
			if !skills.ValidName(v.ID) || ids[v.ID] {
				return fmt.Errorf("asset %s: variant %q is invalid or repeated", a.Name, v.ID)
			}
			ids[v.ID] = true
			if err := v.MinMemoryGB.check(); err != nil {
				return fmt.Errorf("asset %s %s: %w", a.Name, v.ID, err)
			}
			if len(v.Files) == 0 {
				return fmt.Errorf("asset %s %s has no files", a.Name, v.ID)
			}
			names := map[string]bool{} // case-folded: macOS and Windows disks ignore case
			for _, f := range v.Files {
				where := fmt.Sprintf("asset %s %s: %s", a.Name, v.ID, f.Name)
				switch {
				case !portableFileName(f.Name) || names[strings.ToLower(f.Name)]:
					return fmt.Errorf("%s: the name must be one file name every platform can create, not repeated", where)
				case !strings.HasPrefix(f.URL, "https://"):
					return fmt.Errorf("%s: the url must be https", where)
				case !sha256RE.MatchString(f.SHA256):
					return fmt.Errorf("%s: sha256 must be 64 lowercase hex digits", where)
				case f.Size <= 0 || f.Size > maxAssetFile:
					return fmt.Errorf("%s: size must be 1 to %d bytes", where, int64(maxAssetFile))
				}
				names[strings.ToLower(f.Name)] = true
			}
		}
	}
	return nil
}

// windowsReserved are device names Windows will not create a file as, with any extension.
var windowsReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9¹²³]|lpt[0-9¹²³])(\..*)?$`)

// portableFileName: one file name that macOS, Linux and Windows can all create.
func portableFileName(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, `/\:*?"<>|`) &&
		!strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 }) &&
		!strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ") && !windowsReserved.MatchString(name)
}

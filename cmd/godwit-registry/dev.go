package main

// godwit-registry dev: a local registry for trying registry-only flows (a
// plugin's hosted model files) in a development build, signed with a key of
// its own. Production builds ignore GODWIT_DEV_REGISTRY.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
)

// devSpec is what dev publishes; relative dirs are from the spec's directory.
type devSpec struct {
	Plugins []struct {
		Name    string       `json:"name"`
		Title   plugins.Text `json:"title"`
		Summary plugins.Text `json:"summary"`
		Dir     string       `json:"dir"` // the built plugin: plugin.json and this platform's library
		Assets  []struct {
			Name      string   `json:"name"`
			Platforms []string `json:"platforms,omitempty"`
			Variants  []struct {
				ID          string                    `json:"id"`
				MinMemoryGB plugins.MemoryRequirement `json:"minMemoryGB,omitempty"`
				Dir         string                    `json:"dir"` // every file in it, symlinks followed
			} `json:"variants"`
		} `json:"assets"`
	} `json:"plugins"`
}

// devRegistry builds the registry spec describes for this platform and
// serves it with its files on addr until ctx ends. Its key and
// source.json (what GODWIT_DEV_REGISTRY names) are kept in
// <spec dir>/.dev-registry, so a restart serves the same registry to Godwit.
func devRegistry(ctx context.Context, specPath, addr string, stdout io.Writer) error {
	b, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	var spec devSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return fmt.Errorf("%s: %w", specPath, err)
	}
	base, err := filepath.Abs(filepath.Dir(specPath))
	if err != nil {
		return err
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	state := filepath.Join(base, ".dev-registry")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	key, err := devKey(filepath.Join(state, "key"))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	files := map[string]string{} // URL path -> local file
	docs := map[string][]byte{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := docs[r.URL.Path]; ok {
			w.Write(b)
		} else if f, ok := files[r.URL.Path]; ok {
			http.ServeFile(w, r, f) // with Range, for resumed downloads
		} else {
			http.NotFound(w, r)
		}
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.StartTLS() // httptest's fixed certificate for 127.0.0.1, so source.json's CA stays valid
	defer srv.Close()

	// serve publishes local under urlPath and describes it.
	serve := func(urlPath, local string) (plugins.Artifact, error) {
		f, err := os.Open(local)
		if err != nil {
			return plugins.Artifact{}, err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return plugins.Artifact{}, err
		}
		files[urlPath] = local // r.URL.Path is unescaped, as this is
		parts := strings.Split(urlPath, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i]) // a # or % in a file name stays in the path
		}
		return plugins.Artifact{URL: srv.URL + strings.Join(parts, "/"), SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
	}
	platform := plugins.PlatformKey()
	var out []map[string]any
	for _, p := range spec.Plugins {
		manifest, err := os.ReadFile(filepath.Join(abs(p.Dir), "plugin.json"))
		if err != nil {
			return err
		}
		var m struct{ Version string }
		if err := json.Unmarshal(manifest, &m); err != nil {
			return fmt.Errorf("%s plugin.json: %w", p.Name, err)
		}
		lib, err := serve(path.Join("/files", p.Name, m.Version, plugins.LibraryName()), filepath.Join(abs(p.Dir), plugins.LibraryName()))
		if err != nil {
			return err
		}
		var assets []plugins.Asset
		for _, a := range p.Assets {
			if len(a.Platforms) > 0 && !slices.Contains(a.Platforms, platform) {
				continue // only this platform's files are served, so two of one name never share a URL
			}
			asset := plugins.Asset{Name: a.Name, Platforms: a.Platforms}
			for _, v := range a.Variants {
				variant := plugins.AssetVariant{ID: v.ID, MinMemoryGB: v.MinMemoryGB}
				entries, err := os.ReadDir(abs(v.Dir))
				if err != nil {
					return err
				}
				for _, e := range entries {
					local := filepath.Join(abs(v.Dir), e.Name())
					if st, err := os.Stat(local); err != nil || !st.Mode().IsRegular() {
						continue
					}
					fmt.Fprintf(stdout, "hashing %s\n", local)
					f, err := serve(path.Join("/files", p.Name, m.Version, a.Name, v.ID, e.Name()), local)
					if err != nil {
						return err
					}
					variant.Files = append(variant.Files, plugins.AssetFile{Name: e.Name(), URL: f.URL, SHA256: f.SHA256, Size: f.Size})
				}
				asset.Variants = append(asset.Variants, variant)
			}
			assets = append(assets, asset)
		}
		out = append(out, map[string]any{"name": p.Name, "title": p.Title, "summary": p.Summary, "releases": []any{map[string]any{
			"version": m.Version, "manifest": json.RawMessage(manifest),
			"artifacts": map[string]any{platform: lib}, "assets": assets}}})
	}
	// Now, so it is never older than the registry Godwit cached from a previous run.
	reg, err := json.Marshal(map[string]any{"schema": 1, "updated": time.Now().UTC().Format(time.RFC3339), "plugins": out})
	if err != nil {
		return err
	}
	if _, err := plugins.CheckRegistry(reg); err != nil {
		return err
	}
	docs["/registry.json"] = reg
	docs["/registry.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, reg)) + "\n")

	source, _ := json.Marshal(map[string]string{
		"url":       srv.URL + "/registry.json",
		"publicKey": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
		"ca":        string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})),
	})
	sourcePath := filepath.Join(state, "source.json")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "serving %s\nrun the app with: GODWIT_DEV_REGISTRY=%s mise run dev\n", srv.URL+"/registry.json", sourcePath)
	<-ctx.Done()
	return nil
}

// devKey is the signing key kept at p, made on first use.
func devKey(p string) (ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(p); err == nil {
		seed, err := base64.StdEncoding.DecodeString(string(b))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not a key; delete it to make a new one", p)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return key, os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key.Seed())), 0o600)
}

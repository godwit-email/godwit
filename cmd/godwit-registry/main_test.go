package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registryFile writes a registry with one script plugin whose file is at url with sum.
func registryFile(t *testing.T, url, sum string, size int) string {
	t.Helper()
	reg := map[string]any{"schema": 1, "updated": "2026-10-14T08:00:00Z", "plugins": []any{map[string]any{
		"name": "hello", "title": "Hello", "summary": "Says hello",
		"releases": []any{map[string]any{"version": "0.1.0",
			"manifest":  map[string]any{"name": "hello", "version": "0.1.0", "main": "index.js", "permissions": map[string]any{}},
			"artifacts": map[string]any{"any": map[string]any{"url": url, "sha256": sum, "size": size}}}}}}}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func call(args ...string) (int, string, string) {
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String(), errw.String()
}

func TestKeygenSignVerify(t *testing.T) {
	code, out, _ := call("keygen")
	if code != 0 {
		t.Fatal("keygen failed")
	}
	var pub, priv string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, _ := strings.Cut(line, ": ")
		switch k {
		case "public":
			pub = v
		case "private":
			priv = v
		}
	}
	t.Setenv("GODWIT_REGISTRY_KEY", priv)
	reg := registryFile(t, "https://example.com/index.js", strings.Repeat("a", 64), 10)
	if code, _, errw := call("sign", reg); code != 0 {
		t.Fatalf("sign: %s", errw)
	}
	body, _ := os.ReadFile(reg)
	sig, _ := os.ReadFile(reg + ".sig")
	key, _ := base64.StdEncoding.DecodeString(pub)
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if !ed25519.Verify(key, body, raw) {
		t.Fatal("the signature does not verify with the generated public key")
	}
}

func TestCheckRejects(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "registry.json")
	os.WriteFile(bad, []byte(`{"schema":2,"updated":"2026-10-14T08:00:00Z","plugins":[]}`), 0o600)
	if code, _, errw := call("check", bad); code != 1 || !strings.Contains(errw, "schema") {
		t.Fatalf("code %d, %q", code, errw)
	}
	good := registryFile(t, "https://example.com/index.js", strings.Repeat("a", 64), 10)
	if code, _, errw := call("check", good); code != 0 {
		t.Fatalf("a valid registry fails: %s", errw)
	}
}

func TestCheckFetch(t *testing.T) {
	file := []byte("export default function () {}")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(file) }))
	defer srv.Close()
	old := client
	client = srv.Client()
	defer func() { client = old }()
	sum := sha256.Sum256(file)
	good := registryFile(t, srv.URL+"/index.js", hex.EncodeToString(sum[:]), len(file))
	if code, _, errw := call("check", "--fetch", good); code != 0 {
		t.Fatalf("matching file fails: %s", errw)
	}
	bad := registryFile(t, srv.URL+"/index.js", strings.Repeat("b", 64), len(file))
	if code, _, errw := call("check", "--fetch", bad); code != 1 || !strings.Contains(errw, "sha256") {
		t.Fatalf("code %d, %q", code, errw)
	}
}

func TestSignWithoutKey(t *testing.T) {
	t.Setenv("GODWIT_REGISTRY_KEY", "")
	reg := registryFile(t, "https://example.com/index.js", strings.Repeat("a", 64), 10)
	if code, _, errw := call("sign", reg); code != 1 || !strings.Contains(errw, "GODWIT_REGISTRY_KEY") {
		t.Fatalf("code %d, %q", code, errw)
	}
}

func TestCheckFetchAssets(t *testing.T) {
	lib, weights := []byte("lib"), []byte("weights")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lib" {
			w.Write(lib)
			return
		}
		w.Write(weights)
	}))
	defer srv.Close()
	old := client
	client = srv.Client()
	defer func() { client = old }()
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	write := func(weightsSHA string) string {
		manifest := `{"name":"dlx","version":"0.1.0","native":{"abi":1},"capabilities":["translate"]}`
		reg := fmt.Sprintf(`{"schema":1,"updated":"2026-10-14T08:00:00Z","plugins":[{"name":"dlx","title":"DLX","summary":"s","releases":[
			{"version":"0.1.0","manifest":%s,"artifacts":{"darwin-arm64":{"url":"%s/lib","sha256":"%s","size":%d}},
			 "assets":[{"name":"model","variants":[{"id":"small","files":[{"name":"model.gguf","url":"%s/w","sha256":"%s","size":%d}]}]}]}]}]}`,
			manifest, srv.URL, hash(lib), len(lib), srv.URL, weightsSHA, len(weights))
		path := filepath.Join(t.TempDir(), "registry.json")
		os.WriteFile(path, []byte(reg), 0o600)
		return path
	}
	if code, out, errw := call("check", "--fetch", write(hash(weights))); code != 0 || !strings.Contains(out, "model small model.gguf: ok") {
		t.Fatalf("code %d, %q, %q", code, out, errw)
	}
	if code, _, errw := call("check", "--fetch", write(strings.Repeat("b", 64))); code != 1 || !strings.Contains(errw, "model.gguf") {
		t.Fatalf("code %d, %q", code, errw)
	}
}

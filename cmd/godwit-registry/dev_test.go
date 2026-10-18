package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
)

func TestDevRegistry(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o700)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugin/plugin.json", `{"name":"local","version":"0.1.0","native":{"abi":1},"capabilities":["models"]}`)
	write("plugin/"+plugins.LibraryName(), "library")
	write("small/model.gguf", "0123456789")
	write("spec.json", `{"plugins":[{"name":"local","title":"Local","summary":"Local models","dir":"plugin",
		"assets":[{"name":"model","variants":[{"id":"small","minMemoryGB":4,"dir":"small"}]}]}]}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- devRegistry(ctx, filepath.Join(dir, "spec.json"), "127.0.0.1:0", io.Discard) }()
	var src struct{ URL, PublicKey, CA string }
	for deadline := time.Now().Add(10 * time.Second); ; {
		if b, err := os.ReadFile(filepath.Join(dir, ".dev-registry", "source.json")); err == nil && json.Unmarshal(b, &src) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no source.json")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(src.CA))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	get := func(url, rng string) []byte {
		req, _ := http.NewRequest("GET", url, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	reg := get(src.URL, "")
	sig, _ := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(get(src.URL+".sig", ""))))
	key, _ := base64.StdEncoding.DecodeString(src.PublicKey)
	if !ed25519.Verify(key, reg, sig) {
		t.Fatal("the signature does not verify")
	}
	r, err := plugins.CheckRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	rel := r.Plugins[0].Releases[0]
	f := rel.Assets[0].Variants[0].Files[0]
	if rel.Version != "0.1.0" || f.Name != "model.gguf" || f.Size != 10 || rel.Assets[0].Variants[0].MinMemoryGB.For("default") != 4 {
		t.Fatalf("release %+v", rel)
	}
	if got := string(get(f.URL, "bytes=4-")); got != "456789" {
		t.Fatalf("a resumed download got %q", got)
	}
	if got := string(get(rel.Artifacts[plugins.PlatformKey()].URL, "")); got != "library" {
		t.Fatalf("the library is %q", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

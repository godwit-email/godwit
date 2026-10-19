package main

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
)

// registrySource is Godwit's own registry or, in a development build with
// GODWIT_DEV_REGISTRY set, the local one whose source.json that names
// (godwit-registry dev). Production builds ignore the variable.
func registrySource() (*core.RegistrySource, error) {
	p := os.Getenv("GODWIT_DEV_REGISTRY")
	if p == "" || !mygo.IsDev() {
		return core.DefaultRegistry, nil
	}
	return devRegistrySource(p)
}

func devRegistrySource(p string) (*core.RegistrySource, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("GODWIT_DEV_REGISTRY: %w", err)
	}
	var s struct{ URL, PublicKey, CA string }
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("GODWIT_DEV_REGISTRY: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(s.PublicKey)
	pool := x509.NewCertPool()
	if err != nil || len(key) != ed25519.PublicKeySize || !pool.AppendCertsFromPEM([]byte(s.CA)) {
		return nil, errors.New("GODWIT_DEV_REGISTRY: not a godwit-registry dev source.json")
	}
	// No timeout: the same client downloads model files, which watch for stalls themselves.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{RootCAs: pool}}}
	return &core.RegistrySource{URL: s.URL, PublicKey: key, Client: client}, nil
}

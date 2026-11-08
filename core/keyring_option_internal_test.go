package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// mapKeyring is a Keyring in memory: the shape a shell's own keychain wrapper
// (SecItem on iOS) has from core's side.
type mapKeyring struct {
	mu  sync.Mutex
	m   map[string]string
	ops []string
}

func (k *mapKeyring) key(service, user string) string { return service + "\x00" + user }

func (k *mapKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ops = append(k.ops, "get")
	v, ok := k.m[k.key(service, user)]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (k *mapKeyring) Set(service, user, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ops = append(k.ops, "set")
	k.m[k.key(service, user)] = secret
	return nil
}

func (k *mapKeyring) Delete(service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ops = append(k.ops, "delete")
	delete(k.m, k.key(service, user))
	return nil
}

// TestOptionsKeyring: a Keyring given in Options takes every keychain call,
// and a missing entry reads as ErrSecretNotFound, not as an unavailable keychain.
func TestOptionsKeyring(t *testing.T) {
	get, set, del := keyringGet, keyringSet, keyringDelete
	t.Cleanup(func() { keyringGet, keyringSet, keyringDelete = get, set, del })

	k := &mapKeyring{m: map[string]string{}}
	ctx := context.Background()
	s, err := New(ctx, Options{DataDir: t.TempDir(), Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	k.mu.Lock()
	k.ops = nil
	k.mu.Unlock()
	if err := s.KeychainStatus(ctx); err != nil {
		t.Fatalf("KeychainStatus through Options.Keyring: %v", err)
	}
	k.mu.Lock()
	got := strings.Join(k.ops, ",")
	k.mu.Unlock()
	if got != "set,get,delete" {
		t.Fatalf("keychain calls = %q, want set,get,delete", got)
	}

	_, err = keychainGet("nobody")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing entry: err = %v, want ErrSecretNotFound", err)
	}
	if errors.Is(err, ErrKeychainUnavailable) {
		t.Fatal("a missing entry must not read as an unavailable keychain")
	}
}

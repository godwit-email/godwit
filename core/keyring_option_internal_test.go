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
	preserveKeyring(t)

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

func preserveKeyring(t *testing.T) {
	t.Helper()
	get, set, del := keyringGet, keyringSet, keyringDelete
	previous := customKeyring.Swap(nil)
	t.Cleanup(func() {
		keyringGet, keyringSet, keyringDelete = get, set, del
		customKeyring.Store(previous)
	})
}

func TestOptionsKeyringKeepsFirst(t *testing.T) {
	for _, name := range []string{"nil", "replacement", "failed"} {
		t.Run(name, func(t *testing.T) {
			preserveKeyring(t)
			ctx := context.Background()
			first := &mapKeyring{m: map[string]string{}}
			s, err := New(ctx, Options{DataDir: t.TempDir(), Keyring: first})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := keychainSet("retained", "original-secret"); err != nil {
				t.Fatal(err)
			}

			second := &mapKeyring{m: map[string]string{}}
			opts := Options{DataDir: t.TempDir()}
			if name != "nil" {
				opts.Keyring = second
			}
			if name == "failed" {
				opts.DataDir = ""
			}
			other, err := New(ctx, opts)
			if other != nil {
				defer other.Close()
			}
			if (err != nil) != (name == "failed") {
				t.Fatalf("second New: %v", err)
			}

			if got, err := keychainGet("retained"); err != nil || got != "original-secret" {
				t.Fatalf("first keyring read: got %q, err %v", got, err)
			}
			if err := keychainSet("retained", "updated-secret"); err != nil {
				t.Fatal(err)
			}
			if got, err := first.Get(keyringService, "retained"); err != nil || got != "updated-secret" {
				t.Fatalf("first keyring write: got %q, err %v", got, err)
			}
			if err := keychainDelete("retained"); err != nil {
				t.Fatal(err)
			}
			if _, err := first.Get(keyringService, "retained"); !errors.Is(err, ErrSecretNotFound) {
				t.Fatalf("first keyring delete: %v", err)
			}
			second.mu.Lock()
			calls := len(second.ops)
			second.mu.Unlock()
			if calls != 0 {
				t.Fatalf("replacement keyring received %d calls", calls)
			}
		})
	}
}

func TestKeyringConcurrentInitialization(t *testing.T) {
	preserveKeyring(t)
	k := &mapKeyring{m: map[string]string{}}
	keyringGet, keyringSet, keyringDelete = k.Get, k.Set, k.Delete

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			for range 100 {
				setKeyring(k)
			}
		})
	}
	for _, user := range []string{"reader-a", "reader-b", "reader-c"} {
		wg.Go(func() {
			<-start
			for range 100 {
				if err := keychainSet(user, user+"-secret"); err != nil {
					t.Error(err)
					return
				}
				if got, err := keychainGet(user); err != nil || got != user+"-secret" {
					t.Errorf("concurrent read: got %q, err %v", got, err)
					return
				}
				if err := keychainDelete(user); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
}

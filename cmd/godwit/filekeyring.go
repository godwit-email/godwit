package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/godwit-email/godwit/core"
)

// The two variables that, set together, keep credentials in an encrypted file
// instead of the system keychain: for a server with no desktop session or
// Secret Service. One without the other is an error, never a fallback.
const (
	keyringFileEnv    = "GODWIT_KEYRING_FILE"
	keyringKeyFileEnv = "GODWIT_KEYRING_KEY_FILE"
)

// keyringFromEnv is the file keyring the environment asks for, or nil (the
// system keychain) when it asks for none.
func keyringFromEnv() (core.Keyring, error) {
	path, keyPath := os.Getenv(keyringFileEnv), os.Getenv(keyringKeyFileEnv)
	if path == "" && keyPath == "" {
		return nil, nil
	}
	if path == "" || keyPath == "" {
		return nil, fmt.Errorf("%s and %s go together: set both or neither", keyringFileEnv, keyringKeyFileEnv)
	}
	k, err := openFileKeyring(path, keyPath)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// fileKeyring is a core.Keyring in one file: a JSON map of "service\x00user"
// to secret, sealed with AES-256-GCM as nonce || ciphertext. Every call reads
// the file, so a failed write leaves nothing stale in memory.
type fileKeyring struct {
	mu   sync.Mutex
	path string
	aead cipher.AEAD
}

// openFileKeyring reads the key (64 hex characters, from a file no wider than
// 0600) and the store, if it exists yet, so a wrong key fails at start.
func openFileKeyring(path, keyPath string) (*fileKeyring, error) {
	fi, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyringKeyFileEnv, err)
	}
	if perm := fi.Mode().Perm(); perm&^0o600 != 0 {
		return nil, fmt.Errorf("%s: %s has mode %04o; it must be 0600 or narrower", keyringKeyFileEnv, keyPath, perm)
	}
	b, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyringKeyFileEnv, err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s: %s must hold 32 bytes as 64 hex characters (openssl rand -hex 32)", keyringKeyFileEnv, keyPath)
	}
	block, _ := aes.NewCipher(key) // 32 bytes: never fails
	aead, _ := cipher.NewGCM(block)
	k := &fileKeyring{path: path, aead: aead}
	if _, err := k.load(); err != nil {
		return nil, err
	}
	return k, nil
}

func entryKey(service, user string) string { return service + "\x00" + user }

func (k *fileKeyring) load() (map[string]string, error) {
	b, err := os.ReadFile(k.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyringFileEnv, err)
	}
	n := k.aead.NonceSize()
	if len(b) < n {
		return nil, fmt.Errorf("%s: %s is not a Godwit keyring", keyringFileEnv, k.path)
	}
	plain, err := k.aead.Open(nil, b[:n], b[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %s cannot be decrypted: wrong key or changed file", keyringFileEnv, k.path)
	}
	m := map[string]string{}
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", keyringFileEnv, err)
	}
	return m, nil
}

// save replaces the store atomically: a temporary file beside it (CreateTemp
// makes it 0600), synced, then renamed over it.
func (k *fileKeyring) save(m map[string]string) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	nonce := make([]byte, k.aead.NonceSize())
	rand.Read(nonce)
	f, err := os.CreateTemp(filepath.Dir(k.path), ".godwit-keyring-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // a no-op once renamed
	if _, err := f.Write(k.aead.Seal(nonce, nonce, plain, nil)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), k.path)
}

func (k *fileKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return "", err
	}
	v, ok := m[entryKey(service, user)]
	if !ok {
		return "", core.ErrSecretNotFound
	}
	return v, nil
}

func (k *fileKeyring) Set(service, user, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	m[entryKey(service, user)] = secret
	return k.save(m)
}

// Delete of a missing entry is ErrSecretNotFound, as go-keyring's is.
func (k *fileKeyring) Delete(service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	if _, ok := m[entryKey(service, user)]; !ok {
		return core.ErrSecretNotFound
	}
	delete(m, entryKey(service, user))
	return k.save(m)
}

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/uiclient"
)

const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func writeKey(t *testing.T, hexKey string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte(hexKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileKeyringRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring")
	keyPath := writeKey(t, testKeyHex)
	k, err := openFileKeyring(path, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("godwit", "a"); !errors.Is(err, core.ErrSecretNotFound) {
		t.Fatalf("missing entry: %v", err)
	}
	if err := k.Delete("godwit", "a"); !errors.Is(err, core.ErrSecretNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	if err := k.Set("godwit", "a", "hunter2-secret"); err != nil {
		t.Fatal(err)
	}
	if err := k.Set("godwit", "b", "other"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("store: %v %v", fi.Mode(), err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "hunter2") {
		t.Fatal("secret stored in plain text")
	}
	k2, err := openFileKeyring(path, keyPath) // a new process reads what the last wrote
	if err != nil {
		t.Fatal(err)
	}
	if v, err := k2.Get("godwit", "a"); err != nil || v != "hunter2-secret" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := k2.Delete("godwit", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("godwit", "a"); !errors.Is(err, core.ErrSecretNotFound) {
		t.Fatalf("deleted entry: %v", err)
	}
	if v, err := k.Get("godwit", "b"); err != nil || v != "other" {
		t.Fatalf("other entry = %q, %v", v, err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Fatalf("temporary files left: %v", ents)
	}
}

func TestFileKeyringWrongKeyAndTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring")
	keyPath := writeKey(t, testKeyHex)
	k, err := openFileKeyring(path, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Set("godwit", "a", "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := openFileKeyring(path, writeKey(t, strings.Repeat("ff", 32))); err == nil || !strings.Contains(err.Error(), "cannot be decrypted") {
		t.Fatalf("wrong key: %v", err)
	}
	b, _ := os.ReadFile(path)
	b[len(b)-1] ^= 1
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("godwit", "a"); err == nil || errors.Is(err, core.ErrSecretNotFound) {
		t.Fatalf("tampered file: %v", err)
	}
	if err := k.Set("godwit", "c", "x"); err == nil {
		t.Fatal("Set over a tampered file replaced it")
	}
}

func TestFileKeyringKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring")
	if _, err := openFileKeyring(path, writeKey(t, "abcd")); err == nil {
		t.Fatal("short key accepted")
	}
	if runtime.GOOS == "windows" {
		return // no Unix modes
	}
	keyPath := writeKey(t, testKeyHex)
	if err := os.Chmod(keyPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := openFileKeyring(path, keyPath); err == nil || !strings.Contains(err.Error(), "0640") {
		t.Fatalf("group-readable key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := openFileKeyring(path, keyPath); err != nil {
		t.Fatalf("0400 key: %v", err)
	}
}

func TestKeyringFromEnv(t *testing.T) {
	t.Setenv(keyringFileEnv, "")
	t.Setenv(keyringKeyFileEnv, "")
	if k, err := keyringFromEnv(); k != nil || err != nil {
		t.Fatalf("unset: %v %v", k, err)
	}
	t.Setenv(keyringFileEnv, filepath.Join(t.TempDir(), "keyring"))
	if _, err := keyringFromEnv(); err == nil {
		t.Fatal("only the file set: no error")
	}
	t.Setenv(keyringFileEnv, "")
	t.Setenv(keyringKeyFileEnv, writeKey(t, testKeyHex))
	if _, err := keyringFromEnv(); err == nil {
		t.Fatal("only the key set: no error")
	}
}

// The backend process (--headless, which the interface also starts) keeps
// credentials in the file the environment names, and refuses to start on half
// of it.
func TestHeadlessUsesFileKeyring(t *testing.T) {
	for _, uiStarted := range []bool{false, true} {
		t.Run(map[bool]string{false: "headless", true: "ui-started"}[uiStarted], func(t *testing.T) {
			dir := shortDir(t)
			path := filepath.Join(t.TempDir(), "keyring")
			keyPath := writeKey(t, testKeyHex)
			args := []string{"--headless", "--data-dir", dir}
			if uiStarted {
				args = append(args, "--"+uiStartedFlag)
			}
			env := func(keyFile string) []string {
				return append(os.Environ(), "GODWIT_TEST_MAIN=1", "GODWIT_HEADLESS_DESKTOP=0", keyringFileEnv+"="+path, keyringKeyFileEnv+"="+keyFile)
			}

			half := exec.Command(os.Args[0], args...)
			half.Env = env("")
			if out, err := half.CombinedOutput(); err == nil || !strings.Contains(string(out), keyringKeyFileEnv) {
				t.Fatalf("half configured: %v %s", err, out)
			}

			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = env(keyPath)
			var stderr syncBuf
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			var c *uiclient.Client // as the interface reaches it, without starting a backend of its own
			waitCond(t, "the backend to answer", func() bool {
				var err error
				c, err = uiclient.Dial(dir, "")
				return err == nil
			})
			defer c.Close()
			if err := c.KeychainStatus(context.Background()); err != nil {
				t.Fatalf("KeychainStatus: %v; stderr %q", err, stderr.String())
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the backend wrote no store (%v); stderr %q", err, stderr.String())
			}
			k, err := openFileKeyring(path, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			if m, err := k.load(); err != nil || len(m) != 0 {
				t.Fatalf("probe left behind: %v %v", m, err)
			}
		})
	}
}

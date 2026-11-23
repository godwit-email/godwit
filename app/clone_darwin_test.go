package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A clone of a 0600 cache file gets the mode copyFile gives a copy.
func TestCloneFileModeAsCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cache")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	cloned, copied := filepath.Join(dir, "cloned"), filepath.Join(dir, "copied")
	if err := cloneFile(src, cloned); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, copied); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(cloned)
	b, _ := os.Stat(copied)
	if a.Mode().Perm() != b.Mode().Perm() {
		t.Fatalf("clone %v, copy %v", a.Mode().Perm(), b.Mode().Perm())
	}
}

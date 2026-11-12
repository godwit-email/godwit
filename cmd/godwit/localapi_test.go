package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/ownerlock"
)

func TestPlatformDataDir(t *testing.T) {
	t.Setenv("MYGO_ENV", "production")
	def, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir, custom := (&platform{dataDir: def}).DataDir(); dir != def || custom {
		t.Errorf("default: %q, %v", dir, custom)
	}
	other := shortDir(t)
	if dir, custom := (&platform{dataDir: other}).DataDir(); dir != other || !custom {
		t.Errorf("custom: %q, %v", dir, custom)
	}
}

func TestSaveDirPersistence(t *testing.T) {
	dataDir := t.TempDir()
	downloads := filepath.Join(t.TempDir(), "Downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	chosen := filepath.Join(t.TempDir(), "Invoices")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}
	newPlat := func() *platform {
		return &platform{
			dataDir:      dataDir,
			downloadsDir: func() (string, error) { return downloads, nil },
		}
	}

	p1 := newPlat()
	if got, want := p1.saveDefaultPath("report.pdf"), filepath.Join(downloads, "report.pdf"); got != want {
		t.Fatalf("initial saveDefaultPath = %q, want %q", got, want)
	}
	if got := p1.defaultSaveDir(); got != downloads {
		t.Fatalf("initial defaultSaveDir = %q, want %q", got, downloads)
	}

	p1.rememberSaveDir(chosen)
	if got, want := p1.saveDefaultPath("next.pdf"), filepath.Join(chosen, "next.pdf"); got != want {
		t.Fatalf("remembered saveDefaultPath = %q, want %q", got, want)
	}

	// A new platform instance on the same dataDir restores the saved directory.
	p2 := newPlat()
	if got, want := p2.saveDefaultPath("restarted.pdf"), filepath.Join(chosen, "restarted.pdf"); got != want {
		t.Fatalf("persisted saveDefaultPath = %q, want %q", got, want)
	}
	abs := filepath.Join(downloads, "explicit.pdf")
	if got := p2.saveDefaultPath(abs); got != abs {
		t.Fatalf("absolute saveDefaultPath = %q, want %q", got, abs)
	}

	// Removing the remembered directory falls back to Downloads cleanly.
	if err := os.RemoveAll(chosen); err != nil {
		t.Fatal(err)
	}
	if got := p2.defaultSaveDir(); got != downloads {
		t.Fatalf("fallback defaultSaveDir = %q, want %q", got, downloads)
	}
}

// The core.New failure dialog names a class of reason, never err's text.
func TestOpenFailure(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	i18n.SetLanguage("zh-CN")
	secret := "/Users/someone/secret path"
	perm := openFailure(&os.PathError{Op: "open", Path: secret, Err: fs.ErrPermission})
	other := openFailure(fmt.Errorf("migrate %s: boom", secret))
	if perm != "Godwit 无法打开数据目录：没有访问权限。" || other != "Godwit 无法打开数据目录：数据库无法打开。" {
		t.Fatalf("perm %q, other %q", perm, other)
	}
	for _, m := range []string{perm, other} {
		if strings.Contains(m, secret) || strings.Contains(m, "boom") {
			t.Fatalf("%q carries the error's text", m)
		}
	}
}

// Only a GUI on the default data directory takes mygo's single-instance
// lock (a second one focuses the first); one on another directory runs
// beside it, guarded by that directory's owner lock alone.
func TestSingleInstanceOnlyOnDefaultDir(t *testing.T) {
	t.Setenv("MYGO_ENV", "production")
	def, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if !singleInstance(def) || !singleInstance(def+string(filepath.Separator)) {
		t.Error("the default directory does not take the single-instance lock")
	}
	dir := shortDir(t)
	if singleInstance(dir) {
		t.Error("a custom directory takes the single-instance lock")
	}
	// The owner lock still keeps a second GUI off that directory.
	release, err := ownerlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ownerlock.Acquire(dir); !errors.Is(err, ownerlock.ErrAnotherOwner) {
		t.Fatalf("second owner: %v", err)
	}
	if _, code := lockFailure(ownerlock.ErrAnotherOwner); code != 2 {
		t.Fatalf("exit code %d", code)
	}
}

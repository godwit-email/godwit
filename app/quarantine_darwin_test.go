package app

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCopiedAttachmentIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "cached"), filepath.Join(dir, "invoice.pdf")
	if err := os.WriteFile(src, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Getxattr(dst, "com.apple.quarantine", nil); err != nil {
		t.Errorf("no quarantine flag: %v", err)
	}
}

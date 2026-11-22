package app

import (
	"os"

	"golang.org/x/sys/unix"
)

// umask is the process's, read once at start: setting it is the only way to read it.
var umask = func() os.FileMode { m := unix.Umask(0); unix.Umask(m); return os.FileMode(m) }()

// cloneFile copies an attachment out of the cache, marked as from the
// internet, as an APFS clone that takes no more disk; copyFile where the
// volume cannot clone.
func cloneFile(src, dst string) error {
	if unix.Clonefile(src, dst, 0) != nil {
		return copyFile(src, dst)
	}
	if err := os.Chmod(dst, 0o666&^umask); err != nil { // the clone has the cache's 0600; copyFile's mode instead
		os.Remove(dst)
		return err
	}
	_ = markFromInternet(dst) // best effort, as copyFile
	return nil
}

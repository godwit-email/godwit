package app

import "golang.org/x/sys/unix"

// cloneFile copies an attachment out of the cache, marked as from the
// internet, as an APFS clone that takes no more disk; copyFile where the
// volume cannot clone.
func cloneFile(src, dst string) error {
	if unix.Clonefile(src, dst, 0) != nil {
		return copyFile(src, dst)
	}
	_ = markFromInternet(dst) // best effort, as copyFile
	return nil
}

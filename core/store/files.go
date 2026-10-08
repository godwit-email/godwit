package store

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data via a same-directory temp file, fsync and rename.
func WriteFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// CleanOrphans removes leftover .tmp-* files and any regular file in dir
// (recursively) for which keep returns false.
func CleanOrphans(dir string, keep func(path string) bool) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if len(d.Name()) >= 5 && d.Name()[:5] == ".tmp-" || !keep(p) {
			return os.Remove(p)
		}
		return nil
	})
}

package store

import (
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic writes data via a same-directory temp file, fsync and rename,
// creating missing parent directories. When it returns nil the file, its name
// and any directory it created survive a crash.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := mkdirAllSync(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
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
		return err
	}
	return syncDir(dir)
}

// mkdirAllSync is os.MkdirAll(dir, 0o700) that also fsyncs the parent of each
// directory it creates, so the new entries are durable.
func mkdirAllSync(dir string) error {
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return nil
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirAllSync(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return syncDir(parent)
}

// syncDir fsyncs a directory, making renames and creations in it durable. A
// variable so tests can watch it. Windows cannot fsync a directory (opening one
// for sync is "Access is denied"); NTFS journals the rename itself.
var syncDir = func(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
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

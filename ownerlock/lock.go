// Package ownerlock makes one process the owner of a data directory.
package ownerlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrAnotherOwner: another process holds the data directory's lock.
var ErrAnotherOwner = errors.New("another process owns the data directory")

// Acquire creates dataDir (0700) and takes an exclusive, non-blocking lock on
// <dataDir>/godwit.lock; it fails with ErrAnotherOwner if someone holds it.
// On Unix the directory must belong to the current user and is tightened to
// 0700; on Windows it relies on the user directory's default ACL, and the ACL
// of a custom data directory is not checked. The lock is released by release
// or when the process exits, however it exits.
func Acquire(dataDir string) (release func() error, err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := checkOwner(dataDir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "godwit.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	if err := tighten(dataDir); err != nil {
		unlock(f)
		f.Close()
		return nil, err
	}
	return func() error { return errors.Join(unlock(f), f.Close()) }, nil
}

func wrap(err error) error { return fmt.Errorf("ownerlock: %w", err) }

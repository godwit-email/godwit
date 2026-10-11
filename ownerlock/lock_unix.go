//go:build unix

package ownerlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func checkOwner(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("ownerlock: %s belongs to another user (uid %d)", dir, st.Uid)
	}
	return nil
}

func tighten(dir string) error { return os.Chmod(dir, 0o700) }

func lock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrAnotherOwner
	}
	if err != nil {
		return wrap(err)
	}
	return nil
}

func unlock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

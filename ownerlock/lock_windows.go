package ownerlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func checkOwner(string) error { return nil }
func tighten(string) error    { return nil }

func lock(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrAnotherOwner
	}
	if err != nil {
		return wrap(err)
	}
	return nil
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}

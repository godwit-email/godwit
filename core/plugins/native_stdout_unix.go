//go:build !windows

package plugins

import (
	"os"

	"golang.org/x/sys/unix"
)

// takeStdout keeps the process's stdout for the protocol and points fd 1 at
// stderr, so whatever a library prints goes to the log, not into the JSON lines.
func takeStdout() (*os.File, error) {
	fd, err := unix.Dup(1)
	if err != nil {
		return nil, err
	}
	if err := unix.Dup2(2, 1); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "plugin-host protocol"), nil
}

//go:build !windows

package plugins

import (
	"os"

	"golang.org/x/sys/unix"
)

// takeStdout keeps the process's stdout for the protocol and points fd 1 at
// stderr, so whatever a library prints goes to the log, not into the JSON lines.
// It is close-on-exec: a helper the library starts must not hold it open past the host.
func takeStdout() (*os.File, error) {
	fd, err := unix.FcntlInt(1, unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	if err := unix.Dup2(2, 1); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "plugin-host protocol"), nil
}

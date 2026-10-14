//go:build windows

package plugins

import (
	"os"

	"golang.org/x/sys/windows"
)

// takeStdout keeps the process's stdout for the protocol and makes stderr the
// standard output a library loaded later sees.
func takeStdout() (*os.File, error) {
	out, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	errh, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return nil, err
	}
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, errh); err != nil {
		return nil, err
	}
	// A helper the library starts must not hold the protocol open past the host.
	if err := windows.SetHandleInformation(out, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(out), "plugin-host protocol"), nil
}

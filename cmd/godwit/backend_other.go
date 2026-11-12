//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detachBackend runs cmd in a session of its own, so a signal to the
// interface's process group (Ctrl-C in its terminal) does not reach it; its
// log goes where the interface's does.
func detachBackend(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stderr = os.Stderr
}

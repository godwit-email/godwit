//go:build !windows

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE keeps the backend alive when the stderr it shares with the
// interface that started it is a pipe nobody reads any more: a log line
// would otherwise end it, skipping its close path. The logger drops the error.
func ignoreSIGPIPE() { signal.Ignore(syscall.SIGPIPE) }

// detachBackend runs cmd in a session of its own, so a signal to the
// interface's process group (Ctrl-C in its terminal) does not reach it; its
// log goes where the interface's does.
func detachBackend(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stderr = os.Stderr
}

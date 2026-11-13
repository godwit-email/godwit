package main

import (
	"os/exec"
	"syscall"
)

// ignoreSIGPIPE: Windows raises no SIGPIPE.
func ignoreSIGPIPE() {}

// detachedProcess is DETACHED_PROCESS: no console of the interface's.
const detachedProcess = 0x00000008

// detachBackend runs cmd in a process group of its own and without the
// interface's console, so neither its Ctrl-C nor its closing reaches it. A
// release has no stderr to share (-H windowsgui).
func detachBackend(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}

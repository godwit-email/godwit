//go:build unix

package core

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the command in a process group of its own.
func setProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// groupKiller kills the started command's whole process group.
func groupKiller(cmd *exec.Cmd) func() {
	pgid := cmd.Process.Pid
	return func() { syscall.Kill(-pgid, syscall.SIGKILL) }
}

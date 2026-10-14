//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// The plugin's process gets a group of its own, so killing it ends what it started too.
func setNativeGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// nativeKiller ends the started cmd's process group.
func nativeKiller(cmd *exec.Cmd) func() {
	pid := cmd.Process.Pid
	return func() { syscall.Kill(-pid, syscall.SIGKILL) }
}

//go:build !windows

package plugins

import (
	"os/exec"
	"sync"
	"syscall"
)

// The plugin's process gets a group of its own, so killing it ends what it started too.
func setNativeGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// nativeKiller ends the started cmd's process group, once: the first call
// wins (a timeout, or the clean exit), so a later one never hits a reused pid.
func nativeKiller(cmd *exec.Cmd) func() {
	pid := cmd.Process.Pid
	return sync.OnceFunc(func() { syscall.Kill(-pid, syscall.SIGKILL) })
}

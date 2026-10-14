//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// The plugin's process gets a group of its own, so killing it ends what it started too.
func setNativeGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killNativeGroup(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

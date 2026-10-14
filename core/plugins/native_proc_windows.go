//go:build windows

package plugins

import "os/exec"

func setNativeGroup(*exec.Cmd) {}

func killNativeGroup(cmd *exec.Cmd) { cmd.Process.Kill() }

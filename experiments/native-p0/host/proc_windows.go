//go:build windows

package main

import "os/exec"

func setGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) { cmd.Process.Kill() }

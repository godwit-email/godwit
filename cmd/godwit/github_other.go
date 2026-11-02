//go:build !windows

package main

import "os/exec"

func noConsole(*exec.Cmd) {}

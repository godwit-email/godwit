//go:build !windows

package main

import (
	"os"
	"syscall"
)

func terminatePID(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGTERM)
	}
}

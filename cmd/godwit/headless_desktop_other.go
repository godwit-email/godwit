//go:build !darwin && !windows

package main

import (
	"os"
	"runtime"
)

func hasGraphicalSession() bool {
	return runtime.GOOS == "linux" && linuxHasDisplay(os.Getenv)
}

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// noConsole keeps gh from opening a console window, as Godwit has none to share.
func noConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

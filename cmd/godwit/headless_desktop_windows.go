//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	uoiFlags   = 1
	wsfVisible = 0x0001
)

type userObjectFlags struct {
	inherit  int32
	reserved int32
	flags    uint32
}

func hasGraphicalSession() bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	winsta, _, _ := user32.NewProc("GetProcessWindowStation").Call()
	if winsta == 0 {
		return false
	}
	var f userObjectFlags
	var needed uint32
	ok, _, _ := user32.NewProc("GetUserObjectInformationW").Call(
		winsta,
		uoiFlags,
		uintptr(unsafe.Pointer(&f)),
		unsafe.Sizeof(f),
		uintptr(unsafe.Pointer(&needed)),
	)
	return ok != 0 && f.flags&wsfVisible != 0
}

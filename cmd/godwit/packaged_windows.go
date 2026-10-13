package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// packaged reports whether Godwit runs from an MSIX package, which the
// Microsoft Store updates; the app must not update itself there.
func packaged() bool {
	var n uint32
	r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentPackageFullName").Call(uintptr(unsafe.Pointer(&n)), 0)
	return r != 15700 // APPMODEL_ERROR_NO_PACKAGE; a packaged app gets ERROR_INSUFFICIENT_BUFFER
}

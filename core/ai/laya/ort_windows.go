package laya

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// openLibrary opens the DLL at path, resolving its own dependencies from its
// directory.
func openLibrary(path string) (uintptr, error) {
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
	return uintptr(h), err
}

func librarySymbol(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}

// ortPath is a path as ORTCHAR_T*: UTF-16 on Windows.
func ortPath(p string) unsafe.Pointer {
	w, err := windows.UTF16PtrFromString(p)
	if err != nil { // a NUL in the path: no file has it
		w, _ = windows.UTF16PtrFromString("")
	}
	return unsafe.Pointer(w)
}

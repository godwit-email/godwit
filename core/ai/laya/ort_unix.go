//go:build darwin || linux

package laya

import (
	"unsafe"

	"github.com/ebitengine/purego"
)

// openLibrary opens the shared library at path, which must be absolute: a
// hardened macOS runtime loads no relative path.
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

func librarySymbol(h uintptr, name string) (uintptr, error) { return purego.Dlsym(h, name) }

// ortPath is a path as ORTCHAR_T*: UTF-8 here.
func ortPath(p string) unsafe.Pointer { return unsafe.Pointer(cstring(p)) }

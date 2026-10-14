//go:build darwin || linux

package main

import "github.com/ebitengine/purego"

func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

func symbol(h uintptr, name string) (uintptr, error) { return purego.Dlsym(h, name) }

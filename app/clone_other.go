//go:build !darwin

package app

// cloneFile is copyFile: cloning is darwin's (clone_darwin.go).
func cloneFile(src, dst string) error { return copyFile(src, dst) }

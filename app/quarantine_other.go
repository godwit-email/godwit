//go:build !darwin && !windows

package app

// markFromInternet does nothing: Linux desktops have no such mark.
func markFromInternet(string) error { return nil }

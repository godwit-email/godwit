//go:build !darwin && !windows

package sysproxy

// system: elsewhere the desktop's proxy reaches apps as *_PROXY variables,
// which For reads first.
func system() settings { return settings{} }

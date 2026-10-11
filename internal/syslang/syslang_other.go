//go:build !darwin && !windows

package syslang

func osLanguage() string { return "" }

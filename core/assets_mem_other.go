//go:build !darwin && !linux && !windows

package core

func sysPhysicalMemory() uint64 { return 0 } // unknown: no requirement is enforced

func quarantined(string) bool { return false }

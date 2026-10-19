package core

import "golang.org/x/sys/unix"

func sysPhysicalMemory() uint64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return n
}

// quarantined reports the quarantine flag macOS refuses to load libraries with.
func quarantined(path string) bool {
	_, err := unix.Getxattr(path, "com.apple.quarantine", nil)
	return err == nil
}

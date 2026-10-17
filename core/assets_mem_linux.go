package core

import "golang.org/x/sys/unix"

func sysPhysicalMemory() uint64 {
	var si unix.Sysinfo_t
	if unix.Sysinfo(&si) != nil {
		return 0
	}
	return uint64(si.Totalram) * uint64(si.Unit)
}

func quarantined(string) bool { return false }

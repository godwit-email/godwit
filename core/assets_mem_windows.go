package core

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length, MemoryLoad                          uint32
	TotalPhys, AvailPhys                        uint64
	TotalPageFile, AvailPageFile                uint64
	TotalVirtual, AvailVirtual, AvailExtVirtual uint64
}

func sysPhysicalMemory() uint64 {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0
	}
	return m.TotalPhys
}

func quarantined(string) bool { return false }

//go:build darwin

package main

import (
	"sync"

	"github.com/ebitengine/purego"
)

var responsibleSym = sync.OnceValue(func() uintptr {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY)
	if err != nil {
		return 0
	}
	sym, err := purego.Dlsym(lib, "responsibility_get_pid_responsible_for_pid")
	if err != nil {
		return 0
	}
	return sym
})

func responsiblePID(pid int) int {
	sym := responsibleSym()
	if sym == 0 || pid <= 0 {
		return pid
	}
	r, _, _ := purego.SyscallN(sym, uintptr(pid))
	if got := int(int32(r)); got > 0 {
		return got
	}
	return pid
}

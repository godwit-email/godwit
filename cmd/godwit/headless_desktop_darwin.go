//go:build darwin

package main

import (
	"sync"

	"github.com/ebitengine/purego"
)

var loadSessionCheck = sync.OnceValue(func() func() bool {
	cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	var copySession func() uintptr
	var release func(uintptr)
	purego.RegisterLibFunc(&copySession, cg, "CGSessionCopyCurrentDictionary")
	purego.RegisterLibFunc(&release, cf, "CFRelease")
	return func() bool {
		dict := copySession()
		if dict == 0 {
			return false
		}
		release(dict)
		return true
	}
})

func hasGraphicalSession() bool {
	check := loadSessionCheck()
	return check != nil && check()
}

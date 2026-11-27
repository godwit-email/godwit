//go:build darwin

package main

import (
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
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

// disableAutomaticTermination keeps macOS TAL / loginwindow from terminating
// the windowless headless NSApplication process as idle.
func disableAutomaticTermination() {
	if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return
	}
	cls := objc.GetClass("NSProcessInfo")
	if cls == 0 {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := noteSend(objc.ID(objc.GetClass("NSAutoreleasePool")), "new")
	defer noteSend(pool, "drain")
	info := noteSend(objc.ID(cls), "processInfo")
	if info == 0 {
		return
	}
	noteSend(info, "disableAutomaticTermination:", noteString("Godwit headless backend"))
}

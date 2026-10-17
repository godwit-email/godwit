package main

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
)

// MyGo checks before donation; check here too before reading an avatar.
func (p *platform) notificationAvatar(n app.Notification) []byte {
	if n.Sender == "" || n.MessageID == 0 || n.ThreadID == 0 || !communicationAuthorized() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	avatar, err := p.svc.SenderAvatar(ctx, n.MessageID, false)
	if err != nil {
		return nil
	}
	var data []byte
	// The renderer shares caches with the UI, so serialize it with window drawing.
	mygo.RunOnMain(func() { data = notificationAvatarData(avatar) })
	return data
}

var loadCommunicationNotifications = sync.OnceValue(func() error {
	for _, name := range []string{"Foundation", "UserNotifications", "Intents"} {
		if _, err := purego.Dlopen("/System/Library/Frameworks/"+name+".framework/"+name, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
			return err
		}
	}
	if objc.GetClass("INSendMessageIntent") == 0 || !objc.Send[bool](objc.ID(objc.GetClass("UNMutableNotificationContent")), objc.RegisterName("instancesRespondToSelector:"), objc.RegisterName("contentByUpdatingWithProvider:error:")) {
		return errors.New("communication notifications require macOS 12")
	}
	return nil
})

var communicationAuthorized = sync.OnceValue(func() bool {
	if loadCommunicationNotifications() != nil {
		return false
	}
	security, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW)
	if err != nil {
		return false
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW)
	if err != nil {
		return false
	}
	var createTask func(uintptr) uintptr
	var copyEntitlement func(uintptr, objc.ID, uintptr) objc.ID
	var release func(uintptr)
	purego.RegisterLibFunc(&createTask, security, "SecTaskCreateFromSelf")
	purego.RegisterLibFunc(&copyEntitlement, security, "SecTaskCopyValueForEntitlement")
	purego.RegisterLibFunc(&release, cf, "CFRelease")
	task := createTask(0)
	if task == 0 {
		return false
	}
	defer release(task)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := noteSend(objc.ID(objc.GetClass("NSAutoreleasePool")), "new")
	defer noteSend(pool, "drain")
	value := copyEntitlement(task, noteString("com.apple.developer.usernotifications.communication"), 0)
	if value == 0 {
		return false
	}
	defer release(uintptr(value))
	return objc.Send[bool](value, objc.RegisterName("boolValue"))
})

func noteSend(obj objc.ID, selector string, args ...any) objc.ID {
	return obj.Send(objc.RegisterName(selector), args...)
}

func noteString(s string) objc.ID {
	str := noteSend(noteSend(objc.ID(objc.GetClass("NSString")), "alloc"), "initWithBytes:length:encoding:", unsafe.StringData(s), uintptr(len(s)), uintptr(4))
	runtime.KeepAlive(s)
	return noteSend(str, "autorelease")
}

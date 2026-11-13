package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
)

// The MSIX starts Godwit at login through the startup task msix.yml declares
// (uap5:StartupTask), not mygo's Run entry, which a package cannot use: WinRT's
// StartupTask turns it on and off, and AppInstance tells it started Godwit.
// IIDs and vtable slots follow the Windows SDK metadata (windows-rs).

// startupTaskID is the TaskId of the uap5:StartupTask in msix.yml.
const startupTaskID = "GodwitStartup"

func setOpenAtLogin(on bool) error {
	if !packaged() {
		return mygo.App.SetOpenAtLogin(on)
	}
	return withStartupTask(func(task unsafe.Pointer) error {
		if !on {
			return comCall(task, 7) // IStartupTask.Disable
		}
		var op unsafe.Pointer
		if err := comCall(task, 6, uintptr(unsafe.Pointer(&op))); err != nil { // RequestEnableAsync; no prompt for a desktop app
			return err
		}
		defer comRelease(op)
		var state int32
		return await(op, unsafe.Pointer(&state))
	})
}

func openAtLogin() bool {
	if !packaged() {
		return mygo.App.OpenAtLogin()
	}
	var state int32 // StartupTaskState
	err := withStartupTask(func(task unsafe.Pointer) error { return comCall(task, 8, uintptr(unsafe.Pointer(&state))) })
	return err == nil && (state == 2 || state == 4) // Enabled, EnabledByPolicy
}

func wasOpenedAtLogin() bool {
	if !packaged() {
		return mygo.App.WasOpenedAtLogin()
	}
	var kind int32 // ActivationKind
	err := onWinRT(func() error {
		f, err := activationFactory("Windows.ApplicationModel.AppInstance", &iidAppInstanceStatics)
		if err != nil {
			return err
		}
		defer comRelease(f)
		var args unsafe.Pointer
		if err := comCall(f, 7, uintptr(unsafe.Pointer(&args))); err != nil || args == nil { // GetActivatedEventArgs
			return err
		}
		defer comRelease(args)
		return comCall(args, 6, uintptr(unsafe.Pointer(&kind))) // IActivatedEventArgs.Kind
	})
	return err == nil && kind == 1020 // ActivationKind.StartupTask
}

var (
	iidStartupTaskStatics = windows.GUID{Data1: 0xee5b60bd, Data2: 0xa148, Data3: 0x41a7, Data4: [8]byte{0xb2, 0x6e, 0xe8, 0xb8, 0x8a, 0x1e, 0x62, 0xf8}}
	iidAppInstanceStatics = windows.GUID{Data1: 0x9d11e77f, Data2: 0x9ea6, Data3: 0x47af, Data4: [8]byte{0xa6, 0xec, 0x46, 0x78, 0x4c, 0x5b, 0xa2, 0x54}}
	iidAsyncInfo          = windows.GUID{Data1: 0x00000036, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

	combase                = windows.NewLazySystemDLL("combase.dll")
	roInitialize           = combase.NewProc("RoInitialize")
	roUninitialize         = combase.NewProc("RoUninitialize")
	roGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	windowsCreateString    = combase.NewProc("WindowsCreateString")
	windowsDeleteString    = combase.NewProc("WindowsDeleteString")
)

// withStartupTask runs fn with Godwit's StartupTask.
func withStartupTask(fn func(task unsafe.Pointer) error) error {
	return onWinRT(func() error {
		f, err := activationFactory("Windows.ApplicationModel.StartupTask", &iidStartupTaskStatics)
		if err != nil {
			return err
		}
		defer comRelease(f)
		id, err := hstring(startupTaskID)
		if err != nil {
			return err
		}
		defer windowsDeleteString.Call(id)
		var op unsafe.Pointer
		if err := comCall(f, 7, id, uintptr(unsafe.Pointer(&op))); err != nil { // GetAsync
			return err
		}
		defer comRelease(op)
		var task unsafe.Pointer
		if err := await(op, unsafe.Pointer(&task)); err != nil {
			return err
		}
		defer comRelease(task)
		return fn(task)
	})
}

// onWinRT runs fn on an OS thread of its own in the multithreaded apartment;
// the thread ends with it.
func onWinRT(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		// Never unlocked: the thread ends with the goroutine, apartment and all.
		runtime.LockOSThread()
		if r, _, _ := roInitialize.Call(1); int32(r) >= 0 { // RO_INIT_MULTITHREADED
			defer roUninitialize.Call()
		}
		done <- fn()
	}()
	return <-done
}

func activationFactory(class string, iid *windows.GUID) (unsafe.Pointer, error) {
	h, err := hstring(class)
	if err != nil {
		return nil, err
	}
	defer windowsDeleteString.Call(h)
	var f unsafe.Pointer
	if r, _, _ := roGetActivationFactory.Call(h, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&f))); int32(r) < 0 {
		return nil, fmt.Errorf("RoGetActivationFactory %s: %#x", class, uint32(r))
	}
	return f, nil
}

func hstring(s string) (uintptr, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	if r, _, _ := windowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h))); int32(r) < 0 {
		return 0, fmt.Errorf("WindowsCreateString: %#x", uint32(r))
	}
	return h, nil
}

// await waits for the IAsyncOperation op, then stores its result at out.
func await(op, out unsafe.Pointer) error {
	var info unsafe.Pointer
	if err := comCall(op, 0, uintptr(unsafe.Pointer(&iidAsyncInfo)), uintptr(unsafe.Pointer(&info))); err != nil { // QueryInterface
		return err
	}
	defer comRelease(info)
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var status int32
		if err := comCall(info, 7, uintptr(unsafe.Pointer(&status))); err != nil { // IAsyncInfo.Status
			return err
		}
		if status != 0 { // no longer Started; GetResults reports an error or cancel
			break
		}
		if time.Now().After(end) {
			return errors.New("WinRT: the operation did not finish")
		}
	}
	return comCall(op, 8, uintptr(out)) // GetResults
}

// comCall calls slot i of COM object obj's vtable, failing on a failed HRESULT.
// Callers pass out-parameters as uintptr(unsafe.Pointer(&local)): uintptrescapes
// keeps those locals alive and in place until the call returns.
//
//go:uintptrescapes
func comCall(obj unsafe.Pointer, i uintptr, args ...uintptr) error {
	fn := *(*uintptr)(unsafe.Add(*(*unsafe.Pointer)(obj), i*unsafe.Sizeof(uintptr(0))))
	if r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...); int32(r) < 0 {
		return fmt.Errorf("WinRT: %#x", uint32(r))
	}
	return nil
}

func comRelease(obj unsafe.Pointer) { _ = comCall(obj, 2) }

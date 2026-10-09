//go:build windows

package core

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func setProcessGroup(*exec.Cmd) {}

// groupKiller puts the started command in a Job Object that kills every
// process in it when terminated. If the job cannot be set up it kills the
// command's process alone. A child the command starts before it is assigned
// to the job escapes it.
func groupKiller(cmd *exec.Cmd) func() {
	one := func() { cmd.Process.Kill() }
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return one
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	var h windows.Handle
	if err == nil {
		h, err = windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	}
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err != nil {
		windows.CloseHandle(job)
		return one
	}
	return func() {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
	}
}

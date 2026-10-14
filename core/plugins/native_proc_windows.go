//go:build windows

package plugins

import (
	"context"
	"os/exec"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

func setNativeGroup(*exec.Cmd) {}

// nativeKiller ends the started cmd's process tree, as core's MCP groupKiller
// does: taskkill /T walks the tree as it is, then a Job Object kills whatever
// joined it. If the job cannot be set up, taskkill /T and killing the process remain.
func nativeKiller(cmd *exec.Cmd) func() {
	pid := cmd.Process.Pid
	tree := func() {
		ctx, cancel := context.WithTimeout(context.Background(), nativeGrace)
		defer cancel()
		exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() { tree(); cmd.Process.Kill() }
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	var h windows.Handle
	if err == nil {
		h, err = windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	}
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err != nil {
		windows.CloseHandle(job)
		return func() { tree(); cmd.Process.Kill() }
	}
	return func() {
		tree()
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
	}
}

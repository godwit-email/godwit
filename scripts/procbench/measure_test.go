package main

import (
	"testing"
	"time"
)

func TestMatchProcessesIsolatesDataDirAndIncludesHelpers(t *testing.T) {
	const dir = "/private/tmp/bench-1"
	procs := []procEntry{
		// User's production Godwit + its WebKit helper: must NEVER match.
		{PID: 38325, PPID: 1, ResponsiblePID: 38325, Args: "/Applications/Godwit.app/Contents/MacOS/Godwit", RSSBytes: 360 << 20},
		{PID: 38358, PPID: 1, ResponsiblePID: 38325, Args: "/System/Library/.../com.apple.WebKit.WebContent", RSSBytes: 40 << 20},
		// Prefix-colliding data-dir: must NOT match.
		{PID: 4100, PPID: 1, Args: "/dist/Godwit --data-dir /private/tmp/bench-10", RSSBytes: 100 << 20},
		// procbench itself and its parent `go run` carrying --data-dir: must NOT match.
		{PID: 9001, PPID: 1000, Args: "go run ./scripts/procbench --data-dir /private/tmp/bench-1", RSSBytes: 30 << 20},
		{PID: 9002, PPID: 9001, Args: "/tmp/go-build/exe/procbench --data-dir /private/tmp/bench-1", RSSBytes: 25 << 20},
		// Target Godwit processes (post-split shape: backend + UI + plugin-host + WebKit XPC + grandchild).
		{PID: 5001, PPID: 1, ResponsiblePID: 5001, Args: "/dist/Godwit --headless --data-dir /private/tmp/bench-1", RSSBytes: 45 << 20, Wakeups: 10},
		{PID: 5002, PPID: 1, ResponsiblePID: 5002, Args: "/dist/Godwit --data-dir=/private/tmp/bench-1", RSSBytes: 80 << 20, Wakeups: 20},
		{PID: 5003, PPID: 5001, ResponsiblePID: 5001, Args: "/dist/Godwit plugin-host /path/plugin", RSSBytes: 12 << 20, Wakeups: 2},
		{PID: 5004, PPID: 1, ResponsiblePID: 5002, Args: "/System/Library/.../com.apple.WebKit.WebContent", RSSBytes: 35 << 20, Wakeups: 5},
		{PID: 5005, PPID: 5004, ResponsiblePID: 5002, Args: "helper-grandchild", RSSBytes: 5 << 20, Wakeups: 1},
	}

	snap := matchProcesses(procs, dir, 9002)
	if snap.OwnCount != 3 {
		t.Fatalf("OwnCount = %d, want 3 (backend + UI + plugin-host); pids=%v", snap.OwnCount, snap.ByPID)
	}
	if snap.HelperCount != 2 {
		t.Fatalf("HelperCount = %d, want 2; pids=%v", snap.HelperCount, snap.ByPID)
	}
	wantOwnRSS := uint64((45 + 80 + 12) << 20)
	if snap.OwnRSSBytes != wantOwnRSS {
		t.Fatalf("OwnRSSBytes = %d, want %d", snap.OwnRSSBytes, wantOwnRSS)
	}
	wantTreeRSS := uint64((45 + 80 + 12 + 35 + 5) << 20)
	if snap.TreeRSSBytes != wantTreeRSS {
		t.Fatalf("TreeRSSBytes = %d, want %d", snap.TreeRSSBytes, wantTreeRSS)
	}
}

func TestParseDarwinPSAndTop(t *testing.T) {
	psOut := `
  38325     1 362640 /Applications/Godwit.app/Contents/MacOS/Godwit
   5100     1 102400 /tmp/dist/Godwit.app/Contents/MacOS/Godwit --data-dir "/private/tmp/my bench"
   5101     1  20480 /System/Library/Frameworks/WebKit.framework/com.apple.WebKit.WebContent
`
	topOut := `Processes: 512 total, 2 running, 510 sleeping, 3100 threads
Load Avg: 2.10, 2.05, 1.98
PID    MEM   IDLEW   CSW
38325  435M  15801   10742152
5100   112M+ 42+     950-
5101   24M   7       310
`
	resp := func(pid int) int {
		if pid == 5101 {
			return 5100
		}
		return pid
	}
	procs, err := parseDarwinPSAndTop(psOut, topOut, resp)
	if err != nil {
		t.Fatal(err)
	}
	snap := matchProcesses(procs, "/private/tmp/my bench", 99999)
	if snap.OwnCount != 1 || snap.HelperCount != 1 {
		t.Fatalf("counts own=%d helper=%d", snap.OwnCount, snap.HelperCount)
	}
	if snap.OwnRSSBytes != 102400*1024 {
		t.Fatalf("OwnRSSBytes = %d, want %d", snap.OwnRSSBytes, 102400*1024)
	}
	if snap.TreeRSSBytes != (102400+20480)*1024 {
		t.Fatalf("TreeRSSBytes = %d, want %d", snap.TreeRSSBytes, (102400+20480)*1024)
	}
	if snap.OwnPrivateBytes != 112<<20 {
		t.Fatalf("OwnPrivateBytes = %d, want %d", snap.OwnPrivateBytes, 112<<20)
	}
	if snap.ByPID[5100].Wakeups != 42 || snap.ByPID[5100].ContextSwitches != 950 {
		t.Fatalf("pid 5100 = %+v", snap.ByPID[5100])
	}
}

func TestParseWindowsJSONSingleAndArray(t *testing.T) {
	single := []byte(`{"PID":4200,"PPID":1000,"Name":"Godwit.exe","CommandLine":"\"C:\\App\\Godwit.exe\" --data-dir \"C:\\Temp\\gw bench\"","WorkingSet64":67108864,"PrivateBytes":50331648,"CPUMs":125.5,"Handles":410,"ContextSwitches":880}`)
	procs, err := parseWindowsJSON(single)
	if err != nil || len(procs) != 1 {
		t.Fatalf("single parse = %v, %v", procs, err)
	}
	snap := matchProcesses(procs, `C:\Temp\gw bench`, 9999)
	if snap.OwnCount != 1 || snap.OwnRSSBytes != 67108864 {
		t.Fatalf("single snap = %+v", snap)
	}

	arr := []byte(`[
		{"PID":4200,"PPID":1000,"Name":"Godwit.exe","CommandLine":"\"C:\\App\\Godwit.exe\" --data-dir \"C:\\Temp\\gw bench\"","WorkingSet64":67108864,"PrivateBytes":50331648,"CPUMs":125.5,"Handles":410,"ContextSwitches":880},
		{"PID":4201,"PPID":4200,"Name":"msedgewebview2.exe","CommandLine":"\"C:\\Edge\\msedgewebview2.exe\" --type=browser","WorkingSet64":33554432,"PrivateBytes":25165824,"CPUMs":40.0,"Handles":220,"ContextSwitches":300}
	]`)
	procs, err = parseWindowsJSON(arr)
	if err != nil {
		t.Fatal(err)
	}
	snap = matchProcesses(procs, `C:\Temp\gw bench`, 9999)
	if snap.OwnCount != 1 || snap.HelperCount != 1 {
		t.Fatalf("array counts own=%d helper=%d", snap.OwnCount, snap.HelperCount)
	}
	if snap.TreeRSSBytes != 67108864+33554432 {
		t.Fatalf("TreeRSSBytes = %d", snap.TreeRSSBytes)
	}
}

func TestDiffSnapshotsHandlesSurvivingSpawnedAndExitedPIDs(t *testing.T) {
	start := processSnapshot{
		OwnRSSBytes:  50 << 20,
		TreeRSSBytes: 80 << 20,
		ByPID: map[int]matchedProc{
			5001: {procEntry: procEntry{PID: 5001, Wakeups: 100, ContextSwitches: 1000, CPUMs: 50}, Own: true},
			5002: {procEntry: procEntry{PID: 5002, Wakeups: 40, ContextSwitches: 400, CPUMs: 20}, Own: false}, // exits before end
		},
	}
	end := processSnapshot{
		OwnCount:     1,
		HelperCount:  1,
		OwnRSSBytes:  52 << 20,
		TreeRSSBytes: 76 << 20,
		ByPID: map[int]matchedProc{
			5001: {procEntry: procEntry{PID: 5001, Wakeups: 115, ContextSwitches: 1090, CPUMs: 62.5}, Own: true},
			5003: {procEntry: procEntry{PID: 5003, Wakeups: 6, ContextSwitches: 30, CPUMs: 4.0}, Own: false}, // spawned mid-window
		},
	}
	got := diffSnapshots(start, end, 30*time.Second)
	if got.OwnWakeups != 15 || got.TreeWakeups != 21 {
		t.Fatalf("wakeups own=%d tree=%d, want 15 and 21", got.OwnWakeups, got.TreeWakeups)
	}
	if got.OwnContextSwitches != 90 || got.TreeContextSwitches != 120 {
		t.Fatalf("csw own=%d tree=%d, want 90 and 120", got.OwnContextSwitches, got.TreeContextSwitches)
	}
	if got.OwnCPUMs != 12.5 || got.TreeCPUMs != 16.5 {
		t.Fatalf("cpu own=%v tree=%v, want 12.5 and 16.5", got.OwnCPUMs, got.TreeCPUMs)
	}
}

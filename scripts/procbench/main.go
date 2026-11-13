// Command procbench measures process-level metrics of the packaged Godwit
// desktop app before and after the #714 backend process split without relying
// on internal process structure:
//   - cold start latency until the mail list frame renders
//   - background steady-state memory (RSS + OS footprint/private bytes) and
//     wakeups / context switches across all processes attached to --data-dir
//   - window reopen latency until the mail list frame renders
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ctlEndpoint struct {
	Addr  string
	Token string
}

type benchLogState struct {
	LatestCtl    ctlEndpoint
	MailReady    []int64
	WindowHidden []int64
}

func parseBenchLog(raw string) benchLogState {
	var st benchLogState
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		ts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "bench_ctl":
			var ep ctlEndpoint
			for _, f := range fields[2:] {
				if v, ok := strings.CutPrefix(f, "addr="); ok {
					ep.Addr = v
				}
				if v, ok := strings.CutPrefix(f, "token="); ok {
					ep.Token = v
				}
			}
			if ep.Addr != "" && ep.Token != "" {
				st.LatestCtl = ep
			}
		case "mail_ready":
			st.MailReady = append(st.MailReady, ts)
		case "window_hidden":
			st.WindowHidden = append(st.WindowHidden, ts)
		}
	}
	return st
}

func sendBenchCmd(ep ctlEndpoint, cmd string) error {
	if ep.Addr == "" || ep.Token == "" {
		return errors.New("missing bench control endpoint")
	}
	conn, err := net.DialTimeout("tcp", ep.Addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err = fmt.Fprintf(conn, "%s %s\n", ep.Token, cmd)
	return err
}

// triggerReopen wakes an existing hidden UI window if its control listener is
// still open (pre-split), or spawns a fresh UI process against the running
// backend if the UI process exited on window close (post-split).
func triggerReopen(ep ctlEndpoint, spawnUI func() error) error {
	if err := sendBenchCmd(ep, "open"); err == nil {
		return nil
	}
	return spawnUI()
}

func medianDuration(xs []time.Duration) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	cp := slices.Clone(xs)
	slices.Sort(cp)
	return cp[len(cp)/2]
}

func medianUint64(xs []uint64) uint64 {
	if len(xs) == 0 {
		return 0
	}
	cp := slices.Clone(xs)
	slices.Sort(cp)
	return cp[len(cp)/2]
}

func medianFloat64(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := slices.Clone(xs)
	slices.SortFunc(cp, cmp.Compare[float64])
	return cp[len(cp)/2]
}

type runResult struct {
	Run                 int     `json:"run"`
	ColdStartMs         float64 `json:"cold_start_ms"`
	ReopenMs            float64 `json:"reopen_ms"`
	BgOwnProcs          int     `json:"bg_own_procs"`
	BgHelperProcs       int     `json:"bg_helper_procs"`
	BgOwnRSSMiB         float64 `json:"bg_own_rss_mib"`
	BgTreeRSSMiB        float64 `json:"bg_tree_rss_mib"`
	BgOwnPrivateMiB     float64 `json:"bg_own_private_mib"`
	BgTreePrivateMiB    float64 `json:"bg_tree_private_mib"`
	BgOwnWakeups        uint64  `json:"bg_own_wakeups"`
	BgTreeWakeups       uint64  `json:"bg_tree_wakeups"`
	BgOwnContextSwitch  uint64  `json:"bg_own_context_switches"`
	BgTreeContextSwitch uint64  `json:"bg_tree_context_switches"`
	BgOwnCPUMs          float64 `json:"bg_own_cpu_ms"`
	BgTreeCPUMs         float64 `json:"bg_tree_cpu_ms"`
	BgWindowSec         float64 `json:"bg_window_sec"`
}

type machineInfo struct {
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	OSVersion string `json:"os_version"`
	CPU       string `json:"cpu"`
	RAMGiB    string `json:"ram_gib"`
}

type benchReport struct {
	Timestamp string      `json:"timestamp"`
	Machine   machineInfo `json:"machine"`
	Scale     seedScale   `json:"scale"`
	SettleSec float64     `json:"settle_sec"`
	WindowSec float64     `json:"window_sec"`
	Runs      []runResult `json:"runs"`
	Median    runResult   `json:"median"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMain(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "procbench:", err)
		os.Exit(1)
	}
}

func defaultBinPath() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join("dist", "darwin-"+runtime.GOARCH, "Godwit.app", "Contents", "MacOS", "Godwit")
	case "windows":
		return filepath.Join("dist", "windows-"+runtime.GOARCH, "Godwit.exe")
	default:
		return filepath.Join("dist", "linux-"+runtime.GOARCH, "Godwit")
	}
}

func runMain(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("procbench", flag.ContinueOnError)
	binFlag := fs.String("bin", defaultBinPath(), "path to packaged Godwit binary")
	dataDirFlag := fs.String("data-dir", "", "existing --data-dir to measure (default: temporary seeded directory)")
	seedFlag := fs.Bool("seed", false, "seed --data-dir with deterministic local IMAP accounts before measuring")
	sampleOnly := fs.Bool("sample-only", false, "only sample an already-running --data-dir over --window")
	accounts := fs.Int("accounts", 2, "number of IMAP accounts to seed")
	folders := fs.Int("folders", 10, "extra folders per account (in addition to INBOX/Sent/Trash/Archive)")
	inboxMsgs := fs.Int("inbox-messages", 500, "messages in INBOX per account")
	folderMsgs := fs.Int("folder-messages", 50, "messages per extra folder per account")
	settle := fs.Duration("settle", 5*time.Second, "settle wait after hiding window before background sampling")
	window := fs.Duration("window", 30*time.Second, "undisturbed background sampling duration")
	runs := fs.Int("runs", 3, "number of full cold-start / background / reopen runs")
	asJSON := fs.Bool("json", false, "print JSON report")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *sampleOnly {
		if *dataDirFlag == "" {
			return errors.New("--sample-only requires --data-dir")
		}
		dir, err := canonicalDir(*dataDirFlag)
		if err != nil {
			return err
		}
		return runSampleOnly(ctx, dir, *window, *asJSON)
	}

	binPath, err := filepath.Abs(*binFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("binary not found at %s (build first with: go tool mygo build -skip-dmg -skip-notarize)", binPath)
	}

	scale := seedScale{
		Accounts:       *accounts,
		ExtraFolders:   *folders,
		InboxMessages:  *inboxMsgs,
		FolderMessages: *folderMsgs,
	}

	var dataDir string
	tempDir := false
	if *dataDirFlag == "" {
		rawDir, err := os.MkdirTemp("", "gwbench-")
		if err != nil {
			return err
		}
		tempDir = true
		dataDir, err = canonicalDir(rawDir)
		if err != nil {
			os.RemoveAll(rawDir)
			return err
		}
		defer os.RemoveAll(dataDir)
	} else {
		if err := os.MkdirAll(*dataDirFlag, 0o700); err != nil {
			return err
		}
		dataDir, err = canonicalDir(*dataDirFlag)
		if err != nil {
			return err
		}
	}

	var mailSrv *loopbackMail
	if tempDir || *seedFlag {
		mailSrv, err = startLoopbackMail(scale)
		if err != nil {
			return fmt.Errorf("start local mail server: %w", err)
		}
		defer mailSrv.Close()
		if err := seedDataDir(ctx, dataDir, mailSrv, scale); err != nil {
			return fmt.Errorf("seed data dir: %w", err)
		}
		if tempDir {
			defer func() {
				cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = cleanupSeededAccounts(cleanCtx, dataDir)
			}()
		}
	}

	logDir, err := os.MkdirTemp("", "gwbench-logs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(logDir)

	var results []runResult
	for i := 1; i <= *runs; i++ {
		logPath := filepath.Join(logDir, fmt.Sprintf("run-%d.log", i))
		res, err := executeRun(ctx, i, binPath, dataDir, logPath, *settle, *window)
		stopDataDirProcesses(context.Background(), dataDir)
		if err != nil {
			return fmt.Errorf("run %d: %w", i, err)
		}
		results = append(results, res)
	}

	rep := buildReport(scale, *settle, *window, results)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(rep)
	return nil
}

func runSampleOnly(ctx context.Context, dataDir string, window time.Duration, asJSON bool) error {
	startSnap, err := collectSnapshot(ctx, dataDir)
	if err != nil {
		return err
	}
	if startSnap.OwnCount == 0 {
		return fmt.Errorf("no live Godwit process found with --data-dir %s", dataDir)
	}
	t0 := time.Now()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(window):
	}
	endSnap, err := collectSnapshot(ctx, dataDir)
	if err != nil {
		return err
	}
	st := diffSnapshots(startSnap, endSnap, time.Since(t0))
	res := toRunResult(1, 0, 0, st)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Printf("DataDir: %s (%.1fs window)\n", dataDir, res.BgWindowSec)
	fmt.Printf("Processes: %d own + %d helpers\n", res.BgOwnProcs, res.BgHelperProcs)
	fmt.Printf("RSS: %.1f MiB own / %.1f MiB tree (footprint/private: %.1f MiB own / %.1f MiB tree)\n",
		res.BgOwnRSSMiB, res.BgTreeRSSMiB, res.BgOwnPrivateMiB, res.BgTreePrivateMiB)
	fmt.Printf("Wakeups: %d own / %d tree | Context switches: %d own / %d tree | CPU: %.1f ms own / %.1f ms tree\n",
		res.BgOwnWakeups, res.BgTreeWakeups, res.BgOwnContextSwitch, res.BgTreeContextSwitch, res.BgOwnCPUMs, res.BgTreeCPUMs)
	return nil
}

func appBundleFromBin(binPath string) (string, bool) {
	const marker = ".app/Contents/MacOS/"
	idx := strings.Index(filepath.ToSlash(binPath), marker)
	if idx < 0 {
		return "", false
	}
	return binPath[:idx+len(".app")], true
}

func executeRun(ctx context.Context, idx int, binPath, dataDir, logPath string, settle, window time.Duration) (runResult, error) {
	var stderrBuf bytes.Buffer

	spawn := func() error {
		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			if bundle, ok := appBundleFromBin(binPath); ok {
				cmd = exec.CommandContext(ctx, "open", "-g", "-n", "-a", bundle,
					"--env", "GODWIT_BENCH_LOG="+logPath,
					"--args", "--data-dir", dataDir)
			}
		}
		if cmd == nil {
			cmd = exec.CommandContext(ctx, binPath, "--data-dir", dataDir)
			cmd.Env = append(os.Environ(), "GODWIT_BENCH_LOG="+logPath)
		}
		cmd.Stderr = &stderrBuf
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	defer func() {
		if data, err := os.ReadFile(logPath); err == nil {
			st := parseBenchLog(string(data))
			_ = sendBenchCmd(st.LatestCtl, "quit")
		}
	}()

	launchNano := time.Now().UnixNano()
	if err := spawn(); err != nil {
		return runResult{}, err
	}

	st, err := waitForLog(ctx, logPath, 30*time.Second, func(s benchLogState) bool {
		return s.LatestCtl.Addr != "" && len(s.MailReady) >= 1
	})
	if err != nil {
		return runResult{}, fmt.Errorf("waiting for cold-start mail_ready: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}
	coldStart := time.Duration(st.MailReady[0] - launchNano)

	if err := sendBenchCmd(st.LatestCtl, "close"); err != nil {
		return runResult{}, fmt.Errorf("sending close: %w", err)
	}
	st, err = waitForLog(ctx, logPath, 15*time.Second, func(s benchLogState) bool {
		return len(s.WindowHidden) >= 1
	})
	if err != nil {
		return runResult{}, fmt.Errorf("waiting for window_hidden: %w", err)
	}

	select {
	case <-ctx.Done():
		return runResult{}, ctx.Err()
	case <-time.After(settle):
	}

	startSnap, err := collectSnapshot(ctx, dataDir)
	if err != nil {
		return runResult{}, fmt.Errorf("start snapshot: %w", err)
	}
	if startSnap.OwnCount == 0 {
		return runResult{}, errors.New("no background Godwit process remained alive after closing window")
	}

	winStart := time.Now()
	select {
	case <-ctx.Done():
		return runResult{}, ctx.Err()
	case <-time.After(window):
	}
	actualWindow := time.Since(winStart)

	endSnap, err := collectSnapshot(ctx, dataDir)
	if err != nil {
		return runResult{}, fmt.Errorf("end snapshot: %w", err)
	}
	bgStats := diffSnapshots(startSnap, endSnap, actualWindow)
	if len(bgStats.OwnExited) > 0 { // its share of the window would silently drop out of the numbers
		return runResult{}, fmt.Errorf("Godwit processes %v exited during the background window; use a window shorter than the interface's hidden exit", bgStats.OwnExited)
	}

	if data, err := os.ReadFile(logPath); err == nil {
		st = parseBenchLog(string(data))
	}
	if len(st.MailReady) != 1 {
		return runResult{}, fmt.Errorf("unexpected mail_ready during background window (%d events)", len(st.MailReady))
	}
	seenReady := len(st.MailReady)
	reopenNano := time.Now().UnixNano()
	if err := triggerReopen(st.LatestCtl, spawn); err != nil {
		return runResult{}, fmt.Errorf("reopening window: %w", err)
	}
	st, err = waitForLog(ctx, logPath, 30*time.Second, func(s benchLogState) bool {
		return len(s.MailReady) > seenReady && s.MailReady[len(s.MailReady)-1] >= reopenNano
	})
	if err != nil {
		return runResult{}, fmt.Errorf("waiting for reopen mail_ready: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}
	reopen := time.Duration(st.MailReady[len(st.MailReady)-1] - reopenNano)

	_ = sendBenchCmd(st.LatestCtl, "quit")
	return toRunResult(idx, coldStart, reopen, bgStats), nil
}

func toRunResult(idx int, coldStart, reopen time.Duration, st windowStats) runResult {
	const mib = 1024 * 1024
	return runResult{
		Run:                 idx,
		ColdStartMs:         float64(coldStart.Microseconds()) / 1000.0,
		ReopenMs:            float64(reopen.Microseconds()) / 1000.0,
		BgOwnProcs:          st.End.OwnCount,
		BgHelperProcs:       st.End.HelperCount,
		BgOwnRSSMiB:         float64(st.End.OwnRSSBytes) / mib,
		BgTreeRSSMiB:        float64(st.End.TreeRSSBytes) / mib,
		BgOwnPrivateMiB:     float64(st.End.OwnPrivateBytes) / mib,
		BgTreePrivateMiB:    float64(st.End.TreePrivateBytes) / mib,
		BgOwnWakeups:        st.OwnWakeups,
		BgTreeWakeups:       st.TreeWakeups,
		BgOwnContextSwitch:  st.OwnContextSwitches,
		BgTreeContextSwitch: st.TreeContextSwitches,
		BgOwnCPUMs:          st.OwnCPUMs,
		BgTreeCPUMs:         st.TreeCPUMs,
		BgWindowSec:         st.Duration.Seconds(),
	}
}

func waitForLog(ctx context.Context, logPath string, timeout time.Duration, cond func(benchLogState) bool) (benchLogState, error) {
	deadline := time.Now().Add(timeout)
	var last benchLogState
	for {
		if data, err := os.ReadFile(logPath); err == nil {
			last = parseBenchLog(string(data))
			if cond(last) {
				return last, nil
			}
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("timed out after %v (state=%+v)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func stopDataDirProcesses(ctx context.Context, dataDir string) {
	deadline := time.Now().Add(8 * time.Second)
	sentTerm := false
	for {
		pids := listDataDirRootPIDs(ctx, dataDir)
		if len(pids) == 0 {
			return
		}
		if !sentTerm && time.Now().Add(6*time.Second).After(deadline) {
			for _, pid := range pids {
				terminatePID(pid)
			}
			sentTerm = true
		}
		if time.Now().After(deadline) {
			for _, pid := range pids {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
			time.Sleep(100 * time.Millisecond)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func listDataDirRootPIDs(ctx context.Context, dataDir string) []int {
	var procs []procEntry
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,args=").Output()
		if err != nil {
			return nil
		}
		procs, _ = parseDarwinPSAndTop(string(out), "", nil)
	case "linux":
		procs, _ = collectLinuxProc()
	case "windows":
		shell := "pwsh"
		if _, err := exec.LookPath(shell); err != nil {
			shell = "powershell.exe"
		}
		raw, err := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command",
			`@(Get-CimInstance Win32_Process | ForEach-Object { [pscustomobject]@{ PID=[int]$_.ProcessId; PPID=[int]$_.ParentProcessId; CommandLine=[string]$_.CommandLine } }) | ConvertTo-Json -Compress`).Output()
		if err != nil {
			return nil
		}
		procs, _ = parseWindowsJSON(raw)
	}
	snap := matchProcesses(procs, dataDir, os.Getpid())
	var pids []int
	for pid, m := range snap.ByPID {
		if m.Own {
			pids = append(pids, pid)
		}
	}
	return pids
}

func canonicalDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
}

func buildReport(scale seedScale, settle, window time.Duration, runs []runResult) benchReport {
	var colds, reopens, ownRSS, treeRSS, ownPriv, treePriv, ownCPU, treeCPU []float64
	var ownWake, treeWake, ownCSW, treeCSW []uint64
	var ownProcs, helperProcs []uint64
	for _, r := range runs {
		colds = append(colds, r.ColdStartMs)
		reopens = append(reopens, r.ReopenMs)
		ownRSS = append(ownRSS, r.BgOwnRSSMiB)
		treeRSS = append(treeRSS, r.BgTreeRSSMiB)
		ownPriv = append(ownPriv, r.BgOwnPrivateMiB)
		treePriv = append(treePriv, r.BgTreePrivateMiB)
		ownCPU = append(ownCPU, r.BgOwnCPUMs)
		treeCPU = append(treeCPU, r.BgTreeCPUMs)
		ownWake = append(ownWake, r.BgOwnWakeups)
		treeWake = append(treeWake, r.BgTreeWakeups)
		ownCSW = append(ownCSW, r.BgOwnContextSwitch)
		treeCSW = append(treeCSW, r.BgTreeContextSwitch)
		ownProcs = append(ownProcs, uint64(r.BgOwnProcs))
		helperProcs = append(helperProcs, uint64(r.BgHelperProcs))
	}
	return benchReport{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Machine:   collectMachineInfo(),
		Scale:     scale,
		SettleSec: settle.Seconds(),
		WindowSec: window.Seconds(),
		Runs:      runs,
		Median: runResult{
			ColdStartMs:         medianFloat64(colds),
			ReopenMs:            medianFloat64(reopens),
			BgOwnProcs:          int(medianUint64(ownProcs)),
			BgHelperProcs:       int(medianUint64(helperProcs)),
			BgOwnRSSMiB:         medianFloat64(ownRSS),
			BgTreeRSSMiB:        medianFloat64(treeRSS),
			BgOwnPrivateMiB:     medianFloat64(ownPriv),
			BgTreePrivateMiB:    medianFloat64(treePriv),
			BgOwnWakeups:        medianUint64(ownWake),
			BgTreeWakeups:       medianUint64(treeWake),
			BgOwnContextSwitch:  medianUint64(ownCSW),
			BgTreeContextSwitch: medianUint64(treeCSW),
			BgOwnCPUMs:          medianFloat64(ownCPU),
			BgTreeCPUMs:         medianFloat64(treeCPU),
			BgWindowSec:         window.Seconds(),
		},
	}
}

func collectMachineInfo() machineInfo {
	info := machineInfo{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	trimOut := func(name string, args ...string) string {
		b, err := exec.Command(name, args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	switch runtime.GOOS {
	case "darwin":
		ver := trimOut("sw_vers", "-productVersion")
		build := trimOut("sw_vers", "-buildVersion")
		kern := trimOut("uname", "-r")
		info.OSVersion = fmt.Sprintf("macOS %s (%s, Darwin %s)", ver, build, kern)
		info.CPU = trimOut("sysctl", "-n", "machdep.cpu.brand_string")
		if memBytes, err := strconv.ParseUint(trimOut("sysctl", "-n", "hw.memsize"), 10, 64); err == nil {
			info.RAMGiB = fmt.Sprintf("%.0f GiB", float64(memBytes)/(1<<30))
		}
	case "windows":
		shell := "pwsh"
		if _, err := exec.LookPath(shell); err != nil {
			shell = "powershell.exe"
		}
		info.OSVersion = trimOut(shell, "-NoProfile", "-Command", "[System.Environment]::OSVersion.VersionString")
		info.CPU = trimOut(shell, "-NoProfile", "-Command", "(Get-CimInstance Win32_Processor | Select-Object -First 1).Name")
		if memBytes, err := strconv.ParseUint(trimOut(shell, "-NoProfile", "-Command", "(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory"), 10, 64); err == nil {
			info.RAMGiB = fmt.Sprintf("%.0f GiB", float64(memBytes)/(1<<30))
		}
	case "linux":
		info.OSVersion = trimOut("uname", "-sr")
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
					info.CPU = strings.TrimSpace(v)
					break
				}
			}
		}
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "MemTotal:"); ok {
					fields := strings.Fields(v)
					if len(fields) > 0 {
						if kib, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
							info.RAMGiB = fmt.Sprintf("%.0f GiB", float64(kib)/(1<<20))
						}
					}
					break
				}
			}
		}
	}
	return info
}

func printReport(rep benchReport) {
	totalFolders := rep.Scale.ExtraFolders + 4
	perAccMsgs := rep.Scale.InboxMessages + rep.Scale.ExtraFolders*rep.Scale.FolderMessages
	fmt.Printf("Machine: %s | %s | %s (%s/%s)\n", rep.Machine.CPU, rep.Machine.RAMGiB, rep.Machine.OSVersion, rep.Machine.GOOS, rep.Machine.GOARCH)
	fmt.Printf("Scale:   %d accounts, %d folders/account (%d INBOX + %d*%d extra = %d msgs/account, %d total msgs)\n",
		rep.Scale.Accounts, totalFolders, rep.Scale.InboxMessages, rep.Scale.ExtraFolders, rep.Scale.FolderMessages,
		perAccMsgs, rep.Scale.Accounts*perAccMsgs)
	fmt.Printf("Window:  %.0fs settle + %.0fs undisturbed background window (%d runs)\n\n", rep.SettleSec, rep.WindowSec, len(rep.Runs))

	for _, r := range rep.Runs {
		fmt.Printf("  Run %d: cold=%.1fms  reopen=%.1fms  procs=%d+%d  rss=%.1f/%.1f MiB (priv %.1f/%.1f MiB)  wakeups=%d/%d  csw=%d/%d\n",
			r.Run, r.ColdStartMs, r.ReopenMs, r.BgOwnProcs, r.BgHelperProcs,
			r.BgOwnRSSMiB, r.BgTreeRSSMiB, r.BgOwnPrivateMiB, r.BgTreePrivateMiB,
			r.BgOwnWakeups, r.BgTreeWakeups, r.BgOwnContextSwitch, r.BgTreeContextSwitch)
	}
	m := rep.Median
	fmt.Printf("\nMedian:\n")
	fmt.Printf("  Cold start -> mail list ready: %.1f ms\n", m.ColdStartMs)
	fmt.Printf("  Window reopen -> mail list:    %.1f ms\n", m.ReopenMs)
	fmt.Printf("  Background processes:          %d Godwit + %d helpers\n", m.BgOwnProcs, m.BgHelperProcs)
	fmt.Printf("  Background RSS:                %.1f MiB Godwit / %.1f MiB total tree\n", m.BgOwnRSSMiB, m.BgTreeRSSMiB)
	fmt.Printf("  Background footprint/private:  %.1f MiB Godwit / %.1f MiB total tree\n", m.BgOwnPrivateMiB, m.BgTreePrivateMiB)
	fmt.Printf("  Background wakeups (%.0fs):      %d Godwit / %d total tree\n", m.BgWindowSec, m.BgOwnWakeups, m.BgTreeWakeups)
	fmt.Printf("  Background ctxt switches:      %d Godwit / %d total tree\n", m.BgOwnContextSwitch, m.BgTreeContextSwitch)
}

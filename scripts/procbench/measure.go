package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type procEntry struct {
	PID             int
	PPID            int
	ResponsiblePID  int
	Name            string
	Args            string
	RSSBytes        uint64
	PrivateBytes    uint64 // macOS phys_footprint (top MEM), Windows PrivateMemorySize64, Linux RssAnon
	Wakeups         uint64 // macOS pkg_idle_wkups (top IDLEW)
	ContextSwitches uint64 // macOS CSW, Windows thread ContextSwitches, Linux voluntary+nonvoluntary ctxt switches
	CPUMs           float64
	Handles         uint64
}

type matchedProc struct {
	procEntry
	Own bool // true for Godwit processes (--data-dir roots and plugin-host), false for WebView/XPC helpers
}

type processSnapshot struct {
	OwnCount         int
	HelperCount      int
	OwnRSSBytes      uint64
	TreeRSSBytes     uint64
	OwnPrivateBytes  uint64
	TreePrivateBytes uint64
	OwnHandles       uint64
	TreeHandles      uint64
	ByPID            map[int]matchedProc
}

type windowStats struct {
	Duration            time.Duration
	End                 processSnapshot
	PeakOwnRSSBytes     uint64
	PeakTreeRSSBytes    uint64
	OwnWakeups          uint64
	TreeWakeups         uint64
	OwnContextSwitches  uint64
	TreeContextSwitches uint64
	OwnCPUMs            float64
	TreeCPUMs           float64
}

func matchProcesses(procs []procEntry, dataDir string, selfPID int) processSnapshot {
	target := normPath(dataDir)
	byPID := make(map[int]procEntry, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}

	excluded := map[int]bool{}
	for cur := selfPID; cur > 1; {
		if excluded[cur] {
			break
		}
		excluded[cur] = true
		parent, ok := byPID[cur]
		if !ok || parent.PPID <= 1 || parent.PPID == cur {
			break
		}
		cur = parent.PPID
	}

	matched := map[int]matchedProc{}
	roots := map[int]bool{}
	for _, p := range procs {
		if p.PID <= 0 || excluded[p.PID] {
			continue
		}
		if hasDataDirArg(p.Args, target) {
			roots[p.PID] = true
			matched[p.PID] = matchedProc{procEntry: p, Own: true}
		}
	}

	// Transitively attach PPID children and macOS responsible-PID XPC helpers.
	for changed := true; changed; {
		changed = false
		for _, p := range procs {
			if p.PID <= 0 || excluded[p.PID] {
				continue
			}
			if _, ok := matched[p.PID]; ok {
				continue
			}
			_, parentIn := matched[p.PPID]
			respRoot := p.ResponsiblePID > 0 && roots[p.ResponsiblePID]
			if parentIn || respRoot {
				matched[p.PID] = matchedProc{procEntry: p, Own: isOwnGodwitHelper(p.Args)}
				changed = true
			}
		}
	}

	snap := processSnapshot{ByPID: matched}
	for _, m := range matched {
		snap.TreeRSSBytes += m.RSSBytes
		snap.TreePrivateBytes += m.PrivateBytes
		snap.TreeHandles += m.Handles
		if m.Own {
			snap.OwnCount++
			snap.OwnRSSBytes += m.RSSBytes
			snap.OwnPrivateBytes += m.PrivateBytes
			snap.OwnHandles += m.Handles
		} else {
			snap.HelperCount++
		}
	}
	return snap
}

func isOwnGodwitHelper(args string) bool {
	toks := splitArgs(args)
	if len(toks) < 2 {
		return false
	}
	return toks[1] == "plugin-host"
}

func hasDataDirArg(args, normTarget string) bool {
	if normTarget == "" {
		return false
	}
	toks := splitArgs(args)
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if tok == "--data-dir" || tok == "-data-dir" {
			if i+1 < len(toks) && sameNormPath(normPath(toks[i+1]), normTarget) {
				return true
			}
			continue
		}
		for _, prefix := range []string{"--data-dir=", "-data-dir="} {
			if val, ok := strings.CutPrefix(tok, prefix); ok && sameNormPath(normPath(val), normTarget) {
				return true
			}
		}
	}
	return false
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	s := strings.ReplaceAll(p, "\\", "/")
	return path.Clean(s)
}

func sameNormPath(a, b string) bool {
	if (len(a) >= 2 && a[1] == ':') || (len(b) >= 2 && b[1] == ':') || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// splitArgs tokenizes a command line preserving quoted segments without
// treating Windows path backslashes as escape characters (except \").
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inToken := false
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(s) && s[i+1] == '"' {
				cur.WriteByte('"')
				i++
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		switch ch {
		case ' ', '\t', '\r', '\n':
			if inToken {
				out = append(out, cur.String())
				cur.Reset()
				inToken = false
			}
		case '"', '\'':
			quote = ch
			inToken = true
		default:
			cur.WriteByte(ch)
			inToken = true
		}
	}
	if inToken {
		out = append(out, cur.String())
	}
	return out
}

func diffSnapshots(start, end processSnapshot, dur time.Duration) windowStats {
	st := windowStats{
		Duration:         dur,
		End:              end,
		PeakOwnRSSBytes:  max(start.OwnRSSBytes, end.OwnRSSBytes),
		PeakTreeRSSBytes: max(start.TreeRSSBytes, end.TreeRSSBytes),
	}
	for pid, cur := range end.ByPID {
		prev, existed := start.ByPID[pid]
		var dWake, dCSW uint64
		var dCPU float64
		if existed {
			if cur.Wakeups > prev.Wakeups {
				dWake = cur.Wakeups - prev.Wakeups
			}
			if cur.ContextSwitches > prev.ContextSwitches {
				dCSW = cur.ContextSwitches - prev.ContextSwitches
			}
			if cur.CPUMs > prev.CPUMs {
				dCPU = cur.CPUMs - prev.CPUMs
			}
		} else {
			dWake = cur.Wakeups
			dCSW = cur.ContextSwitches
			dCPU = cur.CPUMs
		}
		st.TreeWakeups += dWake
		st.TreeContextSwitches += dCSW
		st.TreeCPUMs += dCPU
		if cur.Own {
			st.OwnWakeups += dWake
			st.OwnContextSwitches += dCSW
			st.OwnCPUMs += dCPU
		}
	}
	return st
}

func parseDarwinPSAndTop(psOut, topOut string, responsible func(int) int) ([]procEntry, error) {
	type topStat struct {
		mem   uint64
		idlew uint64
		csw   uint64
	}
	topByPID := map[int]topStat{}
	inTable := false
	for _, raw := range strings.Split(topOut, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "PID" && fields[1] == "MEM" {
			inTable = true
			continue
		}
		if !inTable || len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		topByPID[pid] = topStat{
			mem:   parseTopSize(fields[1]),
			idlew: parseTopCounter(fields[2]),
			csw:   parseTopCounter(fields[3]),
		}
	}

	var out []procEntry
	for _, raw := range strings.Split(psOut, "\n") {
		line := strings.TrimLeft(raw, " \t")
		if line == "" {
			continue
		}
		pidStr, rest, ok := cutField(line)
		if !ok {
			continue
		}
		ppidStr, rest, ok := cutField(rest)
		if !ok {
			continue
		}
		rssStr, args, ok := cutField(rest)
		if !ok {
			continue
		}
		pid, err1 := strconv.Atoi(pidStr)
		ppid, err2 := strconv.Atoi(ppidStr)
		rssKiB, err3 := strconv.ParseUint(rssStr, 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		resp := pid
		if responsible != nil {
			if r := responsible(pid); r > 0 {
				resp = r
			}
		}
		ts := topByPID[pid]
		out = append(out, procEntry{
			PID:             pid,
			PPID:            ppid,
			ResponsiblePID:  resp,
			Args:            strings.TrimSpace(args),
			RSSBytes:        rssKiB * 1024,
			PrivateBytes:    ts.mem,
			Wakeups:         ts.idlew,
			ContextSwitches: ts.csw,
		})
	}
	return out, nil
}

func cutField(s string) (field, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", false
	}
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, "", true
	}
	return s[:i], strings.TrimLeft(s[i:], " \t"), true
}

func parseTopCounter(s string) uint64 {
	s = strings.TrimRight(s, "+-*")
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func parseTopSize(s string) uint64 {
	s = strings.TrimRight(s, "+-*")
	if s == "" {
		return 0
	}
	mult := uint64(1)
	switch s[len(s)-1] {
	case 'B', 'b':
		s = s[:len(s)-1]
	case 'K', 'k':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'G', 'g':
		mult = 1 << 30
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return uint64(f * float64(mult))
}

type winProcJSON struct {
	PID             int     `json:"PID"`
	PPID            int     `json:"PPID"`
	Name            string  `json:"Name"`
	CommandLine     string  `json:"CommandLine"`
	WorkingSet64    uint64  `json:"WorkingSet64"`
	PrivateBytes    uint64  `json:"PrivateBytes"`
	CPUMs           float64 `json:"CPUMs"`
	Handles         uint64  `json:"Handles"`
	ContextSwitches uint64  `json:"ContextSwitches"`
}

func parseWindowsJSON(raw []byte) ([]procEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var rows []winProcJSON
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, err
		}
	} else {
		var single winProcJSON
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil, err
		}
		rows = []winProcJSON{single}
	}
	out := make([]procEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, procEntry{
			PID:             r.PID,
			PPID:            r.PPID,
			Name:            r.Name,
			Args:            r.CommandLine,
			RSSBytes:        r.WorkingSet64,
			PrivateBytes:    r.PrivateBytes,
			CPUMs:           r.CPUMs,
			Handles:         r.Handles,
			ContextSwitches: r.ContextSwitches,
		})
	}
	return out, nil
}

const windowsCollectScript = `
$ErrorActionPreference = 'SilentlyContinue'
$csw = @{}
Get-CimInstance Win32_PerfRawData_PerfThread_Thread | ForEach-Object {
  $id = [int]$_.IDProcess
  $csw[$id] = [uint64]($csw[$id]) + [uint64]$_.ContextSwitchesPersec
}
$gp = @{}
Get-Process | ForEach-Object { $gp[[int]$_.Id] = $_ }
$out = foreach ($w in Get-CimInstance Win32_Process) {
  $id = [int]$w.ProcessId
  $p = $gp[$id]
  [pscustomobject]@{
    PID = $id
    PPID = [int]$w.ParentProcessId
    Name = [string]$w.Name
    CommandLine = [string]$w.CommandLine
    WorkingSet64 = if ($p) { [uint64]$p.WorkingSet64 } else { [uint64]$w.WorkingSetSize }
    PrivateBytes = if ($p) { [uint64]$p.PrivateMemorySize64 } else { [uint64]$w.PrivatePageCount }
    CPUMs = if ($p -and $p.TotalProcessorTime) { [double]$p.TotalProcessorTime.TotalMilliseconds } else { 0.0 }
    Handles = if ($p) { [uint64]$p.HandleCount } else { [uint64]$w.HandleCount }
    ContextSwitches = [uint64]($csw[$id])
  }
}
@($out) | ConvertTo-Json -Compress
`

func collectSnapshot(ctx context.Context, dataDir string) (processSnapshot, error) {
	var procs []procEntry
	var err error
	switch runtime.GOOS {
	case "darwin":
		psOut, errPS := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,args=").Output()
		if errPS != nil {
			return processSnapshot{}, fmt.Errorf("ps: %w", errPS)
		}
		topOut, errTop := exec.CommandContext(ctx, "top", "-l", "1", "-stats", "pid,mem,idlew,csw").Output()
		if errTop != nil {
			return processSnapshot{}, fmt.Errorf("top: %w", errTop)
		}
		procs, err = parseDarwinPSAndTop(string(psOut), string(topOut), responsiblePID)
	case "windows":
		shell := "pwsh"
		if _, lookErr := exec.LookPath(shell); lookErr != nil {
			shell = "powershell.exe"
		}
		raw, errPS := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", windowsCollectScript).Output()
		if errPS != nil {
			return processSnapshot{}, fmt.Errorf("%s: %w", shell, errPS)
		}
		procs, err = parseWindowsJSON(raw)
	case "linux":
		procs, err = collectLinuxProc()
	default:
		return processSnapshot{}, fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
	if err != nil {
		return processSnapshot{}, err
	}
	return matchProcesses(procs, dataDir, os.Getpid()), nil
}

func collectLinuxProc() ([]procEntry, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []procEntry
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		base := filepath.Join("/proc", e.Name())
		cmdBytes, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil || len(cmdBytes) == 0 {
			continue
		}
		parts := bytes.Split(bytes.TrimRight(cmdBytes, "\x00"), []byte{0})
		quoted := make([]string, len(parts))
		for i, p := range parts {
			s := string(p)
			if strings.ContainsAny(s, " \t\"") {
				s = strconv.Quote(s)
			}
			quoted[i] = s
		}
		statusBytes, err := os.ReadFile(filepath.Join(base, "status"))
		if err != nil {
			continue
		}
		var ppid int
		var rssKiB, anonKiB, volCSW, nonvolCSW uint64
		for _, line := range strings.Split(string(statusBytes), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			fields := strings.Fields(v)
			if len(fields) == 0 {
				continue
			}
			switch k {
			case "PPid":
				ppid, _ = strconv.Atoi(fields[0])
			case "VmRSS":
				rssKiB, _ = strconv.ParseUint(fields[0], 10, 64)
			case "RssAnon":
				anonKiB, _ = strconv.ParseUint(fields[0], 10, 64)
			case "voluntary_ctxt_switches":
				volCSW, _ = strconv.ParseUint(fields[0], 10, 64)
			case "nonvoluntary_ctxt_switches":
				nonvolCSW, _ = strconv.ParseUint(fields[0], 10, 64)
			}
		}
		out = append(out, procEntry{
			PID:             pid,
			PPID:            ppid,
			Args:            strings.Join(quoted, " "),
			RSSBytes:        rssKiB * 1024,
			PrivateBytes:    anonKiB * 1024,
			ContextSwitches: volCSW + nonvolCSW,
		})
	}
	return out, nil
}

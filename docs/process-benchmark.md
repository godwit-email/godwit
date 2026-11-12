# Process-Level Benchmark Protocol & Pre-Split Baseline

Go microbenchmarks (`testing.B`) measure single-function sync and store costs inside one process, but cannot measure what the backend process split ([ADR 0038](adr/0038-backend-process-owns-the-engine.md), #714) changes at the OS boundary:

1. **Background steady-state memory & wakeups** across every live process attached to a `--data-dir` while the main window is closed/hidden in the background.
2. **Cold start latency** from process launch until the first frame with the loaded mail list renders, and **window reopen latency** from reopen request until the mail list frame renders again.

Both the automated runner (`scripts/procbench`) and the manual OS commands below treat Godwit as a black box keyed by `--data-dir`, so the exact same commands run unchanged **before** and **after** the #714 split.

---

## Why the Protocol Is Process-Structure Independent

- **Process discovery by `--data-dir` + helper tree**:
  - Root processes are matched strictly by `--data-dir <canonical-path>` (or `--data-dir=<canonical-path>`) on the command line, excluding `procbench` and its ancestors and never matching a user's running production Godwit instance.
  - Child and helper processes are attached transitively via `PPID` (`godwit plugin-host`, Windows `msedgewebview2.exe` subtrees, Linux `WebKitWebProcess`) and, on macOS, via LaunchServices responsibility (`responsibility_get_pid_responsible_for_pid` in `libSystem.B.dylib`, which attributes `com.apple.WebKit.WebContent`, `Networking`, and `GPU` XPC processes whose `PPID` is `1`).
  - Both **Godwit-own** (`--data-dir` roots + `plugin-host`) and **total process-tree** (including WebView/XPC helpers) totals are reported. Pre-split, hiding the window keeps the UI process and its WebView helpers alive; post-split, closing the window exits the UI process and frees the WebView helpers while leaving the backend process owning `--data-dir`.
- **Lifecycle timestamps (`GODWIT_BENCH_LOG`)**:
  - When `GODWIT_BENCH_LOG=/path/to/bench.log` is set, the UI window logs `mail_ready <unix_nano> pid=<pid>` on the rendered frame where `(*app.App).MailReady()` first holds (after accounts and the initial inbox list have loaded), and `window_hidden <unix_nano> pid=<pid>` when the window hides (`OnHide`) or closes (`OnClosed`).
  - Reopening tries the active UI window's loopback control socket first (pre-split warm `win.Hide()` path) and, if the UI process exited when the window closed (`ECONNREFUSED`), spawns `godwit --data-dir <dir>` against the already-running backend (post-split path).
  - The control listener blocks in `net.Listener.Accept()` with no polling timers, and the background measurement takes only two OS snapshots (`t_start` and `t_end` across an undisturbed sleep), adding **zero** artificial wakeups.

---

## Automated Runner (`scripts/procbench`)

### 1. Build the packaged app

Always benchmark a packaged build so `web/dist` and platform resources are bundled as in production:

```bash
# macOS (builds native bridges + web/dist + dist/darwin-<arch>/Godwit.app)
mise run build

# Windows / Linux
(cd web && bun install --frozen-lockfile)
go tool mygo build -skip-dmg -skip-notarize
```

### 2. Run on an isolated temporary `--data-dir` (recommended)

By default, `procbench` creates a fresh temporary `--data-dir`, starts an in-process loopback IMAP (`IDLE`, `MOVE`, `UIDPLUS`) + SMTP server, seeds deterministic accounts/folders/threads, runs 3 full `cold start -> close window -> settle -> background window -> reopen -> quit` cycles without stealing focus, and removes both the temporary `--data-dir` and its temporary keychain entries on exit:

```bash
# Default scale: 2 accounts, 14 folders/account, 2,000 messages (500 threads), 5s settle + 30s window
CGO_ENABLED=0 go run ./scripts/procbench

# Heavy scale: 5 accounts, 24 folders/account, 15,000 messages (3,750 threads)
CGO_ENABLED=0 go run ./scripts/procbench \
  --accounts 5 --folders 20 --inbox-messages 1000 --folder-messages 100 \
  --runs 3 --settle 5s --window 30s

# Machine-readable JSON output for before/after diffing
CGO_ENABLED=0 go run ./scripts/procbench --json > baseline.json
```

### 3. Sample an already-running `--data-dir`

To measure background steady-state memory and wakeups on an already-running Godwit instance (for example `--data-dir /tmp/my-test-dir` after manually closing its window):

```bash
CGO_ENABLED=0 go run ./scripts/procbench --sample-only --data-dir /tmp/my-test-dir --window 30s
```

---

## Standalone OS Measurement Commands

When inspecting processes manually without `procbench`, always pass an explicit `--data-dir` so production data is untouched.

### macOS (`ps` + `top -stats pid,mem,idlew,csw`)

```bash
DATADIR="/private/tmp/godwit-bench"

# 1. List all Godwit PIDs attached to $DATADIR and sum RSS (KiB):
PIDS=$(ps -axo pid=,args= | awk -v d="$DATADIR" 'index($0, "--data-dir " d) || index($0, "--data-dir=" d) { print $1 }' | paste -sd, -)
ps -o pid=,ppid=,rss=,comm= -p "$PIDS"

# 2. Capture physical footprint (MEM), cumulative package idle wakeups (IDLEW),
#    and context switches (CSW) across an undisturbed 30s background window:
top -l 1 -stats pid,mem,idlew,csw | awk -v pids=",$PIDS," 'NR<=15 && $1=="PID" {p=1; print; next} p && index(pids, "," $1 ",") {print}'
sleep 30
top -l 1 -stats pid,mem,idlew,csw | awk -v pids=",$PIDS," 'NR<=15 && $1=="PID" {p=1; print; next} p && index(pids, "," $1 ",") {print}'
```

*(Note: `scripts/procbench` additionally attaches `com.apple.WebKit.*` XPC services via `responsibility_get_pid_responsible_for_pid` so WebView helper memory and wakeups are included alongside `Godwit`.)*

### Windows (PowerShell: `Win32_Process` + `Get-Process` + `Win32_PerfRawData_PerfThread_Thread`)

```powershell
$DataDir = "C:\Temp\godwit-bench"

function Get-GodwitTreeSnapshot([string]$Dir) {
  $procs = Get-CimInstance Win32_Process
  $matched = @{}
  foreach ($p in $procs) {
    if ($p.CommandLine -match ("--data-dir[= ]`"?" + [regex]::Escape($Dir) + "(`"|\s|$)")) {
      $matched[[int]$p.ProcessId] = $true
    }
  }
  do {
    $added = $false
    foreach ($p in $procs) {
      $id = [int]$p.ProcessId; $ppid = [int]$p.ParentProcessId
      if (-not $matched.ContainsKey($id) -and $matched.ContainsKey($ppid)) {
        $matched[$id] = $true; $added = $true
      }
    }
  } while ($added)

  $ids = [int[]]$matched.Keys
  if (-not $ids) { throw "No Godwit process found for $Dir" }
  $gp = Get-Process -Id $ids -ErrorAction SilentlyContinue
  $csw = (Get-CimInstance Win32_PerfRawData_PerfThread_Thread |
    Where-Object { $ids -contains [int]$_.IDProcess } |
    Measure-Object -Property ContextSwitchesPersec -Sum).Sum

  [pscustomobject]@{
    ProcessCount    = $gp.Count
    WorkingSetMiB   = [math]::Round(($gp | Measure-Object WorkingSet64 -Sum).Sum / 1MB, 1)
    PrivateMiB      = [math]::Round(($gp | Measure-Object PrivateMemorySize64 -Sum).Sum / 1MB, 1)
    CPUMs           = [math]::Round(($gp | ForEach-Object { $_.TotalProcessorTime.TotalMilliseconds } | Measure-Object -Sum).Sum, 1)
    Handles         = ($gp | Measure-Object HandleCount -Sum).Sum
    ContextSwitches = [uint64]$csw
  }
}

$t0 = Get-GodwitTreeSnapshot $DataDir
Start-Sleep -Seconds 30
$t1 = Get-GodwitTreeSnapshot $DataDir

[pscustomobject]@{
  Processes          = $t1.ProcessCount
  WorkingSetMiB      = $t1.WorkingSetMiB
  PrivateMiB         = $t1.PrivateMiB
  DeltaCPUMs_30s     = [math]::Round($t1.CPUMs - $t0.CPUMs, 1)
  DeltaContextSw_30s = $t1.ContextSwitches - $t0.ContextSwitches
  Handles            = $t1.Handles
} | Format-List
```

### Linux (optional: `/proc`)

```bash
DATADIR="/tmp/godwit-bench"
PIDS=$(grep -lZ -- "$DATADIR" /proc/[0-9]*/cmdline 2>/dev/null | tr '\0' '\n' | cut -d/ -f3)
grep -E '^(VmRSS|RssAnon|voluntary_ctxt_switches|nonvoluntary_ctxt_switches):' $(printf '/proc/%s/status ' $PIDS)
```

---

## Pre-Split Baseline (`feat/714-backend-process`, before #714 split)

### Test Environment

| Item | Value |
|---|---|
| Machine | Apple M5 Max, 64 GiB unified memory (`darwin/arm64`) |
| OS | macOS 27.3 (`26C5035r`, Darwin `27.3.0`) |
| Build | Packaged `dist/darwin-arm64/Godwit.app` (`mise run build`, `CGO_ENABLED=0`) |
| Protocol | 3 runs per scale; 5s settle after `window_hidden`, then 30s undisturbed background window; accounts connected via loopback IMAP `IDLE` (`KeepInBackground=true`, `BackgroundSync=inbox`, `AutoLabel=false`) |

### Summary (Medians across 3 runs)

| Scale | Accounts / Folders / Messages | Cold Start -> Mail Ready | Reopen -> Mail Ready | Bg Processes (`own + helpers`) | Bg RSS (`Godwit / Tree`) | Bg `phys_footprint` (`Godwit / Tree`) | Bg `IDLEW` (30s, `Godwit / Tree`) | Bg `CSW` (30s, `Godwit / Tree`) |
|---|---|---|---|---|---|---|---|---|
| **Standard** | 2 accts, 14 folders/acct, 2,000 msgs (500 threads) | **379.8 ms** *(1st uncached: 1025.8 ms)* | **28.4 ms** | `1 + 6` | **190.1 / 439.2 MiB** | **124.0 / 229.2 MiB** | **0 / 0** *(range 0–12 / 0–14)* | **2,694 / 4,216** |
| **Heavy** | 5 accts, 24 folders/acct, 15,000 msgs (3,750 threads) | **701.6 ms** | **30.9 ms** | `1 + 6` | **211.5 / 455.9 MiB** | **139.0 / 242.4 MiB** | **0 / 0** | **3,190 / 4,737** |

### Per-Run Raw Measurements

#### Standard scale (`--accounts 2 --folders 10 --inbox-messages 500 --folder-messages 50`)

| Run | Cold Start (ms) | Reopen (ms) | Bg Procs (`own+helpers`) | Bg RSS MiB (`own / tree`) | Bg Footprint MiB (`own / tree`) | Bg Wakeups / 30s (`own / tree`) | Bg Context Switches / 30s (`own / tree`) |
|---|---|---|---|---|---|---|---|
| 1 *(uncached AMFI)* | 1025.8 | 26.4 | `1 + 6` | 211.9 / 482.0 | 139.0 / 241.2 | 12 / 14 | 12,866 / 17,335 |
| 2 | 376.3 | 29.3 | `1 + 6` | 185.1 / 439.2 | 120.0 / 221.2 | 0 / 0 | 2,694 / 4,216 |
| 3 | 379.8 | 28.4 | `1 + 6` | 190.1 / 428.6 | 124.0 / 229.2 | 0 / 0 | 2,608 / 3,982 |
| **Median** | **379.8** | **28.4** | **`1 + 6`** | **190.1 / 439.2** | **124.0 / 229.2** | **0 / 0** | **2,694 / 4,216** |

#### Heavy scale (`--accounts 5 --folders 20 --inbox-messages 1000 --folder-messages 100`)

| Run | Cold Start (ms) | Reopen (ms) | Bg Procs (`own+helpers`) | Bg RSS MiB (`own / tree`) | Bg Footprint MiB (`own / tree`) | Bg Wakeups / 30s (`own / tree`) | Bg Context Switches / 30s (`own / tree`) |
|---|---|---|---|---|---|---|---|
| 1 | 701.6 | 40.4 | `1 + 6` | 219.1 / 464.8 | 144.0 / 244.3 | 0 / 0 | 101,720 / 103,102 |
| 2 | 903.0 | 26.4 | `1 + 6` | 211.5 / 455.9 | 138.0 / 238.2 | 0 / 0 | 3,190 / 4,737 |
| 3 | 544.6 | 30.9 | `1 + 6` | 206.0 / 449.3 | 139.0 / 242.4 | 0 / 0 | 3,007 / 4,709 |
| **Median** | **701.6** | **30.9** | **`1 + 6`** | **211.5 / 455.9** | **139.0 / 242.4** | **0 / 0** | **3,190 / 4,737** |

### Pre-Split Baseline Takeaways

1. **Background memory is dominated by keeping the UI + WebView process tree alive**: hiding the window leaves `1` monolithic `Godwit` process (`190–212 MiB` RSS, `124–139 MiB` `phys_footprint`) plus `6` macOS WebKit/XPC helper processes (`~245 MiB` additional RSS, `~104 MiB` additional `phys_footprint`), totaling **`439–456 MiB` RSS** (`229–242 MiB` `phys_footprint`).
2. **Reopen vs cold start tradeoff**: pre-split `win.Hide()` reopen takes **`28–31 ms`** because the window, Metal surface, and embedded Wit `WKWebView` remain resident; cached cold start takes **`~380 ms`** at 2,000 messages (`~702 ms` at 15,000 messages), bounding what post-split UI-only cold reopen against an already-warm backend should beat.
3. **Idle wakeups**: once initial sync settles into IMAP `IDLE`, macOS `IDLEW` is **`0` wakeups / 30s** in steady state (`~90–106` context switches/s in `Godwit`, plus `~50/s` in idle WebKit/XPC helpers that disappear post-split).

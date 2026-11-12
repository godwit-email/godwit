# Benchmarks

Benchmarks run entirely in-process against fake IMAP and SMTP servers (`core/internal/testmail`) with a mocked keychain and loopback-only HTTP. They never contact external mail providers or DNS, have no CI pass/fail thresholds, and keep no checked-in baseline files.

## Sync round (`BenchmarkSyncRound`)

`BenchmarkSyncRound` in `core/sync_bench_test.go` measures the cost of one sync round across all accounts in a deterministic fixture built from fixed seed `716` (`testmail.DefaultSeed`).

### Scale tiers

Each tier (`testmail.Scales()`) varies account count, folders per account (`INBOX`, `Sent`, `Trash`, `Archive`, plus `Projects/01`…), and messages per account (plain text, `multipart/alternative` HTML, attachments, and multi-message threads):

| Tier | Accounts | Folders / account | Messages / account | Total messages |
|---|---:|---:|---:|---:|
| `small` | 1 | 5 | 50 | 50 |
| `medium` | 2 | 12 | 200 | 400 |
| `large` | 3 | 24 | 400 | 1,200 |

### Round kinds and metrics

Every tier runs three round kinds:

- `initial` — first sync on an empty local database (discovers folders and imports all seeded messages; account creation traffic is excluded).
- `noop` — full sync across all folders when nothing on the server has changed since the last sync.
- `inbox_only` — narrowed background round checking `INBOX` only (`Service.SetInboxOnly` / worker inbox-only path).

Each sub-benchmark reports:

- `ns/op` — wall time for one round across all accounts in the tier.
- `bytes-down/op` — server-to-client IMAP bytes written during that round alone (per-round delta of `IMAP.BytesSent()`, not cumulative across setup or prior iterations).
- `B/op` and `allocs/op` — heap bytes and allocations per round (`b.ReportAllocs()`).

### Running once

Generate code once if needed, then run `BenchmarkSyncRound`:

```sh
mise run generate
CGO_ENABLED=0 go test -run='^$' -bench='^BenchmarkSyncRound$' ./core
```

To run a single tier or round kind, narrow `-bench`:

```sh
CGO_ENABLED=0 go test -run='^$' -bench='^BenchmarkSyncRound/medium/(noop|inbox_only)$' ./core
```

Example output (macOS, Apple M5 Max, `go1.27.3`, for illustration rather than a pinned baseline):

```text
goos: darwin
goarch: arm64
pkg: github.com/godwit-email/godwit/core
cpu: Apple M5 Max
BenchmarkSyncRound/small/initial-18         	      42	  24775841 ns/op	     64483 bytes-down/op	19169969 B/op	   77288 allocs/op
BenchmarkSyncRound/small/noop-18            	     579	   2083914 ns/op	      3681 bytes-down/op	  355405 B/op	    7185 allocs/op
BenchmarkSyncRound/small/inbox_only-18      	    1076	    932011 ns/op	       974.0 bytes-down/op	  159732 B/op	    3018 allocs/op
BenchmarkSyncRound/medium/initial-18        	       6	 190216056 ns/op	    490687 bytes-down/op	159784960 B/op	  997490 allocs/op
BenchmarkSyncRound/medium/noop-18           	     132	   8086172 ns/op	     23104 bytes-down/op	 1832936 B/op	   38567 allocs/op
BenchmarkSyncRound/medium/inbox_only-18     	     352	   3600321 ns/op	      5744 bytes-down/op	  595415 B/op	   12461 allocs/op
BenchmarkSyncRound/large/initial-18         	       2	 762058167 ns/op	   1469566 bytes-down/op	509137556 B/op	 4399708 allocs/op
BenchmarkSyncRound/large/noop-18            	      38	  26464168 ns/op	     68944 bytes-down/op	 5247064 B/op	  111795 allocs/op
BenchmarkSyncRound/large/inbox_only-18      	     145	   8149430 ns/op	     17449 bytes-down/op	 1557199 B/op	   33997 allocs/op
```

### Before/after comparison table for PRs

Because the mailbox fixture is deterministic, any PR changing sync behavior can attach a reproducible before/after table produced on the same machine with [`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat):

```sh
# 1. On the base commit (e.g. main), collect 6 samples:
git checkout main
mise run generate
CGO_ENABLED=0 go test -run='^$' -bench='^BenchmarkSyncRound$' -count=6 ./core > /tmp/sync-before.txt

# 2. On your branch, collect 6 samples under the same load:
git checkout <your-branch>
mise run generate
CGO_ENABLED=0 go test -run='^$' -bench='^BenchmarkSyncRound$' -count=6 ./core > /tmp/sync-after.txt

# 3. Render the comparison table (sec/op, bytes-down/op, B/op, allocs/op):
go run golang.org/x/perf/cmd/benchstat@latest /tmp/sync-before.txt /tmp/sync-after.txt
```

Paste the `benchstat` output into the PR description, and remove the temporary `/tmp/sync-*.txt` files when done.

## Arrival-to-notification latency (`BenchmarkNotifyLatency`)

`BenchmarkNotifyLatency` in `app/notify_bench_test.go` measures end-to-end latency from a new message landing in `INBOX` on the fake IMAP server (`IMAP.User.Append`) until `app.Platform.Notify` receives the formatted notification. Asserting at `Platform.Notify` keeps the benchmark independent of whether notification filtering and batching run in the UI process or in a separate backend process.

Each sub-benchmark pre-populates one account with the same deterministic mailbox builder as `BenchmarkSyncRound` (`coretest.Seeded(coretest.ScaleSmall, coretest.DefaultSeed)` — 5 folders, 50 messages, seed `716`) and completes the initial sync and `App` load before starting the timer, so seeded messages never trigger notifications.

### Push (`idle`) vs polling (`poll`) and what `ns/op` means

- **Fixed `1s` batching wait (`notifyWait`) is deducted.** In production (`app/notify.go`), `mailArrived` arms a fixed `notifyWait = 1s` timer before querying inbox arrivals, filtering blocked senders and sign-in codes, and calling `Platform.Notify`. `BenchmarkNotifyLatency` drives `App` with `fakeClock` and advances `1s` immediately as soon as `mailArrived` arms that timer. Reported `ns/op` is therefore pure pipeline latency without the constant `1s` sleep; user-visible wall delay for a lone arrival is `ns/op + 1s`.
- **`idle` (IMAP `IDLE` push):** `imapbackend.PollInterval` stays at its `5m` production default so wakes come strictly from IMAP `IDLE` (`core.SetChatterWithinForTest(b, 0)` disables `#453`'s multi-second chatter backoff across back-to-back benchmark iterations). Note that `core/internal/testmail`'s in-memory `IDLE` polls `Session.Poll` on a `10ms` ticker, contributing ~5 ms on average before unilateral `* EXISTS` reaches the client.
- **`poll` (`WithoutIdle()` fallback):** `coretest.WithoutIdle()` removes `IDLE` from `CAPABILITY`, so `Receiver.Watch` returns `backend.ErrUnsupported` and the worker sleeps for `imapbackend.PollInterval` between rounds. To measure protocol, SQLite, and notification overhead without sleeping 5 minutes per iteration, the benchmark sets `imapbackend.PollInterval` to `50ms` by default (override with `GODWIT_BENCH_POLL`, e.g. `GODWIT_BENCH_POLL=1ms` to minimize timer sleep). Reported `ns/op` includes one configured poll wait plus the narrowed `INBOX` sync and notification delivery; in production (`5m` poll interval), polling adds `0–5m` (`2.5m` on average) plus `1s` (`notifyWait`).
- **`bytes-down/op`, `B/op`, `allocs/op`:** Per-arrival server-to-client IMAP bytes and Go heap allocations (`idle` opens both the `Watch` session and the `syncNow` session, whereas `poll` opens only `syncNow`).

### Running once

```sh
mise run generate
CGO_ENABLED=0 go test -run='^$' -bench='^BenchmarkNotifyLatency$' ./app
```

To inspect polling pipeline cost with a custom poll interval:

```sh
GODWIT_BENCH_POLL=1ms CGO_ENABLED=0 go test -run='^$' -bench='^BenchmarkNotifyLatency/poll$' ./app
```

Example output (macOS, Apple M5 Max, `go1.27.3`, default `GODWIT_BENCH_POLL=50ms`, for illustration rather than a pinned baseline):

```text
goos: darwin
goarch: arm64
pkg: github.com/godwit-email/godwit/app
cpu: Apple M5 Max
BenchmarkNotifyLatency/idle-18         	     100	  17937388 ns/op	      4099 bytes-down/op	 3794424 B/op	   77448 allocs/op
BenchmarkNotifyLatency/poll-18         	      21	  56152907 ns/op	      2576 bytes-down/op	 2257566 B/op	   40025 allocs/op
```

For PRs touching background sync, `IDLE`, event delivery, or notifications, compare 6 samples before and after with `benchstat` the same way as `BenchmarkSyncRound`, substituting `./app` and `-bench='^BenchmarkNotifyLatency$'`.

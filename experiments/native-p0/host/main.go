// P0 experiment for docs/superpowers/specs/2026-10-14-native-plugins-design.md.
// Built with CGO_ENABLED=0. Throwaway.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "plugin-host" {
		os.Exit(pluginHost(os.Args[2:]))
	}
	if err := driver(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func emit(kv ...any) {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	fmt.Println(string(b))
}

func rssMiB(pid int) float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return 0
	}
	var kb float64
	fmt.Sscan(strings.TrimSpace(string(out)), &kb)
	return kb / 1024
}

func driver(args []string) error {
	fs := flag.NewFlagSet("driver", flag.ContinueOnError)
	lib := fs.String("library", "", "")
	live := fs.Int("live", 0, "real translations")
	only := fs.String("only", "", "describe: load, describe, exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sha, err := fileSHA(*lib)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c := newClient(*lib, sha, 16)
	defer c.Close()

	start := time.Now()
	e, err := c.Call(ctx, "describe", struct{}{}, 30*time.Second)
	if err != nil || !e.OK {
		return fmt.Errorf("describe: %v %+v", err, e.Error)
	}
	emit("step", "describe", "ms", time.Since(start).Milliseconds(), "result", e.Result, "rss_mib", rssMiB(c.pid()))
	if *only == "describe" {
		return nil
	}

	for _, bad := range []struct {
		method string
		params any
	}{{"translate", map[string]any{"other": 1}}, {"nope", struct{}{}}} {
		e, err := c.Call(ctx, bad.method, bad.params, 5*time.Second)
		if err != nil || e.OK || e.Error == nil || e.Error.Code != "invalid_request" {
			return fmt.Errorf("%s accepted or misreported: %v %+v", bad.method, err, e.Error)
		}
	}
	emit("step", "invalid_requests", "status", "rejected with invalid_request")

	// 1024 local calls (blank texts never leave the process), 16 in flight.
	start = time.Now()
	var wg sync.WaitGroup
	var fails atomic.Int64
	for range 1024 {
		wg.Go(func() {
			e, err := c.Call(ctx, "translate", map[string]any{"texts": []string{" "}, "source": "en", "target": "zh-CN"}, 30*time.Second)
			var r struct{ Texts []string }
			if err != nil || !e.OK || json.Unmarshal(e.Result, &r) != nil || len(r.Texts) != 1 || r.Texts[0] != " " {
				fails.Add(1)
			}
		})
	}
	wg.Wait()
	if fails.Load() > 0 {
		return fmt.Errorf("%d of 1024 concurrent calls failed", fails.Load())
	}
	emit("step", "concurrent", "calls", 1024, "in_flight", 16, "ms", time.Since(start).Milliseconds(), "rss_mib", rssMiB(c.pid()))

	// A call past its deadline kills the process; the next call restarts it.
	pid := c.pid()
	start = time.Now()
	_, err = c.Call(ctx, "probe.sleep", map[string]any{"ms": 10000}, time.Second)
	if !errors.Is(err, errTimeout) {
		return fmt.Errorf("sleep: want timeout, got %v", err)
	}
	killed := time.Since(start).Milliseconds()
	if e, err := c.Call(ctx, "describe", struct{}{}, 30*time.Second); err != nil || !e.OK || c.pid() == pid {
		return fmt.Errorf("no restart after timeout: %v", err)
	}
	emit("step", "timeout_kill", "returned_ms", killed, "restarted", true)

	// A crash in the plugin ends only the subprocess.
	pid = c.pid()
	_, err = c.Call(ctx, "probe.crash", struct{}{}, 10*time.Second)
	if !errors.Is(err, errExited) {
		return fmt.Errorf("crash: want exited, got %v", err)
	}
	stderrHasPanic := strings.Contains(err.Error(), "probe.crash")
	if e, err := c.Call(ctx, "describe", struct{}{}, 30*time.Second); err != nil || !e.OK || c.pid() == pid {
		return fmt.Errorf("no restart after crash: %v", err)
	}
	emit("step", "crash_isolated", "host_alive", true, "stderr_tail_has_panic", stderrHasPanic, "restarted", true, "process_starts", c.starts)

	for i := range *live {
		start = time.Now()
		e, err := c.Call(ctx, "translate", map[string]any{"texts": []string{"Hello, world!", "See you tomorrow."}, "source": "", "target": "zh-CN"}, 30*time.Second)
		if err != nil || !e.OK {
			return fmt.Errorf("live %d: %v %+v", i, err, e.Error)
		}
		emit("step", "live", "n", i+1, "ms", time.Since(start).Milliseconds(), "result", e.Result, "rss_mib", rssMiB(c.pid()))
	}

	// A loaded library's file: removable while loaded (unix) or not (Windows);
	// always removable once the subprocess is gone.
	cp, err := os.CreateTemp("", "p0-lock-*"+filepath.Ext(*lib))
	if err != nil {
		return err
	}
	src, err := os.ReadFile(*lib)
	if err != nil {
		return err
	}
	cp.Write(src)
	cp.Close()
	lc := newClient(cp.Name(), sha, 1)
	if e, err := lc.Call(ctx, "describe", struct{}{}, 30*time.Second); err != nil || !e.OK {
		return fmt.Errorf("lock copy describe: %v", err)
	}
	whileLoaded := os.Remove(cp.Name())
	lc.Close()
	after := os.Remove(cp.Name())
	if whileLoaded == nil {
		after = nil // already gone
	}
	if after != nil {
		return fmt.Errorf("library file not removable after the subprocess exited: %v", after)
	}
	emit("step", "file_lock", "remove_while_loaded", fmt.Sprint(whileLoaded), "remove_after_exit", "ok")

	// Close: stdin closes, the subprocess exits on its own.
	p := c.p
	start = time.Now()
	c.Close()
	emit("step", "close", "exited_ms", time.Since(start).Milliseconds(), "exit_code", p.cmd.ProcessState.ExitCode())
	emit("status", "PASS")
	return nil
}

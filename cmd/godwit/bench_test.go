package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBenchProbeDisabledIsNoop(t *testing.T) {
	var b *benchProbe
	if got := newBenchProbe(""); got != nil {
		t.Fatalf("newBenchProbe(\"\") = %v, want nil", got)
	}
	b.onShow()
	b.onFrame(true)
	b.onHide()
	b.close()
}

func TestBenchProbeLifecycleAndControl(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "bench.log")
	b := newBenchProbe(logPath)
	if b == nil {
		t.Fatal("expected non-nil benchProbe")
	}
	defer b.close()

	cmds := make(chan string, 4)
	if err := b.listen(func(cmd string) { cmds <- cmd }); err != nil {
		t.Fatal(err)
	}

	readLines := func() []string {
		t.Helper()
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}

	lines := readLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "bench_ctl ") {
		t.Fatalf("initial log lines = %q", lines)
	}
	var addr, token string
	for _, field := range strings.Fields(lines[0])[2:] {
		if v, ok := strings.CutPrefix(field, "addr="); ok {
			addr = v
		}
		if v, ok := strings.CutPrefix(field, "token="); ok {
			token = v
		}
	}
	if addr == "" || token == "" {
		t.Fatalf("missing addr or token in %q", lines[0])
	}

	// Frame before shown or before ready must not log mail_ready.
	b.onFrame(true)
	b.onShow()
	b.onFrame(false)
	if got := len(readLines()); got != 1 {
		t.Fatalf("unexpected early event in %q", readLines())
	}

	// First ready frame logs mail_ready once.
	b.onFrame(true)
	b.onFrame(true)
	lines = readLines()
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "mail_ready ") {
		t.Fatalf("after first ready frame: %q", lines)
	}

	send := func(line string) {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := fmt.Fprintln(c, line); err != nil {
			t.Fatal(err)
		}
	}

	// Wrong token must be ignored.
	send("wrong-token close")
	send(token + " close")
	select {
	case got := <-cmds:
		if got != "close" {
			t.Fatalf("got cmd %q, want close", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close command")
	}

	b.onHide()
	b.onHide()
	lines = readLines()
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "window_hidden ") {
		t.Fatalf("after hide: %q", lines)
	}

	// Reopen cycle logs a fresh mail_ready.
	send(token + " open")
	select {
	case got := <-cmds:
		if got != "open" {
			t.Fatalf("got cmd %q, want open", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for open command")
	}
	b.onShow()
	b.onFrame(true)
	lines = readLines()
	if len(lines) != 4 || !strings.HasPrefix(lines[3], "mail_ready ") {
		t.Fatalf("after reopen ready frame: %q", lines)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	if os.Getenv("GODWIT_TEST_MAIN") == "1" { // TestSigintShutdown's child: the real main
		main()
		return
	}
	os.Exit(m.Run())
}

// syncBuf is a bytes.Buffer safe to write from run and read from the test.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// shortDir is a fresh directory short enough for a Unix socket path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// dialWait dials dir's server as full, retrying until it answers.
func dialWait(t *testing.T, dir string) *rpc.Client {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := rpc.Dial(dir, rpc.RoleFull, "cmd-test", rpcbind.NewCodec())
		if err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startHeadless runs `godwit --headless --data-dir dir` until the test ends
// (or stop is called) and waits for it to serve. stop returns run's code.
func startHeadless(t *testing.T, dir string) (c *rpc.Client, stderr *syncBuf, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stderr = &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--headless", "--data-dir", dir}, nil, &syncBuf{}, stderr) }()
	var once sync.Once
	code := -1
	stop = func() int {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(30 * time.Second):
				t.Error("headless did not stop")
			}
		})
		return code
	}
	t.Cleanup(func() { stop() })
	return dialWait(t, dir), stderr, stop
}

func TestHeadlessServes(t *testing.T) {
	dir := shortDir(t)
	c, stderr, stop := startHeadless(t, dir)
	var accs []core.Account
	if err := c.Call(context.Background(), "ListAccounts", &accs); err != nil || len(accs) != 0 {
		t.Fatalf("ListAccounts = %v, %v", accs, err)
	}
	token, err := os.ReadFile(filepath.Join(dir, rpc.TokenName))
	if err != nil {
		t.Fatal(err)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(stderr.String(), "serving the Local API") || strings.Contains(stderr.String(), string(token)) {
		t.Errorf("stderr log %q", stderr.String())
	}
	for _, f := range []string{rpc.SocketName, rpc.TokenName} {
		if _, err := os.Stat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", f, err)
		}
	}
	release, err := ownerlock.Acquire(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	release()
}

func TestHeadlessSecondInstance(t *testing.T) {
	dir := shortDir(t)
	startHeadless(t, dir)
	db := filepath.Join(dir, "godwit.db")
	before, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--headless", "--data-dir", dir}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("second instance: code %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Godwit 已在运行（GUI 或 headless）") {
		t.Fatalf("stderr %q", stderr.String())
	}
	after, err := os.Stat(db)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("the second instance touched the database: %v -> %v, %v", before.ModTime(), after.ModTime(), err)
	}
}

func TestGUIAndHeadlessExclusive(t *testing.T) {
	dir := shortDir(t)
	release, err := ownerlock.Acquire(dir) // the GUI holds it
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--headless", "--data-dir", dir}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "godwit.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("headless opened the database while the GUI owns it: %v", err)
	}
}

func TestCallPrintsJSON(t *testing.T) {
	dir := shortDir(t)
	startHeadless(t, dir)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"call", "--data-dir", dir, "GetSettings"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	var st core.Settings
	if err := json.Unmarshal(stdout.Bytes(), &st); err != nil {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"call", "--data-dir", dir, "ListAccounts", "[]"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "[]" && got != "null" {
		t.Fatalf("ListAccounts printed %q", got)
	}
}

func TestCallStream(t *testing.T) {
	dir := shortDir(t)
	c, _, _ := startHeadless(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout, stderr := &syncBuf{}, &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"call", "--data-dir", dir, "Subscribe"}, nil, stdout, stderr) }()
	var st core.Settings
	if err := c.Call(context.Background(), "GetSettings", &st); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stdout.String(), "\n") { // poke the bus until the subscription sees it
		if time.Now().After(deadline) {
			t.Fatalf("no event; stderr %q", stderr.String())
		}
		if err := c.Call(context.Background(), "SetSettings", nil, st); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		var ev struct{ Type string }
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type == "" {
			t.Fatalf("event line %q: %v", line, err)
		}
	}
}

func TestCallError(t *testing.T) {
	dir := shortDir(t)
	startHeadless(t, dir)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"GrantMCPServer", "[999]"}, "mcp_not_found: MCP server not found\n"},
		{[]string{"NoSuchMethod"}, "-32601: "},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"call", "--data-dir", dir}, tc.args...), nil, &stdout, &stderr)
		if code != 1 || !strings.HasPrefix(stderr.String(), tc.want) || stdout.Len() != 0 {
			t.Errorf("%v: code %d, stdout %q, stderr %q", tc.args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer // nobody serves this directory
	if code := run(context.Background(), []string{"call", "--data-dir", shortDir(t), "ListAccounts"}, nil, &stdout, &stderr); code != 1 || stderr.Len() == 0 {
		t.Errorf("no server: code %d, stderr %q", code, stderr.String())
	}
}

func TestUsageAndVersion(t *testing.T) {
	for _, args := range [][]string{{"call"}, {"call", "M", "not json"}, {"--nope"}, {"frobnicate"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, nil, &stdout, &stderr); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, nil, &stdout, &stderr); code != 0 || !strings.HasPrefix(stdout.String(), "godwit ") {
		t.Fatalf("--version: %d %q", code, stdout.String())
	}
}

// defaultDataDir needs no running App and no display, and creates no window.
func TestDefaultDataDirHeadless(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	dir, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(cfg, "Godwit") {
		t.Fatalf("data dir %q, want under %q", dir, cfg)
	}
	if len(mygo.Windows()) != 0 || mygo.App.IsReady() {
		t.Fatal("asking for the data directory started the App")
	}
}

// A real SIGINT to a real process shuts headless down cleanly.
func TestSigintShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGINT to another process on Windows")
	}
	dir := shortDir(t)
	cmd := exec.Command(os.Args[0], "--headless", "--data-dir", dir)
	cmd.Env = append(os.Environ(), "GODWIT_TEST_MAIN=1")
	var stderr syncBuf
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	c := dialWait(t, dir)
	if err := c.Call(context.Background(), "ListAccounts", nil); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit: %v; stderr %q", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, rpc.SocketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind: %v", err)
	}
}

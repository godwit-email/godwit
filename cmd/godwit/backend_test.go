package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/godwit-email/godwit/uiclient"
)

// started are the backends a test's interface started (backendCmd), as
// GODWIT_TEST_MAIN children of this test binary, without a desktop loop.
type started struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
}

func (s *started) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.cmds) }
func (s *started) last() *exec.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmds[len(s.cmds)-1]
}

// startsBackends makes backendCmd's backends (this test binary, as
// os.Executable) run main.
func startsBackends(t *testing.T) *started {
	t.Helper()
	s := &started{}
	prev := backendCmd
	backendCmd = func(dataDir string) (*exec.Cmd, error) {
		cmd, err := prev(dataDir)
		if err != nil {
			return nil, err
		}
		cmd.Env = append(os.Environ(), "GODWIT_TEST_MAIN=1", "GODWIT_HEADLESS_DESKTOP=0")
		s.mu.Lock()
		s.cmds = append(s.cmds, cmd)
		s.mu.Unlock()
		return cmd, nil
	}
	t.Cleanup(func() {
		backendCmd = prev
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, c := range s.cmds {
			if c.Process != nil {
				c.Process.Kill()
			}
		}
	})
	return s
}

// connect is connectBackend on dir, failing t on an error.
func connect(t *testing.T, dir string) *uiclient.Client {
	t.Helper()
	c, err := connectBackend(dir, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// exited waits for dir's backend to end: the last thing it lets go is the
// owner lock, after its socket.
func exited(t *testing.T, dir string) {
	t.Helper()
	waitCond(t, "the backend to exit", func() bool {
		release, err := ownerlock.Acquire(dir)
		if err == nil {
			release()
		}
		return err == nil
	})
	if _, err := os.Stat(filepath.Join(dir, rpc.SocketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind: %v", err)
	}
}

// The interface starts the backend on its (custom) data directory when none
// runs, waits for it to answer, and its quit stops it.
func TestUIStartsBackend(t *testing.T) {
	s := startsBackends(t)
	dir := shortDir(t)
	c := connect(t, dir)
	if s.count() != 1 {
		t.Fatalf("%d backends started", s.count())
	}
	if args := s.last().Args; !slices.Contains(args, "--data-dir") || !slices.Contains(args, dir) || !slices.Contains(args, "--"+uiStartedFlag) {
		t.Fatalf("backend args %q", args)
	}
	if _, err := c.ListAccounts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerlock.Acquire(dir); !errors.Is(err, ownerlock.ErrAnotherOwner) {
		t.Fatalf("the backend does not own the data directory: %v", err)
	}
	stopBackend(c)
	exited(t, dir)
}

// A backend the user started with --headless is connected to as it is, and
// the interface's quit leaves it running: the two are no longer exclusive.
func TestUIConnectsToUserHeadless(t *testing.T) {
	s := startsBackends(t)
	dir := shortDir(t)
	full, _, stop := startHeadless(t, dir)
	c := connect(t, dir)
	if s.count() != 0 {
		t.Fatalf("%d backends started beside the user's", s.count())
	}
	if h := c.RPC().Hello(); h.Server != "godwit/"+app.Version || h.UIStarted {
		t.Fatalf("hello %+v", h)
	}
	if err := c.RPC().Call(t.Context(), "StopUIBackend", nil); err == nil {
		t.Fatal("the user's backend accepted the interface's stop")
	}
	stopBackend(c)
	if err := full.Call(context.Background(), "ListAccounts", nil); err != nil {
		t.Fatalf("the user's backend stopped with the interface: %v", err)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// oldBackend runs a backend of another version on dir: an earlier interface's
// when uiStarted, else the user's --headless. Its result is its exit.
func oldBackend(t *testing.T, dir string, uiStarted bool) <-chan error {
	t.Helper()
	args := []string{"--headless", "--data-dir", dir}
	if uiStarted {
		args = append(args, "--"+uiStartedFlag)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "GODWIT_TEST_MAIN=1", "GODWIT_HEADLESS_DESKTOP=0", "GODWIT_TEST_VERSION=old")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }() // closed: the cleanup's receive never blocks
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	waitCond(t, "the old backend to answer", func() bool {
		c, err := uiclient.Dial(dir, "")
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	return done
}

// An interface meeting the backend an earlier one started, of another
// version, stops it through its close path and starts its own; the outbox
// goes through the restart as it was.
func TestUIReplacesOutdatedBackend(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	s := startsBackends(t)
	dir := shortDir(t)
	old := oldBackend(t, dir, true)
	oc, err := uiclient.Dial(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer oc.Close()
	if h := oc.RPC().Hello(); h.Server != "godwit/old" || !h.UIStarted {
		t.Fatalf("hello %+v", h)
	}
	acc, _ := addTestAccount(t, env, oc.RPC())
	var d core.Draft
	if err := oc.RPC().Call(t.Context(), "NewDraft", &d, acc.ID, "new", 0); err != nil {
		t.Fatal(err)
	}
	d.To, d.Subject = []string{"bob@example.com"}, "queued"
	if err := oc.RPC().Call(t.Context(), "SaveDraft", &d, d); err != nil {
		t.Fatal(err)
	}
	if err := oc.RPC().Call(t.Context(), "Send", nil, d.ID); err != nil { // queued for its undo window
		t.Fatal(err)
	}

	c := connect(t, dir)
	select {
	case err := <-old:
		if err != nil {
			t.Fatalf("the old backend exited with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the old backend still runs")
	}
	if s.count() != 1 {
		t.Fatalf("%d backends started", s.count())
	}
	if h := c.RPC().Hello(); h.Server != "godwit/"+app.Version || !h.UIStarted {
		t.Fatalf("hello %+v", h)
	}
	out, err := c.ListOutbox(t.Context(), acc.ID)
	if err != nil || len(out) != 1 || out[0].Subject != "queued" || out[0].State != "queued" {
		t.Fatalf("outbox %+v, %v", out, err)
	}
	stopBackend(c)
	exited(t, dir)
}

// A backend of another version the user started is left running, and the
// interface tells the user rather than connect.
func TestUIRefusesOutdatedUserBackend(t *testing.T) {
	s := startsBackends(t)
	dir := shortDir(t)
	old := oldBackend(t, dir, false)
	_, err := connectBackend(dir, 5*time.Second)
	if msg, code := startupFailure(err); code != 1 || msg != i18n.Tf("app.backend_version", "old", app.Version) {
		t.Fatalf("%v: %q %d", err, msg, code)
	}
	if s.count() != 0 {
		t.Fatalf("%d backends started", s.count())
	}
	select {
	case err := <-old:
		t.Fatalf("the user's backend exited: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

// A backend that went away under the interface is noticed, and the next
// interface starts one again.
func TestUIRelaunchesCrashedBackend(t *testing.T) {
	s := startsBackends(t)
	dir := shortDir(t)
	c := connect(t, dir)
	lost := make(chan struct{})
	var quitting atomic.Bool
	watchBackend(c, &quitting, func() { close(lost) })
	if err := s.last().Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(10 * time.Second):
		t.Fatal("the interface did not notice the backend go")
	}
	c = connect(t, dir)
	if s.count() != 2 {
		t.Fatalf("%d backends started, want a second", s.count())
	}
	if _, err := c.ListAccounts(t.Context()); err != nil {
		t.Fatal(err)
	}
	quitting.Store(true) // the interface's own quit is no crash
	watchBackend(c, &quitting, func() { t.Error("a quit taken for a crash") })
	stopBackend(c)
	exited(t, dir)
}

// A backend that cannot start is told to the user as the GUI told it when it
// opened the data directory itself: never the error's text.
func TestUIStartupFailures(t *testing.T) {
	startsBackends(t)
	t.Run("another owner", func(t *testing.T) {
		dir := shortDir(t)
		release, err := ownerlock.Acquire(dir) // an owner that never answers
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		_, err = connectBackend(dir, 2*time.Second)
		if msg, code := startupFailure(err); code != 2 || msg != i18n.T("app.another_owner") {
			t.Fatalf("%v: %q %d", err, msg, code)
		}
	})
	t.Run("database", func(t *testing.T) {
		dir := shortDir(t)
		if err := os.Mkdir(filepath.Join(dir, "godwit.db"), 0o700); err != nil { // not a database
			t.Fatal(err)
		}
		_, err := connectBackend(dir, 30*time.Second)
		if msg, code := startupFailure(err); code != 1 || msg != i18n.Tf("app.open_failed", i18n.T("app.open_failed.other")) {
			t.Fatalf("%v: %q %d", err, msg, code)
		}
	})
	for code, want := range map[startupError]string{
		exitLockFailed: i18n.T("app.lock_failed"),
		exitOpenDenied: i18n.Tf("app.open_failed", i18n.T("app.open_failed.permission")),
	} {
		if msg, c := startupFailure(code); c != 1 || msg != want {
			t.Errorf("exit %d: %q %d", code, msg, c)
		}
	}
}

// The backend the interface started serves external clients only while 接入
// AI 应用 is on, following the setting as it changes; the interface stays on.
func TestUIStartedBackendFollowsLocalAPI(t *testing.T) {
	startsBackends(t)
	dir := shortDir(t)
	c := connect(t, dir)
	full := func() error {
		f, err := rpc.Dial(dir, rpc.RoleFull, "full-test", rpcbind.NewCodec())
		if err == nil {
			err = f.Call(context.Background(), "ListAccounts", nil)
			f.Close()
		}
		return err
	}
	if full() == nil {
		t.Fatal("external access with 接入 AI 应用 off")
	}
	set := func(on bool) {
		st, err := c.GetSettings(t.Context())
		if err == nil {
			st.LocalAPI = on
			_, err = c.SetSettings(t.Context(), st)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	set(true)
	waitCond(t, "external access on", func() bool { return full() == nil })
	if path, err := (&platform{dataDir: dir}).SetLocalAPI(1, true); err != nil || path != filepath.Join(dir, rpc.SocketName) {
		t.Fatalf("SetLocalAPI = %q, %v", path, err)
	}
	set(false)
	waitCond(t, "external access off", func() bool { return full() != nil })
	if _, err := c.ListAccounts(t.Context()); err != nil {
		t.Fatalf("the interface lost the backend: %v", err)
	}
	stopBackend(c)
	exited(t, dir)
}

// A click the backend got goes to a connected interface on its events, and
// nowhere without one (the interface starting for it is #733's).
func TestBackendForwardsNotificationClick(t *testing.T) {
	dir := shortDir(t)
	svc, err := core.New(context.Background(), core.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	ch, unsub := svc.Subscribe()
	defer unsub()
	notificationClicked(svc, "message:1")
	select {
	case ev := <-ch:
		t.Fatalf("%+v with no interface connected", ev)
	case <-time.After(50 * time.Millisecond):
	}
	srv, err := rpcbind.ListenWith(dir, svc, rpcbind.ListenOptions{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(context.Background())
	t.Cleanup(func() { srv.Close() })
	ui, err := uiclient.Dial(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Close()
	evs, stop := ui.Subscribe()
	defer stop()
	waitCond(t, "the interface connected", svc.UIConnected)
	notificationClicked(svc, "message:2")
	for {
		select {
		case ev := <-evs:
			if ev.Type != events.NotificationClicked {
				continue
			}
			if ev.Notification != "message:2" {
				t.Fatalf("%+v", ev)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("the click did not reach the interface")
		}
	}
}

// The tray's Quit asks a connected interface to quit first, and stops the
// backend once it has gone.
func TestTrayQuitAsksInterfaceFirst(t *testing.T) {
	dir := shortDir(t)
	svc, err := core.New(context.Background(), core.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	srv, err := rpcbind.ListenWith(dir, svc, rpcbind.ListenOptions{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(context.Background())
	t.Cleanup(func() { srv.Close() })
	ui, err := uiclient.Dial(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	evs, unsub := ui.Subscribe()
	defer unsub()
	waitCond(t, "the interface connected", svc.UIConnected)
	var stopped atomic.Bool
	quitFromTray(svc, func() { stopped.Store(true) })
	for ev := range evs {
		if ev.Type == events.QuitRequested {
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("stopped before the interface quit")
	}
	ui.Close()
	waitCond(t, "the backend to stop", stopped.Load)
}

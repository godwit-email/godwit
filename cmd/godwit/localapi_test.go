package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpc"
)

// The GUI's switch serves the Local API in-process: on, a client can dial
// it; off, the socket and token are gone.
func TestLocalAPIStartsServer(t *testing.T) {
	dir := shortDir(t)
	svc, err := core.New(context.Background(), core.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	p := &platform{dataDir: dir, svc: svc}
	path, err := p.SetLocalAPI(1, true)
	if err != nil || path != filepath.Join(dir, rpc.SocketName) {
		t.Fatalf("SetLocalAPI(true) = %q, %v", path, err)
	}
	if again, err := p.SetLocalAPI(1, true); err != nil || again != path { // already on: the same server
		t.Fatalf("SetLocalAPI(true) again = %q, %v", again, err)
	}
	var accs []core.Account
	if err := dialWait(t, dir).Call(context.Background(), "ListAccounts", &accs); err != nil {
		t.Fatal(err)
	}
	if path, err := p.SetLocalAPI(2, false); err != nil || path != "" {
		t.Fatalf("SetLocalAPI(false) = %q, %v", path, err)
	}
	for _, name := range []string{rpc.SocketName, rpc.TokenName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after off: %v", name, err)
		}
	}
	if _, err := p.SetLocalAPI(2, false); err != nil { // already off
		t.Fatal(err)
	}
}

// Serve ending by itself closes that server, forgets it, and tells the App;
// the switch can start a fresh one afterwards.
func TestLocalAPIServeFailure(t *testing.T) {
	dir := shortDir(t)
	svc, err := core.New(context.Background(), core.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	stopped := make(chan error, 1)
	p := &platform{dataDir: dir, svc: svc, onAPIStop: func(gen uint64, err error) {
		if gen != 1 {
			t.Errorf("stopped for request %d, want 1", gen)
		}
		stopped <- err
	}}
	fail := errors.New("accept: too many open files")
	p.serve = func(context.Context, *rpc.Server) error { return fail }
	if _, err := p.SetLocalAPI(1, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, fail) {
			t.Fatalf("stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the App was not told")
	}
	p.apiMu.Lock()
	api := p.api
	p.apiMu.Unlock()
	if api != nil {
		t.Fatal("the dead server is still current")
	}
	if _, err := os.Stat(filepath.Join(dir, rpc.SocketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after the failure: %v", err)
	}
	p.serve = nil
	if _, err := p.SetLocalAPI(1, true); err != nil {
		t.Fatal(err)
	}
	dialWait(t, dir)
	if _, err := p.SetLocalAPI(2, false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		t.Fatalf("a requested stop told the App %v", err)
	default:
	}
}

func TestPlatformDataDir(t *testing.T) {
	def, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir, custom := (&platform{dataDir: def}).DataDir(); dir != def || custom {
		t.Errorf("default: %q, %v", dir, custom)
	}
	other := shortDir(t)
	if dir, custom := (&platform{dataDir: other}).DataDir(); dir != other || !custom {
		t.Errorf("custom: %q, %v", dir, custom)
	}
}

// The core.New failure dialog names a class of reason, never err's text.
func TestOpenFailure(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	i18n.SetLanguage("zh-CN")
	secret := "/Users/someone/secret path"
	perm := openFailure(&os.PathError{Op: "open", Path: secret, Err: fs.ErrPermission})
	other := openFailure(fmt.Errorf("migrate %s: boom", secret))
	if perm != "Godwit 无法打开数据目录：没有访问权限。" || other != "Godwit 无法打开数据目录：数据库无法打开。" {
		t.Fatalf("perm %q, other %q", perm, other)
	}
	for _, m := range []string{perm, other} {
		if strings.Contains(m, secret) || strings.Contains(m, "boom") {
			t.Fatalf("%q carries the error's text", m)
		}
	}
}

// The dead server's cleanup cannot remove a newer server's socket and token:
// a SetLocalAPI(true) during it waits for it.
func TestLocalAPICleanupRace(t *testing.T) {
	dir := shortDir(t)
	svc, err := core.New(context.Background(), core.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	closing, release := make(chan struct{}), make(chan struct{})
	var dead *rpc.Server
	cleaned := make(chan struct{})
	p := &platform{dataDir: dir, svc: svc, onAPIStop: func(uint64, error) { close(cleaned) }}
	p.serve = func(_ context.Context, s *rpc.Server) error { dead = s; return errors.New("accept failed") }
	p.closeAPI = func(s *rpc.Server) error {
		if s == dead {
			close(closing)
			<-release
		}
		return s.Close()
	}
	if _, err := p.SetLocalAPI(1, true); err != nil {
		t.Fatal(err)
	}
	<-closing // the dead server's cleanup has begun
	p.serve = nil
	started := make(chan error, 1)
	go func() { _, err := p.SetLocalAPI(3, true); started <- err }()
	select {
	case err := <-started: // it did not wait: the cleanup now runs after it
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		close(release)
		if err := <-started; err != nil {
			t.Fatal(err)
		}
	}
	<-cleaned
	for _, name := range []string{rpc.SocketName, rpc.TokenName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("the new server's %s: %v", name, err)
		}
	}
	dialWait(t, dir)
	p.SetLocalAPI(4, false)
}

// Only a GUI on the default data directory takes mygo's single-instance
// lock (a second one focuses the first); one on another directory runs
// beside it, guarded by that directory's owner lock alone.
func TestSingleInstanceOnlyOnDefaultDir(t *testing.T) {
	def, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if !singleInstance(def) || !singleInstance(def+string(filepath.Separator)) {
		t.Error("the default directory does not take the single-instance lock")
	}
	dir := shortDir(t)
	if singleInstance(dir) {
		t.Error("a custom directory takes the single-instance lock")
	}
	// The owner lock still keeps a second GUI off that directory.
	release, err := ownerlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ownerlock.Acquire(dir); !errors.Is(err, ownerlock.ErrAnotherOwner) {
		t.Fatalf("second owner: %v", err)
	}
	if _, code := lockFailure(ownerlock.ErrAnotherOwner); code != 2 {
		t.Fatalf("exit code %d", code)
	}
}

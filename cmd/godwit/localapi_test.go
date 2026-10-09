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
	path, err := p.SetLocalAPI(true)
	if err != nil || path != filepath.Join(dir, rpc.SocketName) {
		t.Fatalf("SetLocalAPI(true) = %q, %v", path, err)
	}
	if again, err := p.SetLocalAPI(true); err != nil || again != path { // already on: the same server
		t.Fatalf("SetLocalAPI(true) again = %q, %v", again, err)
	}
	var accs []core.Account
	if err := dialWait(t, dir).Call(context.Background(), "ListAccounts", &accs); err != nil {
		t.Fatal(err)
	}
	if path, err := p.SetLocalAPI(false); err != nil || path != "" {
		t.Fatalf("SetLocalAPI(false) = %q, %v", path, err)
	}
	for _, name := range []string{rpc.SocketName, rpc.TokenName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after off: %v", name, err)
		}
	}
	if _, err := p.SetLocalAPI(false); err != nil { // already off
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
	p := &platform{dataDir: dir, svc: svc, onAPIStop: func(err error) { stopped <- err }}
	fail := errors.New("accept: too many open files")
	p.serve = func(context.Context, *rpc.Server) error { return fail }
	if _, err := p.SetLocalAPI(true); err != nil {
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
	if _, err := p.SetLocalAPI(true); err != nil {
		t.Fatal(err)
	}
	dialWait(t, dir)
	if _, err := p.SetLocalAPI(false); err != nil {
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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
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

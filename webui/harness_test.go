package webui

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/godwit-email/godwit/uiclient"
)

// uiTransport is the env var that picks how the pages reach the Service:
// "socket" makes them a ui-role client of a backend serving the test's Env
// over the Local API socket; anything else calls the Service in process.
const uiTransport = "GODWIT_UI_TRANSPORT"

var uiClients sync.Map // *coretest.Env → Service, so a test's pages share one connection

// uiService is what the pages get for e: e.Svc in process, or in socket mode
// a client dialed as the ui role. Setup and assertions keep using e.Svc.
func uiService(t testing.TB, e *coretest.Env) Service {
	t.Helper()
	if os.Getenv(uiTransport) != "socket" {
		return e.Svc
	}
	if svc, ok := uiClients.Load(e); ok {
		return svc.(Service)
	}
	dir, err := os.MkdirTemp("", "gw") // short: the Unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	release, err := ownerlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release() })
	srv, err := rpcbind.ListenWith(dir, e.Svc, rpcbind.ListenOptions{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(context.Background())
	cl, err := uiclient.Dial(dir, "")
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { // before the Env's cleanup closes the Service
		cl.Close()
		srv.Close()
		uiClients.Delete(e)
	})
	uiClients.Store(e, cl)
	return cl
}

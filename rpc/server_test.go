package rpc_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/rpc"
)

// api is a server target with one method the mcp role may call, one it may
// not, and a stream.
type api struct {
	sent    atomic.Int64
	stopped chan struct{}
}

func (a *api) ListAccounts() []string { return []string{"a@example.com"} }
func (a *api) Send(id int64) error    { a.sent.Add(1); return nil }
func (a *api) Watch(ctx context.Context) (<-chan int, func()) {
	ch := make(chan int)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, func() { close(a.stopped) }
}

// shortDir is a data directory short enough for a Unix socket path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startServer(t *testing.T, dir string, opts rpc.ServerOptions) (*rpc.Server, *api, <-chan error) {
	t.Helper()
	a := &api{stopped: make(chan struct{})}
	d, err := rpc.NewDispatcher(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Version == "" {
		opts.Version = "test"
	}
	s, err := rpc.Listen(dir, d, opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	t.Cleanup(func() { s.Close() })
	return s, a, done
}

func dialRaw(t *testing.T, s *rpc.Server) *raw {
	t.Helper()
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return newRaw(t, c)
}

func token(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rpc.TokenName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCode(t *testing.T, m wire, code int) {
	t.Helper()
	if m.Error == nil || m.Error.Code != code {
		t.Fatalf("= %+v, want error %d", m, code)
	}
}

func TestAuth(t *testing.T) {
	dir := shortDir(t)
	s, a, _ := startServer(t, dir, rpc.ServerOptions{Conn: rpc.ConnOptions{AuthTimeout: 200 * time.Millisecond}})
	tok := token(t, dir)

	t.Run("no hello", func(t *testing.T) {
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"ListAccounts","params":[]}`)
		wantCode(t, r.recv(), rpc.CodeUnauthenticated)
		r.closed()
	})
	t.Run("wrong token", func(t *testing.T) {
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","full","t"]}`, strings.Repeat("0", 64))
		m := r.recv()
		wantCode(t, m, rpc.CodeUnauthenticated)
		if m.Error.Data == nil || m.Error.Data.Code != "unauthenticated" {
			t.Fatalf("data = %+v", m.Error.Data)
		}
		r.closed()
	})
	t.Run("bad role", func(t *testing.T) {
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","admin","t"]}`, tok)
		wantCode(t, r.recv(), rpc.CodeUnauthenticated)
		r.closed()
	})
	t.Run("timeout", func(t *testing.T) {
		r := dialRaw(t, s)
		r.closed() // nothing sent: dropped after AuthTimeout
	})
	t.Run("full, then a second hello", func(t *testing.T) {
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","full","t"]}`, tok)
		if m := r.recv(); string(m.Result) != `{"role":"full","server":"godwit/test"}` {
			t.Fatalf("hello = %+v", m)
		}
		r.send(`{"jsonrpc":"2.0","id":2,"method":"hello","params":["%s","full","t"]}`, tok)
		wantCode(t, r.recv(), rpc.CodeInvalidRequest)
		r.send(`{"jsonrpc":"2.0","id":3,"method":"Send","params":[1]}`)
		if m := r.recv(); m.Error != nil {
			t.Fatalf("Send = %+v", m)
		}
	})
	t.Run("mcp cannot switch to full", func(t *testing.T) {
		before := a.sent.Load()
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","mcp","t"]}`, tok)
		if m := r.recv(); string(m.Result) != `{"role":"mcp","server":"godwit/test"}` {
			t.Fatalf("hello = %+v", m)
		}
		r.send(`{"jsonrpc":"2.0","id":2,"method":"Send","params":[1]}`)
		wantCode(t, r.recv(), rpc.CodeForbidden)
		r.send(`{"jsonrpc":"2.0","id":3,"method":"hello","params":["%s","full","t"]}`, tok)
		wantCode(t, r.recv(), rpc.CodeInvalidRequest)
		r.send(`{"jsonrpc":"2.0","id":4,"method":"Send","params":[1]}`)
		wantCode(t, r.recv(), rpc.CodeForbidden)
		r.send(`{"jsonrpc":"2.0","id":5,"method":"ListAccounts","params":[]}`)
		if m := r.recv(); m.Error != nil {
			t.Fatalf("ListAccounts = %+v", m)
		}
		if a.sent.Load() != before {
			t.Fatal("a forbidden Send ran")
		}
	})
}

func TestMCPAllowedDraftModes(t *testing.T) {
	p := func(mode string) []json.RawMessage {
		return []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"` + mode + `"`), json.RawMessage(`2`)}
	}
	for mode, want := range map[string]bool{"new": true, "reply": true, "reply_all": true, "forward": false, "": false} {
		if got := rpc.MCPAllowed("NewDraft", p(mode)); got != want {
			t.Errorf("NewDraft %q = %v", mode, got)
		}
	}
	if rpc.MCPAllowed("NewDraft", nil) || rpc.MCPAllowed("NewDraft", []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`7`)}) {
		t.Error("NewDraft without a string mode allowed")
	}
}

func TestStaleSocket(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, rpc.SocketName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // a crashed owner's leftover
	if _, err := os.Lstat(path); err != nil {
		t.Fatal(err)
	}
	startServer(t, dir, rpc.ServerOptions{})
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	other := shortDir(t)
	if err := os.WriteFile(filepath.Join(other, rpc.SocketName), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := rpc.NewDispatcher(&api{}, nil)
	if _, err := rpc.Listen(other, d, rpc.ServerOptions{}); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("Listen over a regular file: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(other, rpc.SocketName)); string(b) != "keep" {
		t.Fatal("the regular file was touched")
	}
}

func TestLongSocketPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sun_path limit to check")
	}
	dir := filepath.Join(shortDir(t), strings.Repeat("d", 120))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d, _ := rpc.NewDispatcher(&api{}, nil)
	if _, err := rpc.Listen(dir, d, rpc.ServerOptions{}); err == nil || !strings.Contains(err.Error(), "shorter data directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenFilePerms(t *testing.T) {
	dir := shortDir(t)
	s, _, _ := startServer(t, dir, rpc.ServerOptions{})
	tok := token(t, dir)
	if b, err := hex.DecodeString(tok); err != nil || len(b) != 32 {
		t.Fatalf("token %q is not 32 hex bytes", tok)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{filepath.Join(dir, rpc.TokenName), s.Path()} {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: mode %v, want 0600", p, fi.Mode().Perm())
			}
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("leftover files: %v", entries)
	}
	s.Close()
	startServer(t, dir, rpc.ServerOptions{})
	if token(t, dir) == tok {
		t.Fatal("token reused across starts")
	}
}

func TestCloseCleansUp(t *testing.T) {
	dir := shortDir(t)
	s, a, done := startServer(t, dir, rpc.ServerOptions{})
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	events, _, err := c.Stream(context.Background(), "Watch")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select { // Close returned, so the stream was stopped and drained
	case <-a.stopped:
	default:
		t.Fatal("stream not stopped when Close returned")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	for range events {
	}
	for _, name := range []string{rpc.SocketName, rpc.TokenName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", name, err)
		}
	}
	if _, err := rpc.Dial(dir, rpc.RoleFull, "t", nil); err == nil {
		t.Fatal("Dial after Close succeeded")
	}
}

func TestMaxConns(t *testing.T) {
	dir := shortDir(t)
	startServer(t, dir, rpc.ServerOptions{MaxConns: 1})
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.Dial(dir, rpc.RoleFull, "t", nil); err == nil {
		t.Fatal("a second connection past MaxConns was served")
	}
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not freed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDialFull(t *testing.T) {
	dir := shortDir(t)
	var mu sync.Mutex
	var audited []string
	_, a, _ := startServer(t, dir, rpc.ServerOptions{Audit: func(role, method string, _ []json.RawMessage) error {
		mu.Lock()
		defer mu.Unlock()
		audited = append(audited, role+" "+method)
		if method == "ListAccounts" && role == rpc.RoleMCP {
			return errors.New("audit log unavailable")
		}
		return nil
	}})
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var accs []string
	if err := c.Call(context.Background(), "ListAccounts", &accs); err != nil || len(accs) != 1 {
		t.Fatalf("ListAccounts = %v, %v", accs, err)
	}
	if err := c.Call(context.Background(), "Send", nil, 7); err != nil || a.sent.Load() != 1 {
		t.Fatalf("Send: %v", err)
	}

	m, err := rpc.Dial(dir, rpc.RoleMCP, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var we *rpc.WireError
	if err := m.Call(context.Background(), "Send", nil, 7); !errors.As(err, &we) || we.Code != rpc.CodeForbidden {
		t.Fatalf("mcp Send = %v, want forbidden", err)
	}
	// An audit that cannot be written refuses the call.
	if err := m.Call(context.Background(), "ListAccounts", nil); !errors.As(err, &we) || we.Code != rpc.CodeForbidden {
		t.Fatalf("mcp ListAccounts with a failing audit = %v, want forbidden", err)
	}
	want := "full ListAccounts,full Send,mcp ListAccounts" // a forbidden call is not audited
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(audited, ","); got != want {
		t.Fatalf("audited %s, want %s", got, want)
	}
}

package rpc_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	sent     atomic.Int64
	stopped  chan struct{}
	stopOnce sync.Once
	slowStop time.Duration // the stream's stop takes this long
}

func (a *api) ListAccounts() []string { return []string{"a@example.com"} }
func (a *api) Send(id int64) error    { a.sent.Add(1); return nil }
func (a *api) Fail(mode string) error { return fmt.Errorf("bad mode %q", mode) }
func (a *api) InboxOnly() bool        { return true }
func (a *api) Watch(ctx context.Context) (<-chan int, func()) {
	ch := make(chan int)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, func() { time.Sleep(a.slowStop); a.stopOnce.Do(func() { close(a.stopped) }) }
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

func uiToken(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rpc.UITokenName))
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
	utok := uiToken(t, dir)
	if tok == utok {
		t.Fatal("ui-token and api-token are identical")
	}
	for name, val := range map[string]string{rpc.TokenName: tok, rpc.UITokenName: utok} {
		if b, err := hex.DecodeString(val); err != nil || len(b) != 32 {
			t.Fatalf("%s %q is not 32 hex bytes", name, val)
		}
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{filepath.Join(dir, rpc.TokenName), filepath.Join(dir, rpc.UITokenName), s.Path()} {
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
	if len(entries) != 3 {
		t.Errorf("leftover files: %v", entries)
	}
	s.Close()
	startServer(t, dir, rpc.ServerOptions{})
	if token(t, dir) == tok || uiToken(t, dir) == utok {
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
	for _, name := range []string{rpc.SocketName, rpc.UITokenName, rpc.TokenName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", name, err)
		}
	}
	if _, err := rpc.Dial(dir, rpc.RoleFull, "t", nil); err == nil {
		t.Fatal("Dial after Close succeeded")
	}
	if _, err := rpc.Dial(dir, rpc.RoleUI, "t", nil); err == nil {
		t.Fatal("UI Dial after Close succeeded")
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
	// External clients never use the interface's room. The refused connection
	// holds an accept slot until its server goroutine ends, which can be just
	// after the client saw the refusal, so give the interface until then.
	var ui *rpc.Client
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if ui, err = rpc.Dial(dir, rpc.RoleUI, "t", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the interface was refused with MaxConns external connections open: %v", err)
		}
	}
	ui.Close()
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
	// An audit that cannot be written stops the call as an internal error.
	if err := m.Call(context.Background(), "ListAccounts", nil); !errors.As(err, &we) || we.Code != rpc.CodeInternal {
		t.Fatalf("mcp ListAccounts with a failing audit = %v, want internal error", err)
	}
	want := "full ListAccounts,full Send,mcp ListAccounts" // a forbidden call is not audited
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(audited, ","); got != want {
		t.Fatalf("audited %s, want %s", got, want)
	}
}

// logs captures slog output until the test ends.
func logs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var b strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func() string { mu.Lock(); defer mu.Unlock(); return b.String() }
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// A failing audit runs nothing and answers "internal error"; the server log
// has the cause, redacted.
func TestAuditFailure(t *testing.T) {
	logged := logs(t)
	dir := shortDir(t)
	_, a, _ := startServer(t, dir, rpc.ServerOptions{
		Redact: func(s string) string { return strings.ReplaceAll(s, "hunter2", "[redacted]") },
		Audit:  func(string, string, []json.RawMessage) error { return errors.New("audit db down, pw hunter2") },
	})
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var we *rpc.WireError
	if err := c.Call(context.Background(), "Send", nil, 7); !errors.As(err, &we) || we.Code != rpc.CodeInternal || we.Message != "internal error" {
		t.Fatalf("Send = %v, want internal error", err)
	}
	if a.sent.Load() != 0 {
		t.Fatal("Send ran although its audit failed")
	}
	if l := logged(); !strings.Contains(l, "audit db down") || strings.Contains(l, "hunter2") {
		t.Fatalf("log = %q, want the redacted cause", l)
	}
}

// Godwit's own interface gets an unregistered error's text, redacted, to show
// it; an external client still gets "internal error".
func TestUnregisteredErrorTextForUI(t *testing.T) {
	dir := shortDir(t)
	startServer(t, dir, rpc.ServerOptions{
		Redact: func(s string) string { return strings.ReplaceAll(s, "hunter2", "[redacted]") },
		Audit:  func(string, string, []json.RawMessage) error { return errors.New("config plan: bad, pw hunter2") },
	})
	for role, want := range map[string]string{rpc.RoleUI: "config plan: bad, pw [redacted]", rpc.RoleFull: "internal error"} {
		c, err := rpc.Dial(dir, role, "t", nil)
		if err != nil {
			t.Fatal(err)
		}
		var we *rpc.WireError
		if err := c.Call(context.Background(), "Send", nil, 7); !errors.As(err, &we) || we.Code != rpc.CodeInternal || we.Message != want {
			t.Errorf("%s Send = %v, want %q", role, err, want)
		}
		c.Close()
	}
}

// The token never reaches an error response or the log.
func TestTokenRedacted(t *testing.T) {
	logged := logs(t)
	dir := shortDir(t)
	startServer(t, dir, rpc.ServerOptions{})
	tok := token(t, dir)
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(context.Background(), "Fail", nil, tok)
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("Fail = %v", err)
	}
	if l := logged(); !strings.Contains(l, "bad mode") || strings.Contains(l, tok) {
		t.Fatalf("log = %q, want the cause without the token", l)
	}
}

// Close and a cancelled Serve context racing: both return only after the
// streams finished and the files are gone.
func TestConcurrentClose(t *testing.T) {
	dir := shortDir(t)
	a := &api{stopped: make(chan struct{}), slowStop: 300 * time.Millisecond}
	d, _ := rpc.NewDispatcher(a, nil)
	s, err := rpc.Listen(dir, d, rpc.ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	c, err := rpc.Dial(dir, rpc.RoleFull, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, err := c.Stream(context.Background(), "Watch"); err != nil {
		t.Fatal(err)
	}
	cancel() // Serve's own Close starts …
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, err := os.Lstat(s.Path()); errors.Is(err, os.ErrNotExist) {
			break // … and has closed the listener
		}
		if time.Now().After(deadline) {
			t.Fatal("ctx did not close the server")
		}
	}
	s.Close() // … while this one joins it
	check := func(who string) {
		select {
		case <-a.stopped:
		default:
			t.Fatalf("%s returned before the stream stopped", who)
		}
		for _, name := range []string{rpc.UITokenName, rpc.TokenName} {
			if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s returned before %s was removed", who, name)
			}
		}
	}
	check("Close")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	check("Serve")
}

func TestRoleUIDispatcherAndMaxActive(t *testing.T) {
	dir := shortDir(t)
	a := &api{stopped: make(chan struct{})}
	fullD, err := rpc.NewDispatcher(a, func(m string) bool { return m != "InboxOnly" })
	if err != nil {
		t.Fatal(err)
	}
	uiD, err := rpc.NewDispatcher(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := rpc.Listen(dir, fullD, rpc.ServerOptions{Version: "test", UIDispatcher: uiD})
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(context.Background())
	t.Cleanup(func() { s.Close() })

	ui, err := rpc.Dial(dir, rpc.RoleUI, "ui-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Close()
	var on bool
	if err := ui.Call(context.Background(), "InboxOnly", &on); err != nil || !on {
		t.Fatalf("ui InboxOnly = %v, %v", on, err)
	}
	var accs []string
	if err := ui.Call(context.Background(), "ListAccounts", &accs); err != nil || len(accs) != 1 {
		t.Fatalf("ui ListAccounts = %v, %v", accs, err)
	}

	// RoleUI allows more than the default 32 active streams/requests.
	cancels := make([]func(), 40)
	for i := range cancels {
		_, cancel, err := ui.Stream(context.Background(), "Watch")
		if err != nil {
			t.Fatalf("stream %d = %v", i, err)
		}
		cancels[i] = cancel
	}
	for _, cancel := range cancels {
		cancel()
	}

	full, err := rpc.Dial(dir, rpc.RoleFull, "full-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	var we *rpc.WireError
	if err := full.Call(context.Background(), "InboxOnly", &on); !errors.As(err, &we) || we.Code != rpc.CodeMethodNotFound {
		t.Fatalf("full InboxOnly = %v, want CodeMethodNotFound", err)
	}

	mcp, err := rpc.Dial(dir, rpc.RoleMCP, "mcp-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcp.Close()
	if err := mcp.Call(context.Background(), "InboxOnly", &on); !errors.As(err, &we) || we.Code != rpc.CodeForbidden {
		t.Fatalf("mcp InboxOnly = %v, want CodeForbidden", err)
	}
}

func TestDualTokensAndSetExternal(t *testing.T) {
	logged := logs(t)
	dir := shortDir(t)
	// A leftover api-token from a previous run is cleared when starting with external off.
	if err := os.WriteFile(filepath.Join(dir, rpc.TokenName), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, _ := startServer(t, dir, rpc.ServerOptions{DisableExternal: true})

	utok := uiToken(t, dir)
	if _, err := os.Stat(filepath.Join(dir, rpc.TokenName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("api-token with DisableExternal: %v", err)
	}

	// External off: RoleUI connects and works; RoleFull and RoleMCP fail.
	ui, err := rpc.Dial(dir, rpc.RoleUI, "ui-test", nil)
	if err != nil {
		t.Fatalf("Dial RoleUI with external off: %v", err)
	}
	defer ui.Close()
	var accs []string
	if err := ui.Call(context.Background(), "ListAccounts", &accs); err != nil || len(accs) != 1 {
		t.Fatalf("ui ListAccounts = %v, %v", accs, err)
	}
	uiEvents, uiCancel, err := ui.Stream(context.Background(), "Watch")
	if err != nil {
		t.Fatalf("ui Watch = %v", err)
	}
	defer uiCancel()

	// Redact with disabled empty api-token must not insert [redacted] between runes,
	// while still redacting ui-token.
	_ = ui.Call(context.Background(), "Fail", nil, "plain "+utok)
	if l := logged(); !strings.Contains(l, `bad mode \"plain [redacted]\"`) && !strings.Contains(l, `bad mode "plain [redacted]"`) {
		t.Fatalf("log with empty api-token = %q", l)
	}
	if strings.Contains(logged(), utok) {
		t.Fatalf("ui-token leaked into log: %q", logged())
	}

	for _, role := range []string{rpc.RoleFull, rpc.RoleMCP} {
		if c, err := rpc.Dial(dir, role, "ext-test", nil); err == nil {
			c.Close()
			t.Fatalf("Dial %s succeeded with external off", role)
		}
		// Passing ui-token on the socket for full/mcp is rejected.
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","%s","ext"]}`, utok, role)
		wantCode(t, r.recv(), rpc.CodeUnauthenticated)
		r.closed()
	}

	// Enable external access dynamically: api-token appears (0600), idempotent.
	if err := s.SetExternal(true); err != nil {
		t.Fatalf("SetExternal(true): %v", err)
	}
	atok := token(t, dir)
	if atok == utok {
		t.Fatal("api-token equals ui-token")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, rpc.TokenName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("api-token stat = %v, %v", fi, err)
		}
	}
	if err := s.SetExternal(true); err != nil {
		t.Fatalf("SetExternal(true) idempotent: %v", err)
	}
	if got := token(t, dir); got != atok {
		t.Fatalf("SetExternal(true) rotated active token: %q != %q", got, atok)
	}

	// Token separation: api-token cannot claim RoleUI; ui-token still cannot claim RoleFull/RoleMCP.
	{
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","ui","ext"]}`, atok)
		wantCode(t, r.recv(), rpc.CodeUnauthenticated)
		r.closed()
	}
	for _, role := range []string{rpc.RoleFull, rpc.RoleMCP} {
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","%s","ui"]}`, utok, role)
		wantCode(t, r.recv(), rpc.CodeUnauthenticated)
		r.closed()
	}

	full, err := rpc.Dial(dir, rpc.RoleFull, "full-test", nil)
	if err != nil {
		t.Fatalf("Dial RoleFull: %v", err)
	}
	defer full.Close()
	mcp, err := rpc.Dial(dir, rpc.RoleMCP, "mcp-test", nil)
	if err != nil {
		t.Fatalf("Dial RoleMCP: %v", err)
	}
	defer mcp.Close()
	if err := full.Call(context.Background(), "ListAccounts", &accs); err != nil {
		t.Fatalf("full ListAccounts: %v", err)
	}
	if err := mcp.Call(context.Background(), "ListAccounts", &accs); err != nil {
		t.Fatalf("mcp ListAccounts: %v", err)
	}
	_ = full.Call(context.Background(), "Fail", nil, atok)
	if strings.Contains(logged(), atok) {
		t.Fatalf("api-token leaked into log: %q", logged())
	}

	// Disable external access while connected: api-token disappears, full and mcp
	// connections drop immediately, and RoleUI stays open and working.
	if err := s.SetExternal(false); err != nil {
		t.Fatalf("SetExternal(false): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rpc.TokenName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("api-token after SetExternal(false): %v", err)
	}
	for name, c := range map[string]*rpc.Client{"full": full, "mcp": mcp} {
		select {
		case <-c.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("%s connection was not dropped by SetExternal(false)", name)
		}
		if err := c.Call(context.Background(), "ListAccounts", &accs); err == nil {
			t.Fatalf("%s Call succeeded after SetExternal(false)", name)
		}
	}
	for _, role := range []string{rpc.RoleFull, rpc.RoleMCP} {
		r := dialRaw(t, s)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"hello","params":["%s","%s","ext"]}`, atok, role)
		wantCode(t, r.recv(), rpc.CodeUnauthenticated)
		r.closed()
	}

	// Live RoleUI connection and stream are uninterrupted.
	select {
	case <-ui.Done():
		t.Fatal("RoleUI connection was closed by SetExternal(false)")
	case _, ok := <-uiEvents:
		if !ok {
			t.Fatal("RoleUI stream was closed by SetExternal(false)")
		}
	default:
	}
	if err := ui.Call(context.Background(), "ListAccounts", &accs); err != nil || len(accs) != 1 {
		t.Fatalf("ui ListAccounts after SetExternal(false) = %v, %v", accs, err)
	}
	if err := s.SetExternal(false); err != nil {
		t.Fatalf("SetExternal(false) idempotent: %v", err)
	}
}

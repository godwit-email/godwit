// Package coretest is a real core.Service for tests outside core: a mocked
// keychain, fake IMAP / SMTP servers per account, a fake model service, a fake
// MCP server and fixture plugins, all on this machine.
package coretest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"
)

type (
	IMAP    = testmail.IMAP
	SMTP    = testmail.Inbox
	Turn    = testllm.Turn
	Call    = testllm.Call
	Request = testllm.Request
)

// EgressHost is the host fixture plugins fetch from, as http://egress.godwit.test:80/
// (the port written out, as declared); it reaches the Env's local server,
// whose every answer is "pong".
const EgressHost = "egress.godwit.test"

// Env is a running Service with the fake servers behind it.
type Env struct {
	Svc     *core.Service
	DataDir string

	mu      sync.Mutex
	servers map[int64]*mailServers
	started map[string]*mailServers // StartServers' servers by IMAP address, until an account adopts them
	egress  atomic.Int64
}

type mailServers struct {
	imap     *IMAP
	smtp     *SMTP
	smtpAddr string
}

type config struct{ autoSync bool }

type Option func(*config)

// WithoutAutoSync starts no background sync; Deliver still syncs.
func WithoutAutoSync() Option { return func(c *config) { c.autoSync = false } }

// New starts a Service in a temporary directory, closed when the test ends.
func New(t *testing.T, opts ...Option) *Env {
	t.Helper()
	cfg := config{autoSync: true}
	for _, o := range opts {
		o(&cfg)
	}
	core.SerializeKeyringForTest() // workers read credentials while tests write them
	claimEnv(t)                    // MockInit empties the keychain of any Env still running
	keyring.MockInit()
	dir := t.TempDir()
	svc, err := core.New(context.Background(), core.Options{DataDir: dir, AutoSync: cfg.autoSync, SystemLanguage: "zh-CN", ImageClient: imagefetch.NewClient(true)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	e := &Env{Svc: svc, DataDir: dir, servers: map[int64]*mailServers{}, started: map[string]*mailServers{}}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.egress.Add(1)
		fmt.Fprint(w, "pong")
	}))
	t.Cleanup(api.Close)
	svc.SetPluginNetForTest(netOptions(api.Listener.Addr().String()))
	return e
}

var live atomic.Bool // an Env is running

// claimEnv fails tb if another Env is still running, else marks one running
// until tb ends.
func claimEnv(tb testing.TB) {
	tb.Helper()
	if !live.CompareAndSwap(false, true) {
		tb.Fatalf("coretest: another Env is still alive; use one Env per test")
		return
	}
	tb.Cleanup(func() { live.Store(false) })
}

// netOptions is the plugins' network: EgressHost reaches egressAddr; besides
// it only loopback names and addresses resolve and dial, never the internet.
func netOptions(egressAddr string) plugins.NetOptions {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return plugins.NetOptions{Resolver: loopbackDNS{}, AllowHTTP: true, AllowLoopback: true,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("coretest: refusing to dial %s: only loopback is reachable", addr)
			}
			if addr == "127.0.0.1:80" { // EgressHost
				addr = egressAddr
			}
			return d.DialContext(ctx, network, addr)
		}}
}

type loopbackDNS struct{}

func (loopbackDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	switch h := strings.ToLower(host); {
	case h == EgressHost || h == "localhost":
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	case net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback():
		return []net.IPAddr{{IP: net.ParseIP(h)}}, nil
	}
	return nil, &net.DNSError{Err: "coretest: only loopback names resolve", Name: host, IsNotFound: true}
}

// EgressHits is how many requests reached EgressHost.
func (e *Env) EgressHits() int { return int(e.egress.Load()) }

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, n
}

// AddAccount adds email with its own fake IMAP and SMTP servers (the SMTP one
// files sent mail into the IMAP Sent folder). The first account is the default.
func (e *Env) AddAccount(t *testing.T, email string) core.Account {
	t.Helper()
	im := testmail.StartIMAP(t)
	addr, in := testmail.StartSMTP(t, testmail.SaveToSent(im))
	ih, ip := splitHostPort(t, im.Addr)
	sh, sp := splitHostPort(t, addr)
	e.mu.Lock()
	first := len(e.servers) == 0
	e.mu.Unlock()
	name, _, _ := strings.Cut(email, "@")
	acc, err := e.Svc.AddAccount(context.Background(), core.AccountInput{
		Email: email, Name: name, Username: "alice", Password: "secret", IsDefault: first,
		IMAPHost: ih, IMAPPort: ip, IMAPSecurity: core.SecurityNone,
		SMTPHost: sh, SMTPPort: sp, SMTPSecurity: core.SecurityNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.servers[acc.ID] = &mailServers{imap: im, smtp: in, smtpAddr: addr}
	e.mu.Unlock()
	return acc
}

// StartServers starts fake IMAP and SMTP servers for an account not added
// yet, as the onboarding form takes them, with the user and password they
// accept. Once an account is added with them, Deliver, IMAP, SMTP and Sent
// work for it as for AddAccount's.
func (e *Env) StartServers(t *testing.T) (imapHost string, imapPort int, smtpHost string, smtpPort int, user, password string) {
	t.Helper()
	im := testmail.StartIMAP(t)
	addr, in := testmail.StartSMTP(t, testmail.SaveToSent(im))
	imapHost, imapPort = splitHostPort(t, im.Addr)
	smtpHost, smtpPort = splitHostPort(t, addr)
	e.mu.Lock()
	e.started[im.Addr] = &mailServers{imap: im, smtp: in, smtpAddr: addr}
	e.mu.Unlock()
	return imapHost, imapPort, smtpHost, smtpPort, "alice", "secret"
}

func (e *Env) mail(accountID int64) *mailServers {
	e.mu.Lock()
	m := e.servers[accountID]
	e.mu.Unlock()
	if m != nil {
		return m
	}
	accs, _ := e.Svc.ListAccounts(context.Background()) // added with StartServers' servers?
	for _, a := range accs {
		addr := net.JoinHostPort(a.IMAPHost, strconv.Itoa(a.IMAPPort))
		e.mu.Lock()
		if s := e.started[addr]; a.ID == accountID && s != nil {
			e.servers[accountID], m = s, s
		}
		e.mu.Unlock()
	}
	if m == nil {
		panic(fmt.Sprintf("coretest: no account %d", accountID))
	}
	return m
}

// IMAP is the account's fake IMAP server, for fault injection.
func (e *Env) IMAP(accountID int64) *IMAP { return e.mail(accountID).imap }

// SMTP is the account's fake SMTP server, for fault injection.
func (e *Env) SMTP(accountID int64) *SMTP { return e.mail(accountID).smtp }

// Endpoints are the account's servers, as a manual server form takes them.
func (e *Env) Endpoints(accountID int64) (imapHost string, imapPort int, smtpHost string, smtpPort int) {
	m := e.mail(accountID)
	h, p, _ := net.SplitHostPort(m.imap.Addr)
	imapPort, _ = strconv.Atoi(p)
	sh, sp, _ := net.SplitHostPort(m.smtpAddr)
	smtpPort, _ = strconv.Atoi(sp)
	return h, imapPort, sh, smtpPort
}

// Deliver appends raw to the account's folder on its IMAP server and syncs.
func (e *Env) Deliver(t *testing.T, accountID int64, folder string, raw []byte) {
	t.Helper()
	if _, err := e.IMAP(accountID).User.Append(folder, bytes.NewReader(raw), &imap.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Svc.SyncNow(context.Background(), accountID); err != nil {
		t.Fatal(err)
	}
}

// Sent is every message the account's SMTP server accepted, as sent.
func (e *Env) Sent(accountID int64) [][]byte {
	var out [][]byte
	for _, m := range e.SMTP(accountID).Messages() {
		out = append(out, m.Data)
	}
	return out
}

type msgConfig struct {
	inReplyTo, messageID, html string
	date                       time.Time
	atts                       []attachment
}

type attachment struct {
	name, ctype string
	data        []byte
}

type MsgOption func(*msgConfig)

func InReplyTo(id string) MsgOption { return func(c *msgConfig) { c.inReplyTo = id } }
func MessageID(id string) MsgOption { return func(c *msgConfig) { c.messageID = id } }

// HTML adds an HTML alternative to the text body.
func HTML(html string) MsgOption { return func(c *msgConfig) { c.html = html } }
func Date(d time.Time) MsgOption { return func(c *msgConfig) { c.date = d } }
func Attachment(name, ctype string, data []byte) MsgOption {
	return func(c *msgConfig) { c.atts = append(c.atts, attachment{name, ctype, data}) }
}

// leaf is a base64 body part: base64 never contains the "--coretest-" boundaries.
func leaf(header string, data []byte) string {
	return header + "Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(data) + "\r\n"
}

func multipart(kind, boundary string, parts ...string) string {
	s := "Content-Type: multipart/" + kind + "; boundary=" + boundary + "\r\n\r\n"
	for _, p := range parts {
		s += "--" + boundary + "\r\n" + p
	}
	return s + "--" + boundary + "--\r\n"
}

// Msg builds a raw message. Without MessageID it gets a random one; without
// Date, the current time.
func Msg(from, to, subject, body string, opts ...MsgOption) []byte {
	c := msgConfig{messageID: rand.Text() + "@coretest", date: time.Now()}
	for _, o := range opts {
		o(&c)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <%s>\r\nFrom: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n",
		c.messageID, from, to, mime.QEncoding.Encode("utf-8", subject), c.date.Format(time.RFC1123Z))
	if c.inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\nReferences: <%s>\r\n", c.inReplyTo, c.inReplyTo)
	}
	part := leaf("Content-Type: text/plain; charset=utf-8\r\n", []byte(body))
	if c.html != "" {
		part = multipart("alternative", "coretest-alt", part, leaf("Content-Type: text/html; charset=utf-8\r\n", []byte(c.html)))
	}
	if len(c.atts) > 0 {
		parts := []string{part}
		for _, a := range c.atts {
			parts = append(parts, leaf(fmt.Sprintf("Content-Type: %s\r\nContent-Disposition: attachment; filename=%q\r\n", a.ctype, a.name), a.data))
		}
		part = multipart("mixed", "coretest-mixed", parts...)
	}
	b.WriteString(part)
	return []byte(b.String())
}

// LLM is a fake OpenAI-compatible model service.
type LLM struct{ srv *testllm.Server }

// LLM registers a fake model service "test" with model "m1" and makes
// "test/m1" the default model.
func (e *Env) LLM(t *testing.T) *LLM {
	t.Helper()
	srv := testllm.Start(t, "openai")
	srv.SetModels("m1")
	ctx := context.Background()
	if _, err := e.Svc.AddModelService(ctx, core.ModelServiceInput{Name: "test", Kind: "openai", BaseURL: srv.URL, APIKey: "test-key", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	st, err := e.Svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.DefaultModel = "test/m1"
	if _, err := e.Svc.SetSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	return &LLM{srv}
}

// Script appends model turns; once they run out every request gets "ok".
func (l *LLM) Script(turns ...Turn) { l.srv.Script(turns...) }

// Requests is how many chat requests the service got.
func (l *LLM) Requests() int { return len(l.srv.Requests()) }

// RequestLog returns every chat request the service got, raw.
func (l *LLM) RequestLog() []Request { return l.srv.Requests() }

// Release lets hanging turns finish.
func (l *LLM) Release() { l.srv.Release() }

// PluginFixture copies the fixture plugin name into a temporary directory and
// returns it. Fixtures are looked up in core/coretest/testdata,
// core/plugins/testdata and core/testdata, in that order.
func PluginFixture(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	here := filepath.Dir(file)
	for _, root := range []string{filepath.Join(here, "testdata"), filepath.Join(here, "..", "plugins", "testdata"), filepath.Join(here, "..", "testdata")} {
		src := filepath.Join(root, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(t.TempDir(), name)
		if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	t.Fatalf("coretest: no fixture plugin %q", name)
	return ""
}

// InstallPlugin installs the fixture plugin name, grants its permissions and enables it.
func (e *Env) InstallPlugin(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	info, err := e.Svc.InstallPlugin(ctx, PluginFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Svc.GrantPermission(ctx, info.Name); err != nil {
		t.Fatal(err)
	}
	if err := e.Svc.SetPluginEnabled(ctx, info.Name, true); err != nil {
		t.Fatal(err)
	}
}

// mcpChildEnv makes the test binary serve MCP over stdio (ServeMCPIfChild).
const mcpChildEnv = "GODWIT_CORETEST_MCP"

// ServeMCPIfChild, called first in TestMain, turns a test binary started by
// MCPCommand into a stdio MCP server with one read-only tool, lookup, and
// exits when the client goes away. Otherwise it returns at once.
func ServeMCPIfChild() {
	if os.Getenv(mcpChildEnv) == "" {
		return
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "coretest", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "lookup", Description: "Looks things up.", InputSchema: map[string]any{"type": "object"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + string(r.Params.Arguments)}}}, nil
		})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "coretest MCP server:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// MCPCommand is a stdio MCP server command: the test binary itself, whose
// TestMain must call ServeMCPIfChild.
func MCPCommand(t *testing.T) (cmd string, args []string, env map[string]string) {
	t.Helper()
	return os.Args[0], nil, map[string]string{mcpChildEnv: "serve-lookup"}
}

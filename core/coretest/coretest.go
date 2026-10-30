// Package coretest is a real core.Service for tests outside core: a mocked
// keychain, fake IMAP / SMTP servers per account, a fake model service, a fake
// MCP server and fixture plugins, all on this machine.
package coretest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testclock"
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

// The SMTP stages SMTP.Reply answers at.
const (
	AtRcpt = testmail.AtRcpt
	AtData = testmail.AtData
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
	clock   *testclock.Clock
}

type mailServers struct {
	imap     *IMAP
	smtp     *SMTP
	smtpAddr string
}

type config struct {
	autoSync  bool
	registry  *core.RegistrySource
	sys       core.SystemTranslator
	confirm   bool
	unsub     *http.Client
	model     mask.Recognizer
	maskModel []string
}

// WithMaskModelSources has the Service download 识别模型's files from urls
// (core.Options.MaskModelSources), as httptest servers'.
func WithMaskModelSources(urls ...string) Option { return func(c *config) { c.maskModel = urls } }

type Option func(*config)

// WithRegistry gives the Service a plugin registry, as from NewRegistryServer.
func WithRegistry(src *core.RegistrySource) Option { return func(c *config) { c.registry = src } }

// WithSystemTranslator gives the Service the system's translation.
func WithSystemTranslator(t core.SystemTranslator) Option { return func(c *config) { c.sys = t } }

// WithConfirmWindow is the GUI's Service: a window can confirm godwit mcp's
// sends (core.Options.ConfirmWindow). Without it 每次确认 refuses them, as headless.
func WithConfirmWindow() Option { return func(c *config) { c.confirm = true } }

// WithUnsubscribeClient sends one-click unsubscribes with c, as one reaching an httptest server.
func WithUnsubscribeClient(c *http.Client) Option { return func(cfg *config) { cfg.unsub = c } }

// WithRecognizer reads for 识别模型 with r (core.Options.Recognizer).
func WithRecognizer(r mask.Recognizer) Option { return func(c *config) { c.model = r } }

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
	imageClient := imagefetch.NewClient(true)
	imageClient.Transport.(*imagefetch.Transport).DialContext = netOptions("").Dial
	svc, err := core.New(context.Background(), core.Options{DataDir: dir, AutoSync: cfg.autoSync, SystemLanguage: "zh-CN", ImageClient: imageClient,
		Registry: cfg.registry, AppVersion: "1.2.0", SystemTranslator: cfg.sys, ConfirmWindow: cfg.confirm, UnsubscribeClient: cfg.unsub, Recognizer: cfg.model,
		MaskModelSources: cfg.maskModel, MaskModelClient: &http.Client{Transport: imageClient.Transport}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	e := &Env{Svc: svc, DataDir: dir, servers: map[int64]*mailServers{}, started: map[string]*mailServers{}, clock: testclock.New(time.Now())}
	svc.SetClockForTest(e.clock)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.egress.Add(1)
		fmt.Fprint(w, "pong")
	}))
	t.Cleanup(api.Close)
	svc.SetPluginNetForTest(netOptions(api.Listener.Addr().String()))
	svc.SetNativeHostForTest(NativeChildEnv + "=1")
	return e
}

// AdvanceClock moves the Service's clock forward by d, firing the waits that
// come due.
func (e *Env) AdvanceClock(d time.Duration) { e.clock.Advance(d) }

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
	header                     []string
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

// Header adds a header line, as "List-Id: <news.example>".
func Header(line string) MsgOption { return func(c *msgConfig) { c.header = append(c.header, line) } }
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
	for _, l := range c.header {
		b.WriteString(l + "\r\n")
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
	l := e.LLMService(t, "test", "openai")
	ctx := context.Background()
	st, err := e.Svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.DefaultModel = "test/m1"
	if _, err := e.Svc.SetSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	return l
}

// LLMService registers a fake model service name of kind ("openai",
// "openai-responses" or "anthropic") with model "m1": "<name>/m1" picks it.
func (e *Env) LLMService(t *testing.T, name, kind string) *LLM {
	t.Helper()
	srv := testllm.Start(t, kind)
	srv.SetModels("m1")
	in := core.ModelServiceInput{Name: name, Kind: kind, BaseURL: srv.URL, APIKey: "test-key", Models: []string{"m1"}}
	if _, err := e.Svc.AddModelService(context.Background(), in); err != nil {
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

// NativeChildEnv makes the test binary a fake plugin-host (ServeNativeIfChild);
// a Service gets it with SetNativeHostForTest(NativeChildEnv + "=1").
const NativeChildEnv = "GODWIT_CORETEST_NATIVE"

// ServeNativeIfChild serves as a native plugin's process when the test binary
// was started as one: it never loads a library (plugins.FakeNativeOpener). A
// TestMain whose tests run native plugins calls it first.
func ServeNativeIfChild() {
	if os.Getenv(NativeChildEnv) == "" || len(os.Args) < 2 || os.Args[1] != "plugin-host" {
		return
	}
	os.Exit(plugins.ServeNativeWith(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, plugins.FakeNativeOpener(os.Stderr)))
}

// NativePluginFixture writes a native plugin source directory: a manifest
// (capabilities translate; a text setting endpoint with a default and a
// secret token) and a fake library (see plugins.FakeNativeOpener) whose
// describe matches the manifest unless describe is given; extra is merged
// into the fake library's JSON (record, initFail, echoStderr).
func NativePluginFixture(t *testing.T, name, version string, describe, extra map[string]any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src-"+name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := fmt.Sprintf(`{"name":%q,"version":%q,"description":"A native test plugin","native":{"abi":1},"capabilities":["translate"],
		"config":[{"key":"endpoint","type":"text","label":"Endpoint","default":"https://default.example"},{"key":"token","type":"secret","label":"Token"}]}`, name, version)
	if describe == nil {
		describe = map[string]any{"name": name, "version": version, "abi": 1, "capabilities": []string{"translate"}}
	}
	lib := map[string]any{"describe": describe}
	for k, v := range extra {
		lib[k] = v
	}
	b, err := json.Marshal(lib)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plugins.LibraryName()), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
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

// RegistryServer is a plugin registry over https with its own signing key.
type RegistryServer struct {
	Src       *core.RegistrySource
	srv       *httptest.Server
	key       ed25519.PrivateKey
	mu        sync.Mutex
	docs      map[string][]byte
	intercept func(w http.ResponseWriter, req *http.Request, body []byte) bool
}

// Intercept lets fn answer a request first (body is what the path serves,
// nil if none); returning false serves it as usual. nil removes it.
func (r *RegistryServer) Intercept(fn func(w http.ResponseWriter, req *http.Request, body []byte) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.intercept = fn
}

// NewRegistryServer starts an empty registry; Publish puts one up.
func NewRegistryServer(t *testing.T) *RegistryServer {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &RegistryServer{key: key, docs: map[string][]byte{}}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		b, ok := r.docs[req.URL.Path]
		fn := r.intercept
		r.mu.Unlock()
		if fn != nil && fn(w, req, b) {
			return
		}
		if !ok {
			http.NotFound(w, req)
			return
		}
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(b)) // with Range, for resumed downloads
	}))
	t.Cleanup(r.srv.Close)
	r.Src = &core.RegistrySource{URL: r.srv.URL + "/registry.json", PublicKey: pub, Client: r.srv.Client()}
	return r
}

// URL is the server's address for path.
func (r *RegistryServer) URL(path string) string { return r.srv.URL + path }

// Serve puts body at path (an artifact), or removes it when body is nil.
func (r *RegistryServer) Serve(path string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if body == nil {
		delete(r.docs, path)
		return
	}
	r.docs[path] = body
}

// Publish signs and serves reg as /registry.json with /registry.json.sig.
func (r *RegistryServer) Publish(t *testing.T, reg map[string]any) {
	t.Helper()
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	r.Serve("/registry.json", b)
	r.Serve("/registry.json.sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(r.key, b))+"\n"))
}

// Tamper changes the published registry without signing it again.
func (r *RegistryServer) Tamper(fn func([]byte) []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs["/registry.json"] = fn(r.docs["/registry.json"])
}

// NativeRelease is a registry release of the NativePluginFixture plugin
// name at version for this platform, its library served from r.
func (r *RegistryServer) NativeRelease(t *testing.T, name, version string, extra map[string]any) map[string]any {
	t.Helper()
	src := NativePluginFixture(t, name, version, nil, extra)
	manifest, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile(filepath.Join(src, plugins.LibraryName()))
	if err != nil {
		t.Fatal(err)
	}
	return r.release(name, version, plugins.PlatformKey(), manifest, lib)
}

// NativeModelsRelease is a registry release of a native plugin offering
// models (the fake library's "models" list), recording each models.* request
// to record + "." + method.
func (r *RegistryServer) NativeModelsRelease(t *testing.T, name, version, record string, models []any) map[string]any {
	t.Helper()
	manifest := fmt.Sprintf(`{"name":%q,"version":%q,"native":{"abi":1},"capabilities":["models"]}`, name, version)
	lib, err := json.Marshal(map[string]any{"record": record, "models": map[string]any{"models": models},
		"describe": map[string]any{"name": name, "version": version, "abi": 1, "capabilities": []string{"models"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.release(name, version, plugins.PlatformKey(), []byte(manifest), lib)
}

// ScriptRelease is a registry release of a one-file script plugin.
func (r *RegistryServer) ScriptRelease(name, version, perms, code string) map[string]any {
	manifest := fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js","permissions":%s}`, name, version, perms)
	return r.release(name, version, plugins.AnyPlatform, []byte(manifest), []byte(code))
}

func (r *RegistryServer) release(name, version, key string, manifest, file []byte) map[string]any {
	path := "/files/" + name + "-" + version + "-" + key
	r.Serve(path, file)
	sum := sha256.Sum256(file)
	return map[string]any{"version": version, "manifest": json.RawMessage(manifest),
		"artifacts": map[string]any{key: map[string]any{"url": r.URL(path), "sha256": hex.EncodeToString(sum[:]), "size": len(file)}}}
}

// Registry is a registry document updated at updated with plugins, each
// {"name", "title", "summary", "releases"} (see NativeRelease, ScriptRelease).
func Registry(updated time.Time, plugins ...map[string]any) map[string]any {
	return map[string]any{"schema": 1, "updated": updated.UTC().Format(time.RFC3339), "plugins": plugins}
}

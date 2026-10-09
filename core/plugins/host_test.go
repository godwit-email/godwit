package plugins

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHost records which host methods were reached.
type fakeHost struct {
	mu       sync.Mutex
	calls    []string
	msg      MailMessage
	limit    int
	prompt   string
	kv       map[string]string
	audits   []string // AuditEgress rows, apart from calls
	auditErr error
}

func (h *fakeHost) AuditEgress(_ context.Context, inv Invocation, dest, decision, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audits = append(h.audits, strings.Join([]string{inv.ID, dest, decision, reason}, " "))
	return h.auditErr
}

func (h *fakeHost) rec(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, name)
}

func (h *fakeHost) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

func (h *fakeHost) Config(context.Context) (map[string]any, error) {
	h.rec("Config")
	return map[string]any{"plan": "free", "n": 3.0}, nil
}
func (h *fakeHost) StorageGet(_ context.Context, k string) (string, bool, error) {
	h.rec("StorageGet")
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.kv[k]
	return v, ok, nil
}
func (h *fakeHost) StorageSet(_ context.Context, k, v string) error {
	h.rec("StorageSet")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.kv == nil {
		h.kv = map[string]string{}
	}
	h.kv[k] = v
	return nil
}
func (h *fakeHost) StorageDelete(context.Context, string) error { h.rec("StorageDelete"); return nil }
func (h *fakeHost) MailGet(context.Context, int64) (MailMessage, error) {
	h.rec("MailGet")
	return h.msg, nil
}
func (h *fakeHost) MailSearch(_ context.Context, _ string, limit int) ([]MailSummary, error) {
	h.rec("MailSearch")
	h.mu.Lock()
	h.limit = limit
	h.mu.Unlock()
	return []MailSummary{{ID: 1, Subject: "s", Date: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}}, nil
}
func (h *fakeHost) MailSetFlags(context.Context, []int64, []string, []string) error {
	h.rec("MailSetFlags")
	return nil
}
func (h *fakeHost) MailMove(context.Context, []int64, int64) error { h.rec("MailMove"); return nil }
func (h *fakeHost) MailCreateDraft(context.Context, DraftInput) (int64, error) {
	h.rec("MailCreateDraft")
	return 7, nil
}
func (h *fakeHost) MailUpdateDraft(context.Context, DraftUpdate) (int64, error) {
	h.rec("MailUpdateDraft")
	return 2, nil
}
func (h *fakeHost) LLMComplete(_ context.Context, p string) (string, error) {
	h.rec("LLMComplete")
	h.mu.Lock()
	h.prompt = p
	h.mu.Unlock()
	return strings.Repeat("é", 40000), nil // 80 KB
}
func (h *fakeHost) Fetch(_ context.Context, _ Invocation, r FetchRequest) (FetchResponse, error) {
	h.rec("Fetch")
	return FetchResponse{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: "hi " + r.URL}, nil
}

// stash keeps ctx for later calls.
const stash = `var plugin = {default(ctx){ globalThis.ctx = ctx }}`

func loadTest(t *testing.T, perms Permissions, h Host, src string) (*Plugin, error) {
	t.Helper()
	return Load(context.Background(), newTestVM(t, src, nil), Manifest{Name: "test", Permissions: perms}, h)
}

func mustLoad(t *testing.T, perms Permissions, h Host, src string) *Plugin {
	t.Helper()
	p, err := loadTest(t, perms, h, src)
	require.NoError(t, err)
	return p
}

// try runs an expression in an async function under inv: its value, or the
// message of what it threw.
func try(t *testing.T, p *Plugin, inv Invocation, expr string) any {
	t.Helper()
	got, err := p.VM().Call(context.Background(), inv, func(c *CallScope) (any, error) {
		return c.Runtime().RunString(`(async () => { try { return await (` + expr + `) } catch (e) { return "threw: " + e.message } })()`)
	})
	require.NoError(t, err)
	return got
}

var (
	readOnly = Invocation{ID: "r", Kind: KindTool}
	writable = Invocation{ID: "w", Kind: KindTool, Write: true}
)

func TestCtxManifestPermissions(t *testing.T) {
	h := &fakeHost{}
	none := mustLoad(t, Permissions{}, h, stash)
	for expr, perm := range map[string]string{
		`ctx.mail.get(1)`:                          "mail:read",
		`ctx.mail.search("x")`:                     "mail:read",
		`ctx.mail.setFlags([1], ["\\Seen"], [])`:   "mail:write",
		`ctx.mail.move([1], 2)`:                    "mail:write",
		`ctx.mail.createDraft({accountId: 1})`:     "mail:write",
		`ctx.mail.updateDraft({draftId: 1})`:       "mail:write",
		`ctx.llm.complete("hi")`:                   "llm",
		`ctx.net.fetch("https://api.example.com")`: "net:api.example.com",
	} {
		got := try(t, none, Invocation{Kind: KindTool, Write: true, EgressHosts: []string{"api.example.com"}}, expr)
		assert.Contains(t, got, ": permission denied: "+perm, expr)
	}
	assert.Empty(t, h.got(), "nothing reached the host")

	// mail:read alone does not write, even when the invocation may.
	reader := mustLoad(t, Permissions{Mail: []string{"read"}}, h, stash)
	assert.Contains(t, try(t, reader, writable, `ctx.mail.move([1], 2)`), "permission denied: mail:write")
	assert.Equal(t, 1.0, try(t, reader, readOnly, `ctx.mail.search("x").then(r => r.length)`))

	llm := mustLoad(t, Permissions{LLM: true}, h, stash)
	assert.Equal(t, 32768.0, try(t, llm, readOnly, `ctx.llm.complete("hi").then(s => s.length)`)) // cut to 64 KB: 32768 runes of é
	assert.Equal(t, []string{"MailSearch", "LLMComplete"}, h.got())

	// Registering a card or an event handler reads mail: it needs mail:read.
	_, err := loadTest(t, Permissions{}, h, `var plugin = {default(ctx){ ctx.cards.register({match(){return true}, render(){return {title:"x"}}}) }}`)
	assert.ErrorContains(t, err, "permission denied: mail:read")
	_, err = loadTest(t, Permissions{}, h, `var plugin = {default(ctx){ ctx.on("message.received", () => {}) }}`)
	assert.ErrorContains(t, err, "permission denied: mail:read")
}

func TestLLMCompleteLimits(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{LLM: true}, h, stash)
	out := try(t, p, readOnly, `ctx.llm.complete("hi")`).(string)
	assert.LessOrEqual(t, len(out), 64<<10)
	assert.True(t, utf8.ValidString(out))
	assert.Contains(t, try(t, p, readOnly, `ctx.llm.complete("x".repeat(65537))`), "too large")
	assert.Equal(t, "hi", h.prompt)
}

func TestReadOnlyInvocationNeverReachesWrites(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{Mail: []string{"read", "write"}}, h, stash)
	for _, expr := range []string{
		`ctx.mail.setFlags([1], ["\\Seen"], [])`, `ctx.mail.move([1], 2)`,
		`ctx.mail.createDraft({accountId: 1, to: ["a@x"], subject: "s", html: "<p>x</p>"})`,
		`ctx.mail.updateDraft({draftId: 1, revision: 1, html: "x"})`,
	} {
		assert.Contains(t, try(t, p, readOnly, expr), "permission denied: mail:write", expr)
	}
	assert.Empty(t, h.got())
	assert.Equal(t, 7.0, try(t, p, writable, `ctx.mail.createDraft({accountId: 1, to: ["a@x"], subject: "s", html: "<p>x</p>"})`))
	assert.Equal(t, []string{"MailCreateDraft"}, h.got())
}

func TestInitCannotWriteOrFetch(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{Mail: []string{"read", "write"}, Net: []string{"api.example.com"}}, h, `var plugin = {default: async (ctx) => {
		globalThis.ctx = ctx
		globalThis.seen = []
		for (const f of [() => ctx.mail.setFlags([1], ["\\Seen"], []), () => ctx.net.fetch("https://api.example.com/x")]) {
			try { await f(); seen.push("ok") } catch (e) { seen.push(e.message) }
		}
	}}`)
	got := try(t, p, readOnly, `seen`)
	assert.Equal(t, []any{"mail.setFlags: permission denied: mail:write", "net.fetch: permission denied: net:api.example.com"}, got)
	assert.Empty(t, h.got())

	// A later call gets what its invocation allows, and only that host.
	egress := Invocation{Kind: KindHook, EgressHosts: []string{"api.example.com"}}
	assert.Equal(t, "hi https://API.example.com:443/x", try(t, p, egress, `ctx.net.fetch("https://API.example.com:443/x").then(r => r.text())`))
	assert.Equal(t, 200.0, try(t, p, egress, `ctx.net.fetch("https://api.example.com/").then(r => r.status)`))
	assert.Contains(t, try(t, p, egress, `ctx.net.fetch("https://evil.example.com/")`), "permission denied: net:evil.example.com")
	assert.Contains(t, try(t, p, egress, `ctx.net.fetch("https://api.example.com:8443/")`), "permission denied: net:api.example.com:8443")
	assert.Equal(t, []string{"Fetch", "Fetch"}, h.got())
}

func TestCtxMailTruncation(t *testing.T) {
	big := strings.Repeat("ab€", 100<<10) // 500 KB, multi-byte runes
	h := &fakeHost{msg: MailMessage{ID: 1, Subject: "s", Text: big, HTML: "<p>" + big + "</p>",
		Date: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}}
	p := mustLoad(t, Permissions{Mail: []string{"read"}}, h, stash)
	m := try(t, p, readOnly, `ctx.mail.get(1)`).(map[string]any)
	for _, k := range []string{"text", "html"} {
		s := m[k].(string)
		assert.LessOrEqual(t, len(s), 256<<10, k)
		assert.Greater(t, len(s), 255<<10, k)
		assert.True(t, utf8.ValidString(s), k)
	}
	assert.Equal(t, true, m["truncated"])
	assert.Equal(t, "2026-01-02T03:04:05Z", m["date"])

	h.msg = MailMessage{ID: 1, Text: "short"}
	m = try(t, p, readOnly, `ctx.mail.get(1)`).(map[string]any)
	assert.Equal(t, false, m["truncated"])
	assert.Equal(t, []any{}, m["to"])

	try(t, p, readOnly, `ctx.mail.search("x", 500)`)
	assert.Equal(t, 50, h.limit)
	try(t, p, readOnly, `ctx.mail.search("x")`)
	assert.Equal(t, 20, h.limit)
	assert.Equal(t, "2026-01-02T03:04:05Z", try(t, p, readOnly, `ctx.mail.search("x").then(r => r[0].date)`))
}

func TestRegisterOnlyDuringInit(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{Mail: []string{"read"}}, h, `var plugin = {default(ctx){
		globalThis.ctx = ctx
		globalThis.errs = []
		const t = (f) => { try { f() } catch (e) { errs.push(e.message) } }
		ctx.tools.register({name: "lookup", description: "Look up.", schema: {type: "object"}, write: false, run: () => 1})
		ctx.tools.register({name: "tag", description: "Tag.", schema: {type: "object"}, run: () => 1})
		t(() => ctx.tools.register({name: "lookup", description: "x", schema: {}, run: () => 1}))
		t(() => ctx.tools.register({name: "Bad-Name", description: "x", schema: {}, run: () => 1}))
		t(() => ctx.tools.register({name: "norun", description: "x", schema: {}}))
		ctx.actions.register({id: "translate", label: {default: "Translate", "zh-CN": "翻译"}, on: ["message", "selection"], run: () => "x"})
		t(() => ctx.actions.register({id: "translate", label: "x", on: ["message"], run: () => 1}))
		t(() => ctx.actions.register({id: "x", label: "x", on: ["inbox"], run: () => 1}))
		ctx.cards.register({match: () => true, render: () => ({title: "t"})})
		t(() => ctx.cards.register({match: () => true, render: () => ({title: "t"})}))
		ctx.on("message.received", () => {})
		t(() => ctx.on("message.received", () => {}))
		globalThis.later = ctx.tools.register
	}}`)
	errs := try(t, p, readOnly, `errs`).([]any)
	require.Len(t, errs, 7)
	assert.Contains(t, errs[0], "already registered")
	assert.Contains(t, errs[1], "name")
	assert.Contains(t, errs[2], "run")
	assert.Contains(t, errs[3], "already registered")
	assert.Contains(t, errs[4], "on")
	assert.Contains(t, errs[5], "already registered")
	assert.Contains(t, errs[6], "already registered")
	assert.Equal(t, []ToolInfo{
		{Name: "lookup", Description: "Look up.", Schema: map[string]any{"type": "object"}, Write: false},
		{Name: "tag", Description: "Tag.", Schema: map[string]any{"type": "object"}, Write: true},
	}, p.Tools())
	assert.Equal(t, []ActionInfo{{ID: "translate", Label: Text{Default: "Translate", ByLang: map[string]string{"zh-CN": "翻译"}}, On: []string{"message", "selection"}}}, p.Actions())
	assert.True(t, p.HasCard())
	assert.Equal(t, []string{"message.received"}, p.Events())

	for _, expr := range []string{
		`ctx.tools.register({name: "late", description: "x", schema: {}, run: () => 1})`,
		`later({name: "late", description: "x", schema: {}, run: () => 1})`,
		`ctx.on("message.beforeSend", () => {})`,
		`ctx.actions.register({id: "late", label: "x", on: ["message"], run: () => 1})`,
	} {
		assert.Contains(t, try(t, p, readOnly, expr), "only during initialization", expr)
	}
	assert.Len(t, p.Tools(), 2)
}

func TestUnknownEventRejected(t *testing.T) {
	_, err := loadTest(t, Permissions{Mail: []string{"read"}}, &fakeHost{}, `var plugin = {default(ctx){ ctx.on("message.deleted", () => {}) }}`)
	assert.ErrorContains(t, err, "unknown event")
}

func TestCtxConfigAndStorage(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{}, h, `var plugin = {default(ctx){ globalThis.ctx = ctx; globalThis.atInit = ctx.config.plan }}`)
	assert.Equal(t, "free", try(t, p, readOnly, `atInit`))
	assert.Equal(t, 3.0, try(t, p, readOnly, `ctx.config.n + 0 * ctx.config.n`))
	assert.Equal(t, []string{"Config", "Config"}, h.got(), "read once per call")
	assert.Equal(t, "v", try(t, p, readOnly, `ctx.storage.set("k", "v").then(() => ctx.storage.get("k"))`))
	assert.Equal(t, nil, try(t, p, readOnly, `ctx.storage.get("missing")`))
	assert.Contains(t, try(t, p, readOnly, `ctx.storage.set("k", 5)`), "string")
}

// TestCtxCapabilityWhitelist: ctx and the Host offer a fixed set of
// capabilities, and none of them sends mail.
func TestCtxCapabilityWhitelist(t *testing.T) {
	p := mustLoad(t, Permissions{}, &fakeHost{}, stash)
	got := try(t, p, readOnly, `(() => {
		const out = []
		const walk = (o, path) => { for (const k of Object.getOwnPropertyNames(o).sort()) {
			const d = Object.getOwnPropertyDescriptor(o, k)
			if (d.get) { out.push(path + k); continue }
			if (typeof o[k] === "object" && o[k] !== null) walk(o[k], path + k + "."); else out.push(path + k)
		} }
		walk(ctx, "")
		return out
	})()`)
	assert.Equal(t, []any{"actions.register", "cards.register", "config", "llm.complete",
		"mail.createDraft", "mail.get", "mail.move", "mail.search", "mail.setFlags", "mail.updateDraft",
		"net.fetch", "on", "storage.delete", "storage.get", "storage.set", "tools.register"}, got)

	var methods []string
	ht := reflect.TypeFor[Host]()
	for i := range ht.NumMethod() {
		methods = append(methods, ht.Method(i).Name)
	}
	assert.Equal(t, []string{"AuditEgress", "Config", "Fetch", "LLMComplete", "MailCreateDraft", "MailGet", "MailMove", "MailSearch",
		"MailSetFlags", "MailUpdateDraft", "StorageDelete", "StorageGet", "StorageSet"}, methods)
}

// The URL that was checked is the one fetched: init cannot carry another.
func TestFetchInitCannotOverrideURL(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{Net: []string{"allowed.example"}}, h, stash)
	egress := Invocation{Kind: KindHook, EgressHosts: []string{"allowed.example"}}
	got := try(t, p, egress, `ctx.net.fetch("https://allowed.example/a", {url: "https://denied.example/b", URL: "https://denied.example/c", method: "POST"}).then(r => r.text())`)
	assert.Equal(t, "hi https://allowed.example/a", got)
}

func TestRequiredArgsRejectNull(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{Mail: []string{"read"}}, h, `var plugin = {default(ctx){
		globalThis.ctx = ctx
		try { ctx.tools.register({name: "t", description: "d", schema: null, run: () => 1}); globalThis.reg = "ok" } catch (e) { globalThis.reg = e.message }
	}}`)
	assert.Contains(t, try(t, p, readOnly, `reg`), "schema")
	for _, expr := range []string{`ctx.storage.set("k", null)`, `ctx.storage.set(null, "v")`, `ctx.storage.get(null)`,
		`ctx.mail.get(null)`, `ctx.mail.search(null)`} {
		assert.Contains(t, try(t, p, readOnly, expr), "threw: ", expr)
	}
	assert.Empty(t, p.Tools())
	assert.Empty(t, h.got(), "nothing reached the host")
}

// What the plugin side refuses before the host fetches is audited too, and
// a failed audit is never hidden behind the refusal.
func TestFetchGateAudited(t *testing.T) {
	h := &fakeHost{}
	p := mustLoad(t, Permissions{Net: []string{"api.example.com"}}, h, stash)
	egress := Invocation{ID: "c1", Kind: KindHook, EgressHosts: []string{"api.example.com"}}
	assert.Contains(t, try(t, p, egress, `ctx.net.fetch("https://evil.example.com/p?q=1")`), "permission denied: net:evil.example.com")
	assert.Contains(t, try(t, p, egress, `ctx.net.fetch("http://Evil.example.com:8080/p")`), "permission denied: net:evil.example.com:8080")
	assert.Contains(t, try(t, p, readOnly, `ctx.net.fetch("https://api.example.com/p")`), "permission denied: net:api.example.com")
	assert.Contains(t, try(t, p, egress, `ctx.net.fetch("https://api.example.com/p", {method: "POST", body: "x".repeat(2 << 20)})`), "too large")
	assert.Equal(t, []string{"c1 evil.example.com:443 deny host_not_allowed", "c1 evil.example.com:8080 deny host_not_allowed",
		"r api.example.com:443 deny host_not_allowed", "c1 api.example.com:443 deny too_large"}, h.audits)
	assert.Empty(t, h.got(), "nothing was fetched")

	h.auditErr = errors.New("audit disk full")
	got := try(t, p, egress, `ctx.net.fetch("https://evil.example.com/")`)
	assert.Contains(t, got, "permission denied: net:evil.example.com")
	assert.Contains(t, got, "audit disk full")
}

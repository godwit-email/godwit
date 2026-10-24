package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Calcium-Ion/moejs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var translateManifest = Manifest{Name: "test", Capabilities: []string{"translate"}, Permissions: Permissions{Net: []string{"api.example.com"}}}

func newMoeTestVM(t *testing.T, src string, opts VMOptions) *MoeVM {
	t.Helper()
	mod, err := moejs.Compile("plugin.js", src)
	require.NoError(t, err)
	if opts.Clock == nil {
		opts.Clock = &fakeClock{}
	}
	v, err := NewMoeVM("test", mod, opts)
	require.NoError(t, err)
	t.Cleanup(v.Close)
	return v
}

func loadMoeTest(t *testing.T, m Manifest, h Host, src string, opts VMOptions) (*Plugin, error) {
	t.Helper()
	return LoadMoe(context.Background(), newMoeTestVM(t, src, opts), m, h, testConfig)
}

func mustLoadMoe(t *testing.T, h Host, src string, opts VMOptions) *Plugin {
	t.Helper()
	p, err := loadMoeTest(t, translateManifest, h, src, opts)
	require.NoError(t, err)
	return p
}

var translateInv = Invocation{ID: "tr", Kind: KindTranslate, EgressHosts: []string{"api.example.com"}, Config: testConfig}

func translateReq() map[string]any {
	return map[string]any{"texts": []string{"hi", "you"}, "target": "zh-CN"}
}

// The globals are the ECMAScript builtins moejs ships, and nothing more: no
// timers, fetch, console, or anything of core/ai/pi's bundle.
func TestMoeNoUndeclaredGlobals(t *testing.T) {
	p := mustLoadMoe(t, &fakeHost{}, `let seen
	export default function (ctx) {
		seen = { globals: Object.getOwnPropertyNames(globalThis).sort(), ctx: Object.keys(ctx).sort() }
		ctx.translators.register({ translate: () => seen })
	}`, VMOptions{})
	got, err := p.Translate(context.Background(), translateInv, translateReq())
	require.NoError(t, err)
	builtins := strings.Fields(`AggregateError Array ArrayBuffer Atomics BigInt BigInt64Array BigUint64Array Boolean DataView Date
		Error EvalError Float16Array Float32Array Float64Array Function Infinity Int16Array Int32Array Int8Array JSON Map Math NaN
		Number Object Promise Proxy RangeError ReferenceError Reflect RegExp Set SharedArrayBuffer String Symbol SyntaxError
		TextDecoder TextEncoder TypeError URIError Uint16Array Uint32Array Uint8Array Uint8ClampedArray WeakMap WeakRef WeakSet
		atob btoa decodeURI decodeURIComponent encodeURI encodeURIComponent escape eval globalThis isFinite isNaN parseFloat
		parseInt queueMicrotask structuredClone undefined unescape`)
	var globals []string
	for _, g := range got.(map[string]any)["globals"].([]any) {
		globals = append(globals, g.(string))
	}
	assert.ElementsMatch(t, builtins, globals)
	assert.Equal(t, []any{"actions", "cards", "config", "llm", "mail", "models", "net", "on", "storage", "tools", "translators"}, got.(map[string]any)["ctx"])
}

// One plugin source, on goja and on moejs, translates the same.
func TestMoeTranslateMatchesGoja(t *testing.T) {
	const body = `(ctx) => {
		ctx.translators.register({ async translate(req) {
			const r = await ctx.net.fetch("https://api.example.com/t", { method: "POST", body: JSON.stringify(req) })
			await ctx.storage.set("last", req.target)
			const texts = req.texts.map((t) => t.toUpperCase())
			return { texts, source: "en", status: r.status, body: await r.text(), cfg: ctx.config, last: await ctx.storage.get("last") }
		} })
	}`
	h := &fakeHost{}
	gojaPlugin, err := Load(context.Background(), newTestVM(t, `var plugin = {default: `+body+`}`, nil), translateManifest, h, testConfig)
	require.NoError(t, err)
	want, err := gojaPlugin.Translate(context.Background(), translateInv, translateReq())
	require.NoError(t, err)

	moe := &fakeHost{}
	got, err := mustLoadMoe(t, moe, `export default `+body, VMOptions{}).Translate(context.Background(), translateInv, translateReq())
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, "HI", got.(map[string]any)["texts"].([]any)[0])
	assert.Equal(t, h.got(), moe.got())
}

func TestMoeEndlessLoopPreempted(t *testing.T) {
	clock := &fakeClock{}
	p := mustLoadMoe(t, &fakeHost{}, `export default function (ctx) {
		ctx.translators.register({ translate: (req) => { if (req.spin) for (;;) {} return { texts: req.texts, source: "en" } } })
	}`, VMOptions{Clock: clock})
	errc := make(chan error, 1)
	go func() {
		_, err := p.Translate(context.Background(), translateInv, map[string]any{"texts": []string{"x"}, "spin": true})
		errc <- err
	}()
	require.Eventually(t, func() bool { return clock.pending() > 0 }, 5*time.Second, time.Millisecond)
	clock.Advance(29 * time.Second)
	select {
	case err := <-errc:
		t.Fatalf("returned before the 30 s limit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	clock.Advance(time.Second)
	assert.ErrorIs(t, <-errc, ErrTimeout)

	got, err := p.Translate(context.Background(), translateInv, translateReq())
	require.NoError(t, err, "the runtime is reusable")
	assert.Equal(t, []any{"hi", "you"}, got.(map[string]any)["texts"])

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = p.Translate(ctx, translateInv, map[string]any{"texts": []string{"x"}, "spin": true})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMoeMemoryLimit(t *testing.T) {
	p := mustLoadMoe(t, &fakeHost{}, `export default function (ctx) {
		ctx.translators.register({ translate: (req) => {
			if (req.eat) { const a = []; for (;;) a.push({ s: "x".repeat(1024) + a.length }) }
			return { texts: req.texts, source: "en" }
		} })
	}`, VMOptions{MemoryLimit: 8 << 20})
	_, err := p.Translate(context.Background(), translateInv, map[string]any{"texts": []string{"x"}, "eat": true})
	assert.ErrorIs(t, err, ErrMemoryLimit)
	for range 3 { // a new budget for each call: the host and the runtime go on
		got, err := p.Translate(context.Background(), translateInv, translateReq())
		require.NoError(t, err)
		assert.Equal(t, "en", got.(map[string]any)["source"])
	}
}

func TestMoeLimitsAndRedaction(t *testing.T) {
	p := mustLoadMoe(t, &fakeHost{}, `export default function (ctx) {
		ctx.translators.register({ translate: async (req) => {
			if (req.big) return { texts: ["x".repeat(2 << 20)] }
			if (req.leak) throw new Error("key sk-secret at /Users/me/x")
			if (req.store) return ctx.storage.set("k", "v".repeat(70000))
			if (req.bad) return ctx.storage.get(null)
			if (req.never) return new Promise(() => {})
			return { texts: req.texts }
		} })
	}`, VMOptions{Redact: func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[redacted]") }})
	call := func(req map[string]any) error {
		_, err := p.Translate(context.Background(), translateInv, req)
		return err
	}
	assert.ErrorIs(t, call(map[string]any{"big": true}), ErrTooLarge)
	err := call(map[string]any{"leak": true})
	assert.EqualError(t, err, "plugin test: Error: key [redacted] at <path>")
	assert.ErrorContains(t, call(map[string]any{"store": true}), "storage.set: plugin: data too large")
	assert.ErrorContains(t, call(map[string]any{"bad": true}), "TypeError: storage.get: key must be a string")
	assert.ErrorContains(t, call(map[string]any{"never": true}), "never settled")
}

func TestMoeFetchGateAudited(t *testing.T) {
	h := &fakeHost{}
	p := mustLoadMoe(t, h, `let ctx
	export default function (c) { ctx = c; ctx.translators.register({ translate: async (req) => {
		try { await ctx.net.fetch(req.url, req.init) } catch (e) { return e.message }
	} }) }`, VMOptions{})
	try := func(inv Invocation, url string, init any) string {
		got, err := p.Translate(context.Background(), inv, map[string]any{"url": url, "init": init})
		require.NoError(t, err)
		s, _ := got.(string)
		return s
	}
	noHosts := Invocation{ID: "r", Kind: KindTranslate, Config: testConfig}
	assert.Contains(t, try(translateInv, "https://evil.example.com/p?q=1", nil), "net.fetch: permission denied: net:evil.example.com")
	assert.Contains(t, try(noHosts, "https://api.example.com/p", nil), "permission denied: net:api.example.com")
	assert.Contains(t, try(translateInv, "https://api.example.com/p", map[string]any{"method": "POST", "body": strings.Repeat("x", 2<<20)}), "too large")
	assert.Equal(t, []string{"tr evil.example.com:443 deny host_not_allowed", "r api.example.com:443 deny host_not_allowed", "tr api.example.com:443 deny too_large"}, h.audits)
	assert.Empty(t, h.got(), "nothing was fetched")
}

// A settlement for a call that already ended runs none of that call's JS.
func TestMoeLateSettlementDropped(t *testing.T) {
	h := &blockingFetch{release: make(chan struct{})}
	p := mustLoadMoe(t, h, `let ran = 0
	export default function (ctx) { ctx.translators.register({ translate: async (req) => {
		if (req.count) return ran
		await ctx.net.fetch("https://api.example.com/")
		ran++
	} }) }`, VMOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.Translate(ctx, translateInv, map[string]any{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	close(h.release)
	got, err := p.Translate(context.Background(), translateInv, map[string]any{"count": true})
	require.NoError(t, err)
	assert.Equal(t, float64(0), got)
	assert.Equal(t, int64(1), h.done.Load(), "the late fetch did finish")
}

type blockingFetch struct {
	fakeHost
	release chan struct{}
	done    atomic.Int64
}

func (h *blockingFetch) Fetch(context.Context, Invocation, FetchRequest) (FetchResponse, error) {
	<-h.release
	h.done.Add(1)
	return FetchResponse{Status: 200}, nil
}

// A ctx API still on goja sends the plugin back to goja when initialization
// reaches for it, caught or not; after initialization it fails as on goja.
func TestMoeNeedsGoja(t *testing.T) {
	for _, src := range []string{
		`export default function (ctx) { ctx.tools.register({ name: "t", description: "d", schema: {}, run: () => 1 }) }`,
		`export default function (ctx) { try { ctx.actions.register({}) } catch {} ctx.translators.register({ translate: (r) => r }) }`,
		`export default function (ctx) { ctx.on("message.received", () => {}) }`,
	} {
		_, err := loadMoeTest(t, translateManifest, &fakeHost{}, src, VMOptions{})
		assert.ErrorIs(t, err, ErrNeedsGoja, src)
	}
	p := mustLoadMoe(t, &fakeHost{}, `let ctx
	export default function (c) { ctx = c; ctx.translators.register({ translate: async (req) => {
		const out = []
		for (const f of [() => ctx.mail.get(1), () => ctx.mail.move([1], 2), () => ctx.llm.complete("x"), () => ctx.tools.register({})]) {
			try { await f() } catch (e) { out.push(e.message) }
		}
		return out
	} }) }`, VMOptions{})
	got, err := p.Translate(context.Background(), translateInv, translateReq())
	require.NoError(t, err)
	assert.Equal(t, []any{"mail.get: permission denied: mail:read", "mail.move: permission denied: mail:write",
		"llm.complete: permission denied: llm", "tools.register: only during initialization"}, got)
}

func TestMoeRegistration(t *testing.T) {
	_, err := loadMoeTest(t, translateManifest, &fakeHost{}, `export default function () {}`, VMOptions{})
	assert.ErrorContains(t, err, "registered no translator")
	_, err = loadMoeTest(t, Manifest{Name: "test"}, &fakeHost{}, `export default function (ctx) { ctx.translators.register({ translate: (r) => r }) }`, VMOptions{})
	assert.ErrorContains(t, err, "translate capability")
	_, err = loadMoeTest(t, translateManifest, &fakeHost{}, `export const x = 1`, VMOptions{})
	assert.ErrorContains(t, err, "default export")
	_, err = loadMoeTest(t, translateManifest, &fakeHost{}, `export default function (ctx) {
		const t = { translate: (r) => r }; ctx.translators.register(t); ctx.translators.register(t) }`, VMOptions{})
	assert.ErrorContains(t, err, "already")
}

func TestMoeClose(t *testing.T) {
	p := mustLoadMoe(t, &fakeHost{}, `export default function (ctx) { ctx.translators.register({ translate: (r) => { for (;;) {} } }) }`, VMOptions{})
	errc := make(chan error, 1)
	go func() { _, err := p.Translate(context.Background(), translateInv, translateReq()); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	p.Close()
	assert.ErrorIs(t, <-errc, ErrClosed)
	_, err := p.Translate(context.Background(), translateInv, translateReq())
	assert.ErrorIs(t, err, ErrClosed)
	p.Close() // twice is fine
}

func TestRunsOnMoejs(t *testing.T) {
	assert.True(t, RunsOnMoejs(translateManifest))
	for _, m := range []Manifest{
		{Name: "x"},
		{Name: "x", Capabilities: []string{"translate", "models"}},
		{Name: "x", Capabilities: []string{"translate"}, Permissions: Permissions{Mail: []string{"read"}}},
		{Name: "x", Capabilities: []string{"translate"}, Permissions: Permissions{LLM: true}},
		{Name: "x", Capabilities: []string{"translate"}, Native: &NativeSpec{ABI: 1}},
	} {
		assert.False(t, RunsOnMoejs(m), "%+v", m)
	}
}

// The moejs build is one ES module, without goja's down-levelling.
func TestESBuildModule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(`import { up } from "./up"
export default function (ctx: any) {
	ctx.translators.register({ async translate(req: { texts: string[] }) {
		const texts: string[] = []
		for await (const t of up(req.texts)) texts.push(t)
		return { texts, source: "en" }
	} })
}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "up.ts"), []byte(`export async function* up(xs: string[]) { for (const x of xs) yield x.toUpperCase() }`), 0o600))
	code, err := ESBuildModule.Compile(dir, "index.ts")
	require.NoError(t, err)
	assert.Contains(t, string(code), "for await")
	assert.Contains(t, string(code), "async function*")
	assert.NotEqual(t, ESBuild.Key(), ESBuildModule.Key())

	mod, err := moejs.Compile("plugin.js", string(code))
	require.NoError(t, err)
	v, err := NewMoeVM("test", mod, VMOptions{})
	require.NoError(t, err)
	t.Cleanup(v.Close)
	p, err := LoadMoe(context.Background(), v, translateManifest, &fakeHost{}, testConfig)
	require.NoError(t, err)
	got, err := p.Translate(context.Background(), translateInv, translateReq())
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"texts": []any{"HI", "YOU"}, "source": "en"}, got)
}

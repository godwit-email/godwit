package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock fires AfterFunc callbacks only when Advance moves past them.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Duration
	f       func()
	stopped bool
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now + d, f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// pending counts live timers.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now + d
	for {
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && t.at <= end && (next == nil || t.at < next.at) {
				next = t
			}
		}
		if next == nil {
			break
		}
		next.stopped = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
		c.mu.Lock()
	}
	c.now = end
	c.mu.Unlock()
}

func compile(t *testing.T, src string) *goja.Program {
	t.Helper()
	p, err := goja.Compile("plugin.js", src, false)
	require.NoError(t, err)
	return p
}

// quietWatchdog never trips, so tests about other behaviour are not affected by
// the real heap.
func quietWatchdog() *Watchdog {
	return &Watchdog{Meter: func() uint64 { return 0 }, Clock: &fakeClock{}}
}

func newTestVM(t *testing.T, src string, clock Clock) *VM {
	t.Helper()
	if clock == nil {
		clock = &fakeClock{}
	}
	v := NewVM("test", compile(t, src), VMOptions{Clock: clock, Watchdog: quietWatchdog()})
	t.Cleanup(v.Close)
	return v
}

var inv = Invocation{Kind: KindTool}

// run evaluates JS source inside a call and returns the exported result.
func run(v *VM, ctx context.Context, src string) (any, error) {
	return v.Call(ctx, inv, func(c *CallScope) (any, error) {
		return c.Runtime().RunString(src)
	})
}

func TestVMNoGlobals(t *testing.T) {
	v := newTestVM(t, "", nil)
	got, err := run(v, context.Background(), `({
		console: typeof console, setTimeout: typeof setTimeout, require: typeof require,
		fetch: typeof fetch, process: typeof process, print: typeof print, plugin: typeof plugin })`)
	require.NoError(t, err)
	for k, typ := range got.(map[string]any) {
		assert.Equal(t, "undefined", typ, k)
	}
}

func TestInitTopLevelLoopInterrupted(t *testing.T) {
	clock := &fakeClock{}
	v := newTestVM(t, `var plugin = {default(){ return 1 }}; while (true) {}`, clock)
	errc := make(chan error, 1)
	go func() { errc <- v.Init(context.Background(), Invocation{Kind: KindInit}, nil) }()
	require.Eventually(t, func() bool { return clock.pending() > 0 }, 5*time.Second, time.Millisecond)
	clock.Advance(29 * time.Second)
	select {
	case err := <-errc:
		t.Fatalf("returned before the 30 s limit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	clock.Advance(time.Second)
	assert.ErrorIs(t, <-errc, ErrTimeout)
}

func TestInitRunsDefault(t *testing.T) {
	v := newTestVM(t, `var plugin = {default: async (ctx) => { globalThis.seen = ctx.name }}`, nil)
	err := v.Init(context.Background(), Invocation{Kind: KindInit}, func(c *CallScope) goja.Value {
		return c.Runtime().ToValue(map[string]any{"name": "ok"})
	})
	require.NoError(t, err)
	got, err := run(v, context.Background(), `seen`)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)

	v = newTestVM(t, `var plugin = {}`, nil)
	assert.ErrorContains(t, v.Init(context.Background(), Invocation{Kind: KindInit}, nil), "default")
}

func TestCallInterruptLoopVMReusable(t *testing.T) {
	v := newTestVM(t, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := run(v, ctx, `while (true) {}`)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	got, err := run(v, context.Background(), `"second"`)
	require.NoError(t, err)
	assert.Equal(t, "second", got)
}

// asyncFn installs a host function `wait(x)` whose result comes from another
// goroutine once release is closed. If entered is not nil, each wait sends on
// it once its host goroutine is running.
func asyncFn(c *CallScope, release <-chan struct{}, ran *atomic.Int64, entered chan<- struct{}) {
	c.Runtime().Set("wait", c.VM().HostFunc("wait", func(c *CallScope, call goja.FunctionCall) goja.Value {
		x := call.Argument(0).String()
		return c.Async(func(ctx context.Context) (any, error) {
			if entered != nil {
				entered <- struct{}{}
			}
			<-release
			if ran != nil {
				ran.Add(1)
			}
			if x == "fail" {
				return nil, errors.New("nope")
			}
			return "got:" + x, nil
		})
	}))
}

func TestAsyncHostFunc(t *testing.T) {
	v := newTestVM(t, "", nil)
	release := make(chan struct{})
	go func() { time.Sleep(10 * time.Millisecond); close(release) }()
	got, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
		asyncFn(c, release, nil, nil)
		return c.Runtime().RunString(`(async () => {
			const [a, b] = await Promise.all([wait("a"), wait("b")])
			let e
			try { await wait("fail") } catch (x) { e = x.message }
			return [a, b, e]
		})()`)
	})
	require.NoError(t, err)
	assert.Equal(t, []any{"got:a", "got:b", "nope"}, got)

	_, err = run(v, context.Background(), `Promise.reject(new Error("bad"))`)
	assert.ErrorContains(t, err, "bad")
}

func TestLateCallbackDropped(t *testing.T) {
	clock := &fakeClock{}
	v := newTestVM(t, "", clock)
	late := make(chan struct{})
	lateEntered := make(chan struct{}, 1)
	var ran atomic.Int64
	errc := make(chan error, 1)
	go func() {
		_, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
			asyncFn(c, late, &ran, lateEntered)
			return c.Runtime().RunString(`globalThis.n = 0; wait("x").then(() => { globalThis.n++ })`)
		})
		errc <- err
	}()
	// Time out only once the call's async work is in flight: wait("x") ran
	// (so n = 0 ran before it), and its host goroutine is blocked on late.
	<-lateEntered
	clock.Advance(30 * time.Second)
	require.ErrorIs(t, <-errc, ErrTimeout)

	// The late completion is released only while the second call is running,
	// so the only place it can be received is the second call's pump.
	second := make(chan struct{})
	secondEntered := make(chan struct{}, 1)
	type res struct {
		v   any
		err error
	}
	resc := make(chan res, 1)
	go func() {
		got, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
			asyncFn(c, second, nil, secondEntered)
			return c.Runtime().RunString(`(async () => { const r = await wait("y"); return [r, globalThis.n] })()`)
		})
		resc <- res{got, err}
	}()
	<-secondEntered
	close(late)
	require.Eventually(t, func() bool { return v.dropped.Load() == 1 }, 5*time.Second, time.Millisecond)
	close(second)
	r := <-resc
	require.NoError(t, r.err)
	assert.Equal(t, []any{"got:y", 0.0}, r.v)
	assert.EqualValues(t, 1, ran.Load(), "the late host work itself ran")

	got, err := run(v, context.Background(), `globalThis.n`)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got, "the late resolution never ran in JS")
}

func TestCallsSerialized(t *testing.T) {
	v := newTestVM(t, "", nil)
	var mu sync.Mutex
	var log []string
	note := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
	release := make(chan struct{})
	close(release)

	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
				note(name + ":start")
				asyncFn(c, release, nil, nil)
				c.Runtime().Set("note", func(s string) { note(name + ":" + s) })
				return c.Runtime().RunString(`(async () => { for (let i = 0; i < 20; i++) await wait("x"); note("end") })()`)
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Len(t, log, 4)
	first := strings.Split(log[0], ":")[0]
	assert.Equal(t, first+":end", log[1])
	assert.NotEqual(t, first, strings.Split(log[2], ":")[0])
	assert.True(t, strings.HasSuffix(log[2], ":start") && strings.HasSuffix(log[3], ":end"))
}

func TestCloseDuringCall(t *testing.T) {
	v := newTestVM(t, "", nil)
	started := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		_, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
			close(started)
			return c.Runtime().RunString(`while (true) {}`)
		})
		errc <- err
	}()
	<-started
	v.Close()
	select {
	case err := <-errc:
		assert.ErrorIs(t, err, ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end")
	}
	_, err := run(v, context.Background(), `1`)
	assert.ErrorIs(t, err, ErrClosed)
	v.Close()
}

func TestHostPanicSanitized(t *testing.T) {
	v := NewVM("test", compile(t, ""), VMOptions{
		Clock: &fakeClock{}, Watchdog: quietWatchdog(),
		Redact: func(s string) string { return strings.ReplaceAll(s, "s3cret-value", "[redacted]") },
	})
	defer v.Close()
	got, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
		rt := c.Runtime()
		rt.Set("crash", v.HostFunc("crash", func(c *CallScope, call goja.FunctionCall) goja.Value {
			var s []int
			return rt.ToValue(s[3])
		}))
		rt.Set("crashPath", v.HostFunc("crashPath", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic("open /Users/someone/godwit/core/plugins/vm.go: oops")
		}))
		rt.Set("fail", v.HostFunc("fail", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(&PermissionError{Perm: "token s3cret-value"})
		}))
		rt.Set("throwJS", v.HostFunc("throwJS", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(rt.NewTypeError("bad /etc/passwd"))
		}))
		rt.Set("readFile", v.HostFunc("readFile", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(&os.PathError{Op: "open", Path: "/Users/someone/data/godwit.db", Err: os.ErrNotExist})
		}))
		rt.Set("winPath", v.HostFunc("winPath", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(errors.New(`open C:\Users\someone\godwit.db: denied`))
		}))
		rt.Set("colonPath", v.HostFunc("colonPath", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(errors.New("open:/Users/alice/x.db failed"))
		}))
		rt.Set("winColon", v.HostFunc("winColon", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(errors.New(`path:C:\Users\alice\x.db`))
		}))
		rt.Set("urlErr", v.HostFunc("urlErr", func(c *CallScope, call goja.FunctionCall) goja.Value {
			panic(errors.New("fetch https://example.com/a/b: status 500"))
		}))
		rt.Set("laterColon", v.HostFunc("laterColon", func(c *CallScope, call goja.FunctionCall) goja.Value {
			return c.Async(func(ctx context.Context) (any, error) {
				return nil, errors.New("stat:/Users/alice/y|open=C:/Users/alice/z")
			})
		}))
		rt.Set("later", v.HostFunc("later", func(c *CallScope, call goja.FunctionCall) goja.Value {
			return c.Async(func(ctx context.Context) (any, error) {
				return nil, fmt.Errorf("read /var/lib/godwit/x: key s3cret-value: %w", os.ErrPermission)
			})
		}))
		return rt.RunString(`(async () => {
			const out = [crash, crashPath, fail, throwJS, readFile, winPath, colonPath, winColon, urlErr].map(f => {
				try { f(); return ["none"] } catch (e) { return [String(e.message), typeof e.value, e instanceof Error, e instanceof TypeError] }
			})
			for (const f of [later, laterColon]) {
				try { await f() } catch (e) { out.push([String(e.message), typeof e.value, e instanceof Error, false]) }
			}
			return out
		})()`)
	})
	require.NoError(t, err)
	var msgs []any
	for _, r := range got.([]any) {
		r := r.([]any)
		msgs = append(msgs, r[0])
		assert.Equal(t, "undefined", r[1], "e.value exposes the Go error: %v", r[0])
		assert.Equal(t, true, r[2], "not a JS Error: %v", r[0])
	}
	for _, m := range msgs[:2] {
		assert.Contains(t, m, "internal error")
		assert.NotContains(t, m, "/Users")
		assert.NotContains(t, m, ".go")
		assert.NotContains(t, m, "goroutine")
		assert.NotContains(t, m, "index out of range")
	}
	assert.Equal(t, "fail: permission denied: token [redacted]", msgs[2])
	assert.Equal(t, "bad <path>", msgs[3])
	assert.Equal(t, true, got.([]any)[3].([]any)[3], "a host TypeError stays a TypeError")
	assert.Equal(t, "readFile: open <path>: file does not exist", msgs[4])
	assert.Equal(t, "winPath: open <path>: denied", msgs[5])
	assert.Equal(t, "colonPath: open:<path> failed", msgs[6])
	assert.Equal(t, "winColon: path:<path>", msgs[7])
	assert.Equal(t, "urlErr: fetch https://example.com/a/b: status 500", msgs[8], "URLs are not paths")
	assert.Equal(t, "read <path>: key [redacted]: permission denied", msgs[9])
	assert.Equal(t, "stat:<path>|open=<path>", msgs[10])

	// A panic in the call body itself does not kill the owner goroutine.
	_, err = v.Call(context.Background(), inv, func(c *CallScope) (any, error) { panic("body /Users/x.go") })
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "/Users")
	got, err = run(v, context.Background(), `1+1`)
	require.NoError(t, err)
	assert.EqualValues(t, 2, got)
}

func TestRecursionLimited(t *testing.T) {
	v := newTestVM(t, "", nil)
	_, err := run(v, context.Background(), `function f(n) { return f(n + 1) + 1 } f(0)`)
	require.Error(t, err)
	got, err := run(v, context.Background(), `"alive"`)
	require.NoError(t, err)
	assert.Equal(t, "alive", got)
}

func TestWatchdogPolicy(t *testing.T) {
	clock := &fakeClock{}
	var heap atomic.Uint64
	heap.Store(1000)
	w := &Watchdog{Meter: heap.Load, Clock: clock, Interval: 10 * time.Millisecond, Threshold: 500}
	v := NewVM("test", compile(t, ""), VMOptions{Clock: &fakeClock{}, Watchdog: w})
	defer v.Close()

	// Over the threshold during the call: interrupted.
	errc := make(chan error, 1)
	go func() { _, err := run(v, context.Background(), `while (true) {}`); errc <- err }()
	require.Eventually(t, func() bool { return clock.pending() > 0 }, 5*time.Second, time.Millisecond)
	clock.Advance(10 * time.Millisecond) // within the limit
	heap.Store(1400)
	clock.Advance(10 * time.Millisecond)
	select {
	case err := <-errc:
		t.Fatalf("interrupted under the threshold: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	heap.Store(1501)
	clock.Advance(10 * time.Millisecond)
	require.ErrorIs(t, <-errc, ErrMemoryLimit)
	assert.Zero(t, clock.pending(), "the call's watch was released")

	// A watch for another call, or one already released, never touches the
	// running call.
	heap.Store(0)
	stale := w.Watch(v, 999_999)
	released := w.Watch(v, 999_998)
	released()
	heap.Store(10_000)
	gate := make(chan struct{})
	resc := make(chan error, 1)
	go func() {
		_, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
			asyncFn(c, gate, nil, nil)
			return c.Runtime().RunString(`wait("x")`)
		})
		resc <- err
	}()
	require.Eventually(t, func() bool { return clock.pending() > 1 }, 5*time.Second, time.Millisecond)
	clock.Advance(50 * time.Millisecond)
	close(gate)
	require.NoError(t, <-resc)
	stale()
}

func TestWatchdogSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates real memory")
	}
	v := NewVM("test", compile(t, ""), VMOptions{Watchdog: &Watchdog{Threshold: 128 << 20}})
	defer v.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := run(v, ctx, `const a = []; while (true) { a.push(new Array(1e5).fill(1)) }`)
	assert.ErrorIs(t, err, ErrMemoryLimit)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestExportStaysInOwner(t *testing.T) {
	v := newTestVM(t, "", nil)
	got, err := run(v, context.Background(), `({a: 1, b: [1, "x", null], c: {d: true}, f() {}, u: undefined})`)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": 1.0, "b": []any{1.0, "x", nil}, "c": map[string]any{"d": true}}, got)

	got, err = run(v, context.Background(), `undefined`)
	require.NoError(t, err)
	assert.Nil(t, got)

	_, err = run(v, context.Background(), `"x".repeat(1 << 20)`)
	assert.ErrorIs(t, err, ErrTooLarge)

	// Go results are normalized and capped the same way.
	goCall := func(r any) (any, error) {
		return v.Call(context.Background(), inv, func(c *CallScope) (any, error) { return r, nil })
	}
	got, err = goCall(MailSummary{ID: 7, Subject: "hi"})
	require.NoError(t, err)
	assert.Equal(t, 7.0, got.(map[string]any)["id"])
	assert.Equal(t, "hi", got.(map[string]any)["subject"])

	_, err = goCall(strings.Repeat("x", 1<<20))
	assert.ErrorIs(t, err, ErrTooLarge)

	_, err = goCall(func() {})
	assert.Error(t, err)

	_, err = v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
		return map[string]any{"v": c.Runtime().ToValue(1)}, nil
	})
	assert.ErrorContains(t, err, "goja")

	// Past the depth limit is an error, not a pass.
	deep := map[string]any{"v": goja.Undefined()}
	for range 200 {
		deep = map[string]any{"n": deep}
	}
	_, err = goCall(deep)
	assert.Error(t, err)
	plainDeep := map[string]any{}
	for range 200 {
		plainDeep = map[string]any{"n": plainDeep}
	}
	_, err = goCall(plainDeep)
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestGoResultCycleRejectedPromptly(t *testing.T) {
	v := NewVM("test", compile(t, ""), VMOptions{Clock: &fakeClock{}, Watchdog: quietWatchdog()})
	m := map[string]any{}
	m["a"], m["b"] = m, m
	type res struct{ err error }
	resc := make(chan res, 1)
	go func() {
		_, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) { return m, nil })
		resc <- res{err}
	}()
	select {
	case r := <-resc:
		assert.ErrorContains(t, r.err, "cycle")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("owner goroutine stalled on a cyclic result") // v is left running on purpose
	}
	v.Close()

	// Shared but acyclic references are fine; an exponential fan-out is cut off
	// by the size budget instead of stalling.
	v = newTestVM(t, "", nil)
	leaf := map[string]any{"x": 1}
	got, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
		return map[string]any{"a": leaf, "b": leaf}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": map[string]any{"x": 1.0}, "b": map[string]any{"x": 1.0}}, got)
	fan := map[string]any{}
	for range 60 {
		fan = map[string]any{"a": fan, "b": fan}
	}
	start := time.Now()
	_, err = v.Call(context.Background(), inv, func(c *CallScope) (any, error) { return fan, nil })
	assert.ErrorIs(t, err, ErrTooLarge)
	// The linear walk to the 1 MiB budget takes ~50ms (~0.5s under -race, which
	// is ~10x slower and slower still on CI); 2^60 visits would never finish.
	limit := 2 * time.Second
	if raceEnabled {
		limit = 20 * time.Second
	}
	assert.Less(t, time.Since(start), limit)
}

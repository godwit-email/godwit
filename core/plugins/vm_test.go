package plugins

import (
	"context"
	"errors"
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
// goroutine once release is closed.
func asyncFn(c *CallScope, release <-chan struct{}, ran *atomic.Int64) {
	c.Runtime().Set("wait", c.VM().HostFunc("wait", func(c *CallScope, call goja.FunctionCall) goja.Value {
		x := call.Argument(0).String()
		return c.Async(func(ctx context.Context) (any, error) {
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
		asyncFn(c, release, nil)
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
	var ran atomic.Int64
	errc := make(chan error, 1)
	go func() {
		_, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
			asyncFn(c, late, &ran)
			return c.Runtime().RunString(`globalThis.n = 0; wait("x").then(() => { globalThis.n++ })`)
		})
		errc <- err
	}()
	require.Eventually(t, func() bool { return clock.pending() > 0 }, 5*time.Second, time.Millisecond)
	clock.Advance(30 * time.Second)
	require.ErrorIs(t, <-errc, ErrTimeout)

	// The second call is pumping its own completions when the first call's
	// completion arrives.
	second := make(chan struct{})
	type res struct {
		v   any
		err error
	}
	resc := make(chan res, 1)
	go func() {
		got, err := v.Call(context.Background(), inv, func(c *CallScope) (any, error) {
			asyncFn(c, second, nil)
			return c.Runtime().RunString(`(async () => { const r = await wait("y"); return [r, globalThis.n] })()`)
		})
		resc <- res{got, err}
	}()
	require.Eventually(t, func() bool { return clock.pending() > 0 }, 5*time.Second, time.Millisecond)
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
				asyncFn(c, release, nil)
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
			panic(rt.NewTypeError("plain type error"))
		}))
		return rt.RunString(`[crash, crashPath, fail, throwJS].map(f => { try { f(); return "none" } catch (e) { return String(e.message) } })`)
	})
	require.NoError(t, err)
	msgs := got.([]any)
	for _, m := range msgs[:2] {
		assert.Contains(t, m, "internal error")
		assert.NotContains(t, m, "/Users")
		assert.NotContains(t, m, ".go")
		assert.NotContains(t, m, "goroutine")
		assert.NotContains(t, m, "index out of range")
	}
	assert.Equal(t, "fail: permission denied: token [redacted]", msgs[2])
	assert.Equal(t, "plain type error", msgs[3])

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
			asyncFn(c, gate, nil)
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
}

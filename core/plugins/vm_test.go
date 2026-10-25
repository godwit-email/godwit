package plugins

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Calcium-Ion/moejs"
	"github.com/Calcium-Ion/moejs/engine"
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

// newTestVM compiles src as the plugin's module. An older test source that
// defines `var plugin = {default ...}` gets that default exported.
func newTestVM(t *testing.T, src string, opts VMOptions) *VM {
	t.Helper()
	if strings.Contains(src, "var plugin =") {
		src += "\nexport default plugin.default"
	}
	mod, err := moejs.Compile("plugin.js", src)
	require.NoError(t, err)
	if opts.Clock == nil {
		opts.Clock = &fakeClock{}
	}
	v, err := NewVM("test", mod, opts)
	require.NoError(t, err)
	t.Cleanup(v.Close)
	return v
}

var inv = Invocation{Kind: KindTool}

// script runs JS source in the global scope of a call.
func script(c *CallScope, src string) (moejs.Value, error) {
	s, err := moejs.CompileScript("run.js", src)
	if err != nil {
		return moejs.Undefined(), err
	}
	return c.Runtime().RunScript(s)
}

// run evaluates JS source inside a call and returns the exported result.
func run(v *VM, ctx context.Context, src string) (any, error) {
	return v.Call(ctx, inv, func(c *CallScope) (moejs.Value, error) { return script(c, src) })
}

// setGlobal installs a global for the scripts of a test.
func setGlobal(c *CallScope, name string, val moejs.Value) {
	r := c.Runtime().Realm()
	_ = r.Global.SetProp(r, engine.InternKey(name), val)
}

func TestVMNoGlobals(t *testing.T) {
	v := newTestVM(t, "", VMOptions{})
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
	v := newTestVM(t, `export default function () { return 1 }; while (true) {}`, VMOptions{Clock: clock})
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
	v := newTestVM(t, `export default async (ctx) => { globalThis.seen = ctx.name }`, VMOptions{})
	err := v.Init(context.Background(), Invocation{Kind: KindInit}, func(c *CallScope) (moejs.Value, error) {
		return jsonValue(c.Runtime(), map[string]any{"name": "ok"})
	})
	require.NoError(t, err)
	got, err := run(v, context.Background(), `seen`)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)

	v = newTestVM(t, `var plugin = {}`, VMOptions{})
	assert.ErrorContains(t, v.Init(context.Background(), Invocation{Kind: KindInit}, nil), "default")
}

func TestCallInterruptLoopVMReusable(t *testing.T) {
	v := newTestVM(t, "", VMOptions{})
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
func asyncFn(c *CallScope, release <-chan struct{}) {
	setGlobal(c, "wait", c.vm.HostFunc("wait", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
		x := moejs.Arg(args, 0).String()
		return c.AsyncJSON(func(ctx context.Context) (any, error) {
			<-release
			if x == "fail" {
				return nil, errors.New("nope")
			}
			return "got:" + x, nil
		}), nil
	}))
}

func TestAsyncHostFunc(t *testing.T) {
	v := newTestVM(t, "", VMOptions{})
	release := make(chan struct{})
	go func() { time.Sleep(10 * time.Millisecond); close(release) }()
	got, err := v.Call(context.Background(), inv, func(c *CallScope) (moejs.Value, error) {
		asyncFn(c, release)
		return script(c, `(async () => {
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

func TestCallsSerialized(t *testing.T) {
	v := newTestVM(t, "", VMOptions{})
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
			_, err := v.Call(context.Background(), inv, func(c *CallScope) (moejs.Value, error) {
				note(name + ":start")
				asyncFn(c, release)
				setGlobal(c, "note", c.vm.HostFunc("note", func(_ *CallScope, args []moejs.Value) (moejs.Value, error) {
					note(name + ":" + moejs.Arg(args, 0).String())
					return moejs.Undefined(), nil
				}))
				return script(c, `(async () => { for (let i = 0; i < 20; i++) await wait("x"); note("end") })()`)
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
	v := newTestVM(t, "", VMOptions{})
	started := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		_, err := v.Call(context.Background(), inv, func(c *CallScope) (moejs.Value, error) {
			close(started)
			return script(c, `while (true) {}`)
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
	v := newTestVM(t, "", VMOptions{Redact: func(s string) string { return strings.ReplaceAll(s, "s3cret-value", "[redacted]") }})
	got, err := v.Call(context.Background(), inv, func(c *CallScope) (moejs.Value, error) {
		set := func(name string, f hostFn) { setGlobal(c, name, v.HostFunc(name, f)) }
		fail := func(err error) hostFn {
			return func(*CallScope, []moejs.Value) (moejs.Value, error) { return moejs.Undefined(), err }
		}
		set("crash", func(*CallScope, []moejs.Value) (moejs.Value, error) {
			var s []int
			return engine.IntValue(s[3]), nil
		})
		set("crashPath", func(*CallScope, []moejs.Value) (moejs.Value, error) {
			panic("open /Users/someone/godwit/core/plugins/vm.go: oops")
		})
		set("fail", fail(&PermissionError{Perm: "token s3cret-value"}))
		set("throwJS", fail(typeError("bad /etc/passwd")))
		set("readFile", fail(&os.PathError{Op: "open", Path: "/Users/someone/data/godwit.db", Err: os.ErrNotExist}))
		set("winPath", fail(errors.New(`open C:\Users\someone\godwit.db: denied`)))
		set("colonPath", fail(errors.New("open:/Users/alice/x.db failed")))
		set("winColon", fail(errors.New(`path:C:\Users\alice\x.db`)))
		set("urlErr", fail(errors.New("fetch https://example.com/a/b: status 500")))
		set("laterColon", func(c *CallScope, _ []moejs.Value) (moejs.Value, error) {
			return c.AsyncJSON(func(context.Context) (any, error) {
				return nil, errors.New("stat:/Users/alice/y|open=C:/Users/alice/z")
			}), nil
		})
		set("later", func(c *CallScope, _ []moejs.Value) (moejs.Value, error) {
			return c.AsyncJSON(func(context.Context) (any, error) {
				return nil, errors.New("read /var/lib/godwit/x: key s3cret-value: " + os.ErrPermission.Error())
			}), nil
		})
		return script(c, `(async () => {
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

	// A panic in the call body itself does not kill the runtime.
	_, err = v.Call(context.Background(), inv, func(c *CallScope) (moejs.Value, error) { panic("body /Users/x.go") })
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "/Users")
	got, err = run(v, context.Background(), `1+1`)
	require.NoError(t, err)
	assert.EqualValues(t, 2, got)
}

func TestRecursionLimited(t *testing.T) {
	v := newTestVM(t, "", VMOptions{})
	_, err := run(v, context.Background(), `function f(n) { return f(n + 1) + 1 } f(0)`)
	require.Error(t, err)
	got, err := run(v, context.Background(), `"alive"`)
	require.NoError(t, err)
	assert.Equal(t, "alive", got)
}

func TestExportPlainData(t *testing.T) {
	v := newTestVM(t, "", VMOptions{})
	got, err := run(v, context.Background(), `({a: 1, b: [1, "x", null], c: {d: true}, f() {}, u: undefined})`)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": 1.0, "b": []any{1.0, "x", nil}, "c": map[string]any{"d": true}}, got)

	got, err = run(v, context.Background(), `undefined`)
	require.NoError(t, err)
	assert.Nil(t, got)

	_, err = run(v, context.Background(), `"x".repeat(1 << 20)`)
	assert.ErrorIs(t, err, ErrTooLarge)
}

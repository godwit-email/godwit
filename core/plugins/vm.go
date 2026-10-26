package plugins

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/Calcium-Ion/moejs"
	"github.com/Calcium-Ion/moejs/engine"
	"github.com/Calcium-Ion/moejs/eventloop"
)

// MaxResultBytes caps the JSON a plugin call hands back to the host.
const MaxResultBytes = 1 << 20

// MemoryLimit is what a plugin's runtime may hold: past it the runtime stops
// the call with ErrMemoryLimit. The budget spans the event loop's run, across
// host awaits, and an allocation past it first measures what the plugin still
// keeps, so one that keeps little may allocate as much as it likes.
const MemoryLimit = 256 << 20

// Clock schedules the call deadlines; tests inject a fake one.
type Clock interface {
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// VMOptions: a nil Clock is the real clock, a nil Redact the identity, a zero
// MemoryLimit the package's.
type VMOptions struct {
	Clock       Clock
	Redact      func(string) string
	MemoryLimit int64
}

func callTimeout(k InvocationKind) time.Duration {
	if k == KindCard || k == KindBeforeSend {
		return 5 * time.Second
	}
	return 30 * time.Second
}

// absPath matches an absolute Unix, Windows drive or UNC path anywhere it is
// not glued to a preceding word, dot or slash (so "open:/x" and "a=C:\\x" match,
// but "https://h/p" and "a/b" do not: a Unix path's second character is not
// '/', and "/p" in a URL follows a word character).
var absPath = regexp.MustCompile(`(^|[^\w./\\])(?:/[^/\s"'()\[\],;:|][^\s"'()\[\],;:|]*|[A-Za-z]:[\\/][^\s"'()\[\],;:|]*|\\\\[^\s"'()\[\],;:|]+)`)

// ScrubPaths replaces absolute paths in s with "<path>".
func ScrubPaths(s string) string { return absPath.ReplaceAllString(s, "${1}<path>") }

// VM is one plugin's moejs runtime (docs/adr/0024-pi-moejs-agent-runtime.md).
// Its globals are the
// builtins moejs ships and nothing the host adds: no timers, fetch or console,
// nothing of core/ai/pi's bundle. Calls run one at a time on the caller's
// goroutine through the runtime's event loop; host work settles its promise
// from another goroutine. A call ends at settlement, its context's end,
// Close, or its kind's time limit, by Interrupt; the runtime's MemoryLimit
// stops one that allocates too much.
type VM struct {
	name   string
	mod    *moejs.Module
	rt     *moejs.Runtime
	loop   *eventloop.Loop
	clock  Clock
	redact func(string) string

	turn  chan struct{} // held by the running call
	life  context.Context
	kill  context.CancelCauseFunc
	close sync.Once

	mu     sync.Mutex // guards cur for settlements posted from other goroutines
	cur    *CallScope
	nextID uint64
}

// CallScope is one top-level call of a VM, valid while that call runs.
type CallScope struct {
	vm  *VM
	id  uint64
	inv Invocation
	ctx context.Context
}

// eventloop.New installs these; a plugin is not granted them.
var timerGlobals = []string{"setTimeout", "clearTimeout", "setInterval", "clearInterval", "setImmediate", "clearImmediate"}

// NewVM creates the runtime; it runs no plugin code.
func NewVM(name string, mod *moejs.Module, opts VMOptions) (*VM, error) {
	rt := moejs.NewRuntime(moejs.Options{MemoryLimit: cmp.Or(opts.MemoryLimit, MemoryLimit)})
	loop, err := eventloop.New(rt, eventloop.Options{})
	if err != nil {
		return nil, err
	}
	r := rt.Realm()
	for _, n := range timerGlobals {
		if !r.Global.Delete(r, engine.InternKey(n)) {
			return nil, fmt.Errorf("plugin %s: cannot remove %s", name, n)
		}
	}
	v := &VM{name: name, mod: mod, rt: rt, loop: loop, clock: opts.Clock, redact: opts.Redact, turn: make(chan struct{}, 1)}
	if v.clock == nil {
		v.clock = realClock{}
	}
	if v.redact == nil {
		v.redact = func(s string) string { return s }
	}
	v.life, v.kill = context.WithCancelCause(context.Background())
	return v, nil
}

// Close refuses new calls (ErrClosed), interrupts the running one, waits for
// it and ends the event loop, dropping what host work would still settle.
func (v *VM) Close() {
	v.close.Do(func() {
		v.kill(ErrClosed)
		v.turn <- struct{}{}
		v.loop.Terminate()
	})
}

// Call runs fn as one top-level call, after any call already running or
// queued. It ends at settlement, ctx's end, Close (ErrClosed),
// the kind's time limit (ErrTimeout), and the memory limit (ErrMemoryLimit).
// A promise fn returns is awaited; the result is exported as plain JSON data
// of at most MaxResultBytes (else ErrTooLarge).
func (v *VM) Call(ctx context.Context, inv Invocation, fn func(c *CallScope) (moejs.Value, error)) (any, error) {
	if v.life.Err() != nil {
		return nil, ErrClosed
	}
	select {
	case v.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-v.life.Done():
		return nil, ErrClosed
	}
	defer func() { <-v.turn }()
	if v.life.Err() != nil {
		return nil, ErrClosed
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer context.AfterFunc(v.life, func() { cancel(ErrClosed) })()
	defer v.clock.AfterFunc(callTimeout(inv.Kind), func() { cancel(ErrTimeout) })()

	v.mu.Lock()
	v.nextID++
	c := &CallScope{vm: v, id: v.nextID, inv: inv, ctx: ctx}
	v.cur = c
	v.mu.Unlock()
	defer func() { v.mu.Lock(); v.cur = nil; v.mu.Unlock() }()

	// Once the AfterFunc has started, wait for its interrupt to
	// land before clearing it, or the next call is interrupted.
	landed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { v.loop.Interrupt(context.Cause(ctx)); close(landed) })
	defer func() {
		if !stop() {
			<-landed
		}
		v.rt.ClearInterrupt() // a memory limit hit is an interrupt too
	}()

	res, err := v.exec(fn, c)
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return res, err
}

func (v *VM) exec(fn func(c *CallScope) (moejs.Value, error), c *CallScope) (any, error) {
	var res moejs.Value
	// Run ends as the call's result settles; host work still in flight is
	// cancelled with the call's context and settles into nothing. With nothing
	// left that could settle it (no timers keep the loop alive), Run ends too.
	err := v.loop.Run(func(rt *moejs.Runtime) error {
		rt.ResetAllocation() // a budget per call, not per runtime
		var err error
		func() {
			defer func() {
				if recover() != nil {
					err = errors.New("internal error")
				}
			}()
			res, err = fn(c)
		}()
		if err != nil {
			return err
		}
		if _, _, ok := moejs.PromiseResult(res); !ok {
			v.loop.StopNoWait()
			return nil
		}
		then, err := rt.Get(res, "then")
		if err != nil {
			return err
		}
		stop := rt.Function("", 0, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			v.loop.StopNoWait()
			return moejs.Undefined(), nil
		})
		_, err = rt.CallFunction(then, res, stop, stop)
		return err
	})
	if err != nil {
		return nil, v.scriptError(err)
	}
	if state, val, ok := moejs.PromiseResult(res); ok {
		switch state {
		case moejs.PromisePending: // nothing left can settle it: it ends as the call does (a timeout, say)
			<-c.ctx.Done()
			return nil, context.Cause(c.ctx)
		case moejs.PromiseRejected:
			return nil, fmt.Errorf("plugin %s: %s", v.name, v.clean(v.describe(val)))
		}
		res = val
	}
	return v.export(res)
}

// export turns a JS value into plain Go data, as JSON.stringify sees it.
func (v *VM) export(val moejs.Value) (any, error) {
	if val.IsUndefined() {
		return nil, nil
	}
	var out any
	if err := v.rt.UnmarshalWith(val, &out, moejs.DecodeOptions{MaxBytes: MaxResultBytes}); err != nil {
		if errors.Is(err, moejs.ErrTooLarge) {
			return nil, ErrTooLarge
		}
		return nil, v.scriptError(err)
	}
	return out, nil
}

// scriptError reports a JS failure by its thrown value only: no JS or Go stack.
func (v *VM) scriptError(err error) error {
	var exc *moejs.Exception
	var intr *moejs.InterruptedError
	var internal *moejs.InternalError
	switch {
	case errors.Is(err, moejs.ErrMemoryLimit):
		return ErrMemoryLimit
	case errors.As(err, &intr):
		return err // the caller replaces it with the context's cause
	case errors.As(err, &internal):
		return fmt.Errorf("plugin %s: internal error", v.name)
	case errors.As(err, &exc):
		return fmt.Errorf("plugin %s: %s", v.name, v.clean(exc.Error()))
	}
	return fmt.Errorf("plugin %s: %s", v.name, v.clean(err.Error()))
}

// describe renders a rejection as String(reason) would: "Name: message" for an Error.
func (v *VM) describe(val moejs.Value) string {
	if !val.IsObject() || val.AsObject().ErrorData() == nil {
		return val.String()
	}
	name, _ := v.rt.Get(val, "name")
	msg, _ := v.rt.Get(val, "message")
	if m := msg.String(); m != "" && !msg.IsUndefined() {
		return name.String() + ": " + m
	}
	return name.String()
}

func (v *VM) clean(s string) string { return ScrubPaths(v.redact(s)) }

// running reports whether call id is the one running.
func (v *VM) running(id uint64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cur != nil && v.cur.id == id
}

// typeError is thrown to JS as a TypeError with this message.
type typeError string

func (e typeError) Error() string { return string(e) }

// HostFunc wraps a host function for JS: fn gets the
// scope of the call JS invoked it in, and what it returns as an error JS gets
// as a plain Error whose message went through Redact and path scrubbing:
// "<name>: " + the error's message, a typeError as a TypeError of its own
// message, a panic as "<name>: internal error". Interrupts pass through.
func (v *VM) HostFunc(name string, fn func(c *CallScope, args []moejs.Value) (moejs.Value, error)) moejs.Value {
	return v.rt.Function(name, 0, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (res moejs.Value, err error) {
		v.mu.Lock()
		c := v.cur
		v.mu.Unlock()
		if c == nil {
			return moejs.Undefined(), errors.New(name + ": not in a call")
		}
		defer func() {
			if recover() != nil {
				res, err = moejs.Undefined(), errors.New(name+": internal error")
			}
		}()
		res, err = fn(c, args)
		if err == nil {
			return res, nil
		}
		var intr *moejs.InterruptedError
		var te typeError
		switch {
		case errors.As(err, &intr):
			return moejs.Undefined(), err
		case errors.As(err, &te):
			return moejs.Undefined(), r.TypeError("%s", v.clean(string(te)))
		}
		return moejs.Undefined(), errors.New(name + ": " + v.clean(err.Error()))
	})
}

// jsonValue is x as JS data, through its JSON (json tags name struct fields).
func jsonValue(rt *moejs.Runtime, x any) (moejs.Value, error) {
	b, err := json.Marshal(x)
	if err != nil {
		return moejs.Undefined(), err
	}
	return rt.ParseJSONString(string(b))
}

// Async returns a promise settled by fn, which runs on its own goroutine with
// the call's context; its result is made into a JS value on the loop by the
// function it returns (nil: undefined). An error rejects with a plain Error
// whose message is redacted and path-scrubbed. A settlement that arrives
// after its call ended never settles anything: that call's JS never runs again.
func (c *CallScope) Async(fn func(ctx context.Context) (func(*moejs.Runtime) (moejs.Value, error), error)) moejs.Value {
	v := c.vm
	p, settle := v.loop.NewPromise()
	go func() {
		mk, err := func() (mk func(*moejs.Runtime) (moejs.Value, error), err error) {
			defer func() {
				if recover() != nil {
					err = errors.New("internal error")
				}
			}()
			return fn(c.ctx)
		}()
		settle(func(rt *moejs.Runtime) (moejs.Value, error) {
			if !v.running(c.id) {
				never, _, _ := rt.NewPromise() // adopted, so the old promise stays pending
				return never, nil
			}
			if err != nil {
				return moejs.Undefined(), errors.New(v.clean(err.Error()))
			}
			if mk == nil {
				return moejs.Undefined(), nil
			}
			return mk(rt)
		})
	}()
	return p
}

// AsyncJSON is Async whose result is plain JSON data (nil: null).
func (c *CallScope) AsyncJSON(fn func(ctx context.Context) (any, error)) moejs.Value {
	return c.Async(func(ctx context.Context) (func(*moejs.Runtime) (moejs.Value, error), error) {
		r, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		return func(rt *moejs.Runtime) (moejs.Value, error) { return jsonValue(rt, r) }, nil
	})
}

// arg decodes JS value val into out through JSON; anything else is a typeError.
func (c *CallScope) arg(fn string, val moejs.Value, out any, what string) error {
	if val.IsNullish() || val.IsSymbol() || val.IsObject() && val.AsObject().IsCallable() {
		return typeError(fn + ": " + what)
	}
	if err := c.vm.rt.UnmarshalWith(val, out, moejs.DecodeOptions{MaxBytes: MaxResultBytes}); err != nil {
		var intr *moejs.InterruptedError
		switch {
		case errors.As(err, &intr):
			return err
		case errors.Is(err, moejs.ErrTooLarge):
			return ErrTooLarge
		}
		return typeError(fn + ": " + what)
	}
	return nil
}

// Init loads the module and then runs its default export with jsCtx(c) as
// one call, awaiting it if it is async. A nil jsCtx passes undefined.
func (v *VM) Init(ctx context.Context, inv Invocation, jsCtx func(c *CallScope) (moejs.Value, error)) error {
	_, err := v.Call(ctx, inv, func(c *CallScope) (moejs.Value, error) {
		if err := v.rt.Load(v.mod); err != nil {
			return moejs.Undefined(), err
		}
		def, err := v.mod.Hook("default")
		if err != nil {
			return moejs.Undefined(), errors.New("plugin has no default export function")
		}
		arg := moejs.Undefined()
		if jsCtx != nil {
			if arg, err = jsCtx(c); err != nil {
				return moejs.Undefined(), err
			}
		}
		res, err := v.rt.Call(def, arg)
		if errors.Is(err, moejs.ErrNotCallable) || errors.Is(err, moejs.ErrHookNotFound) {
			return moejs.Undefined(), errors.New("plugin has no default export function")
		}
		if _, _, ok := moejs.PromiseResult(res); !ok {
			res = moejs.Undefined() // only a promise is kept, to be awaited
		}
		return res, err
	})
	return err
}

func (c *CallScope) Invocation() Invocation { return c.inv }

// Context ends with the call.
func (c *CallScope) Context() context.Context { return c.ctx }

// Runtime is the moejs runtime; use it only inside the call.
func (c *CallScope) Runtime() *moejs.Runtime { return c.vm.rt }

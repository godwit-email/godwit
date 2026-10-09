package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// MaxResultBytes caps the JSON a plugin call hands back to the host.
const MaxResultBytes = 1 << 20

// Clock schedules the call deadlines and the watchdog ticks; tests inject a
// fake one.
type Clock interface {
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// VMOptions: a nil Clock is the real clock, a nil Watchdog the default one, a
// nil Redact the identity.
type VMOptions struct {
	Clock    Clock
	Watchdog *Watchdog
	Redact   func(string) string
}

// VM is one plugin's goja runtime. Every touch of the runtime happens on its
// owner goroutine: top-level calls are queued and run one at a time there, and
// async host work posts its completion back to it. Nothing returned by Call is
// a goja.Value.
type VM struct {
	name    string
	program *goja.Program
	rt      *goja.Runtime
	clock   Clock
	wd      *Watchdog
	redact  func(string) string

	stringify goja.Callable // JSON.stringify as it was before plugin code ran

	calls   chan *callReq
	jobs    chan job
	life    context.Context
	kill    context.CancelCauseFunc
	stopped chan struct{}

	mu     sync.Mutex // guards cur for the watchdog
	cur    *CallScope
	nextID uint64

	dropped atomic.Int64 // completions of finished calls, dropped unrun (test hook)
}

// CallScope is one top-level call. It is only valid on the owner goroutine,
// inside the Call that made it (including host functions JS invokes then).
type CallScope struct {
	vm     *VM
	id     uint64
	inv    Invocation
	ctx    context.Context
	cancel context.CancelCauseFunc
}

type callReq struct {
	ctx  context.Context
	inv  Invocation
	fn   func(c *CallScope) (any, error)
	done chan callRes
}

type callRes struct {
	v   any
	err error
}

// job is an async completion; it runs only if call id is still the running call.
type job struct {
	id uint64
	f  func()
}

// NewVM creates the runtime and its owner goroutine; it runs no plugin code.
func NewVM(name string, program *goja.Program, opts VMOptions) *VM {
	v := &VM{
		name: name, program: program, rt: goja.New(),
		clock: opts.Clock, wd: opts.Watchdog, redact: opts.Redact,
		calls: make(chan *callReq), jobs: make(chan job), stopped: make(chan struct{}),
	}
	if v.clock == nil {
		v.clock = realClock{}
	}
	if v.wd == nil {
		v.wd = &Watchdog{}
	}
	if v.redact == nil {
		v.redact = func(s string) string { return s }
	}
	v.rt.SetMaxCallStackSize(10000)
	v.rt.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	v.stringify, _ = goja.AssertFunction(v.rt.Get("JSON").ToObject(v.rt).Get("stringify"))
	v.life, v.kill = context.WithCancelCause(context.Background())
	go v.loop()
	return v
}

func (v *VM) loop() {
	defer close(v.stopped)
	for {
		select {
		case req := <-v.calls:
			r, err := v.run(req)
			req.done <- callRes{r, err}
		case <-v.jobs:
			v.dropped.Add(1)
		case <-v.life.Done():
			return
		}
	}
}

// Close refuses new calls (ErrClosed), cancels the running one and waits for
// it, drops pending completions and stops the owner goroutine.
func (v *VM) Close() {
	v.kill(ErrClosed)
	<-v.stopped
}

// Call runs fn on the owner goroutine as one top-level call, after any call
// already running or queued. If fn returns a goja.Value it is awaited when it
// is a promise (pumping only this call's async completions) and exported as
// plain JSON data (map[string]any, []any, float64, string, bool, nil; at most
// MaxResultBytes, else ErrTooLarge); other results are returned as they are.
// The call ends at settlement, ctx's end, Close (ErrClosed), the kind's time
// limit (ErrTimeout) or the watchdog (ErrMemoryLimit); JS still running is
// interrupted.
func (v *VM) Call(ctx context.Context, inv Invocation, fn func(c *CallScope) (any, error)) (any, error) {
	if v.life.Err() != nil {
		return nil, ErrClosed
	}
	req := &callReq{ctx: ctx, inv: inv, fn: fn, done: make(chan callRes, 1)}
	select {
	case v.calls <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-v.life.Done():
		return nil, ErrClosed
	}
	r := <-req.done
	return r.v, r.err
}

// Init runs the bundle (the IIFE that defines `plugin`) and then
// plugin.default(jsCtx(c)) as one call, awaiting it if it is async. A nil jsCtx
// passes undefined.
func (v *VM) Init(ctx context.Context, inv Invocation, jsCtx func(c *CallScope) goja.Value) error {
	_, err := v.Call(ctx, inv, func(c *CallScope) (any, error) {
		if _, err := v.rt.RunProgram(v.program); err != nil {
			return nil, err
		}
		def, ok := goja.AssertFunction(v.rt.Get("plugin").ToObject(v.rt).Get("default"))
		if !ok {
			return nil, errors.New("plugin has no default export function")
		}
		arg := goja.Undefined()
		if jsCtx != nil {
			arg = jsCtx(c)
		}
		res, err := def(goja.Undefined(), arg)
		if err != nil {
			return nil, err
		}
		return v.discard(res), nil
	})
	return err
}

// discard keeps a promise (so Call awaits it) and drops any other value.
func (v *VM) discard(res goja.Value) goja.Value {
	if _, ok := res.Export().(*goja.Promise); ok {
		return res
	}
	return goja.Undefined()
}

func callTimeout(k InvocationKind) time.Duration {
	if k == KindCard || k == KindBeforeSend {
		return 5 * time.Second
	}
	return 30 * time.Second
}

func (v *VM) run(req *callReq) (any, error) {
	ctx, cancel := context.WithCancelCause(req.ctx)
	defer cancel(nil)
	defer context.AfterFunc(v.life, func() { cancel(ErrClosed) })()
	defer v.clock.AfterFunc(callTimeout(req.inv.Kind), func() { cancel(ErrTimeout) })()

	v.mu.Lock()
	v.nextID++
	c := &CallScope{vm: v, id: v.nextID, inv: req.inv, ctx: ctx, cancel: cancel}
	v.cur = c
	v.mu.Unlock()
	defer func() { v.mu.Lock(); v.cur = nil; v.mu.Unlock() }()
	defer v.wd.Watch(v, c.id)()

	// Interrupt as proven in the spike: once the AfterFunc has started, wait for
	// Interrupt to land before clearing it, or the next call is interrupted.
	landed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { v.rt.Interrupt(context.Cause(ctx)); close(landed) })
	defer func() {
		if !stop() {
			<-landed
			v.rt.ClearInterrupt()
		}
	}()

	res, err := v.exec(c, req.fn)
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return res, err
}

func (v *VM) exec(c *CallScope, fn func(c *CallScope) (any, error)) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("plugin %s: internal error", v.name)
		}
	}()
	r, err := fn(c)
	if err != nil {
		return nil, v.scriptError(err)
	}
	val, ok := r.(goja.Value)
	if !ok {
		return r, nil
	}
	if p, ok := val.Export().(*goja.Promise); ok {
		for p.State() == goja.PromiseStatePending {
			select {
			case j := <-v.jobs:
				if j.id != c.id {
					v.dropped.Add(1)
					continue
				}
				j.f()
			case <-c.ctx.Done():
				return nil, context.Cause(c.ctx)
			}
		}
		if p.State() == goja.PromiseStateRejected {
			return nil, fmt.Errorf("plugin %s: %s", v.name, v.redact(v.safeString(p.Result())))
		}
		val = p.Result()
	}
	return v.export(val)
}

// export turns a JS value into plain Go data through JSON on the owner.
func (v *VM) export(val goja.Value) (any, error) {
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil, nil
	}
	s, err := v.stringify(goja.Undefined(), val)
	if err != nil {
		return nil, v.scriptError(err)
	}
	if goja.IsUndefined(s) {
		return nil, nil
	}
	str := s.String()
	if len(str) > MaxResultBytes {
		return nil, ErrTooLarge
	}
	var out any
	if err := json.Unmarshal([]byte(str), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// scriptError reports a JS failure by its thrown value only: no JS or Go stack.
func (v *VM) scriptError(err error) error {
	var exc *goja.Exception
	var so *goja.StackOverflowError
	var intr *goja.InterruptedError
	switch {
	case errors.As(err, &intr):
		return err // the caller replaces it with the context's cause
	case errors.As(err, &so):
		return fmt.Errorf("plugin %s: call stack exceeded", v.name)
	case errors.As(err, &exc):
		return fmt.Errorf("plugin %s: %s", v.name, v.redact(v.safeString(exc.Value())))
	}
	return fmt.Errorf("plugin %s: %s", v.name, v.redact(err.Error()))
}

// safeString renders a thrown value; its toString is plugin code and may throw.
func (v *VM) safeString(val goja.Value) (s string) {
	defer func() {
		if recover() != nil {
			s = "uncaught exception"
		}
	}()
	if val == nil {
		return "undefined"
	}
	return val.String()
}

// abort ends call id with cause if it is still the running call.
func (v *VM) abort(id uint64, cause error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cur != nil && v.cur.id == id {
		v.cur.cancel(cause)
	}
}

// HostFunc wraps a host function for JS. Call it on the owner goroutine (from
// inside a Call or Init). fn gets the scope of the call JS invoked it in. To
// throw, fn panics: goja values (rt.NewTypeError, rt.NewGoError, ...) pass
// through as they are; an error is thrown as a GoError whose message is
// "<name>: " + Redact(err.Error()) (errors.Is still sees err from Go); any
// other panic, Go runtime errors included, becomes "<name>: internal error".
func (v *VM) HostFunc(name string, fn func(c *CallScope, call goja.FunctionCall) goja.Value) goja.Value {
	return v.rt.ToValue(func(call goja.FunctionCall) goja.Value {
		v.mu.Lock()
		c := v.cur
		v.mu.Unlock()
		if c == nil {
			panic(v.rt.NewGoError(fmt.Errorf("%s: not in a call", name)))
		}
		defer func() {
			if r := recover(); r != nil {
				panic(v.thrown(name, r))
			}
		}()
		return fn(c, call)
	})
}

func (v *VM) thrown(name string, r any) any {
	if t := reflect.TypeOf(r); t.PkgPath() == "github.com/dop251/goja" ||
		t.Kind() == reflect.Pointer && t.Elem().PkgPath() == "github.com/dop251/goja" {
		return r // goja's own throw (JS error value, exception, interrupt)
	}
	if err, ok := r.(error); ok {
		if _, isRuntime := err.(runtime.Error); !isRuntime {
			return v.rt.NewGoError(v.hostError(name, err))
		}
	}
	return v.rt.NewGoError(fmt.Errorf("%s: internal error", name))
}

func (v *VM) hostError(name string, err error) error {
	return &redactedError{msg: name + ": " + v.redact(err.Error()), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Runtime is the goja runtime; use it only on the owner goroutine during the call.
func (c *CallScope) Runtime() *goja.Runtime { return c.vm.rt }

func (c *CallScope) VM() *VM { return c.vm }

func (c *CallScope) Invocation() Invocation { return c.inv }

// Context ends with the call; synchronous host work should honour it, since
// Interrupt cannot stop a running Go function.
func (c *CallScope) Context() context.Context { return c.ctx }

// Async returns a promise settled by fn, which runs on its own goroutine with
// the call's context. The result goes through rt.ToValue (json tags name
// struct fields); an error rejects with a redacted GoError. The settlement is
// posted back to the owner goroutine tagged with this call; if the call has
// ended by then it is dropped and never runs.
func (c *CallScope) Async(fn func(ctx context.Context) (any, error)) goja.Value {
	v := c.vm
	p, resolve, reject := v.rt.NewPromise()
	go func() {
		res, err := func() (res any, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = errors.New("internal error")
				}
			}()
			return fn(c.ctx)
		}()
		j := job{id: c.id, f: func() {
			// Errors here are interrupts; the pump sees the ended context next.
			if err != nil {
				_ = reject(v.rt.NewGoError(&redactedError{msg: v.redact(err.Error()), err: err}))
			} else {
				_ = resolve(v.rt.ToValue(res))
			}
		}}
		select {
		case v.jobs <- j:
		case <-v.life.Done():
		}
	}()
	return v.rt.ToValue(p)
}

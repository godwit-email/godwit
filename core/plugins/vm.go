package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
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
	errorCtor goja.Value    // Error as it was before plugin code ran

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
	v.errorCtor = v.rt.Get("Error")
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
// MaxResultBytes, else ErrTooLarge). Go results are normalized the same way
// through encoding/json; one holding a goja.Value, func or channel is an error.
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
		return normalize(r)
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
			return nil, fmt.Errorf("plugin %s: %s", v.name, v.clean(v.safeString(p.Result())))
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
	return decode([]byte(s.String()))
}

func decode(b []byte) (any, error) {
	if len(b) > MaxResultBytes {
		return nil, ErrTooLarge
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

var gojaValueType = reflect.TypeFor[goja.Value]()

const maxResultDepth = 64

// normalize turns a host Go result into the same plain JSON data as a JS one.
// checkResult runs first, so encoding/json never sees a goja value, a cycle
// or an exponential fan-out of shared references.
func normalize(r any) (any, error) {
	w := resultWalker{budget: MaxResultBytes, onPath: map[resultRef]bool{}}
	if err := w.walk(reflect.ValueOf(r), 0); err != nil {
		return nil, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("plugin: result is not JSON data: %w", err)
	}
	return decode(b)
}

type resultRef struct {
	p uintptr
	t reflect.Type
}

// resultWalker visits what encoding/json would encode (exported, non "-"
// fields). Each value costs at least one byte of JSON (strings their length),
// so the byte budget also bounds the walk; shared references are walked once
// per use, exactly as json would expand them.
type resultWalker struct {
	budget int
	onPath map[resultRef]bool
}

func (w *resultWalker) walk(r reflect.Value, depth int) error {
	if !r.IsValid() {
		return nil
	}
	if depth > maxResultDepth {
		return fmt.Errorf("%w: result nested deeper than %d", ErrTooLarge, maxResultDepth)
	}
	w.budget--
	if r.Kind() == reflect.String {
		w.budget -= r.Len()
	}
	if w.budget < 0 {
		return ErrTooLarge
	}
	if r.Type().Implements(gojaValueType) {
		return errors.New("plugin: result holds a goja value")
	}
	switch r.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if r.IsNil() {
			return nil
		}
		ref := resultRef{r.Pointer(), r.Type()}
		if w.onPath[ref] {
			return errors.New("plugin: result has a cycle")
		}
		w.onPath[ref] = true
		defer delete(w.onPath, ref)
	}
	switch r.Kind() {
	case reflect.Interface:
		return w.walk(r.Elem(), depth)
	case reflect.Pointer:
		return w.walk(r.Elem(), depth+1)
	case reflect.Map:
		for it := r.MapRange(); it.Next(); {
			if err := w.walk(it.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if r.Type().Elem().Kind() == reflect.Uint8 {
			w.budget -= r.Len() // json encodes bytes as one base64 string
			if w.budget < 0 {
				return ErrTooLarge
			}
			return nil
		}
		for i := range r.Len() {
			if err := w.walk(r.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		t := r.Type()
		for i := range r.NumField() {
			if f := t.Field(i); f.IsExported() && f.Tag.Get("json") != "-" {
				if err := w.walk(r.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
		return fmt.Errorf("plugin %s: %s", v.name, v.clean(v.safeString(exc.Value())))
	}
	return fmt.Errorf("plugin %s: %s", v.name, v.clean(err.Error()))
}

// absPath matches an absolute Unix, Windows drive or UNC path anywhere it is
// not glued to a preceding word, dot or slash (so "open:/x" and "a=C:\\x" match,
// but "https://h/p" and "a/b" do not: a Unix path's second character is not
// '/', and "/p" in a URL follows a word character).
var absPath = regexp.MustCompile(`(^|[^\w./\\])(?:/[^/\s"'()\[\],;:|][^\s"'()\[\],;:|]*|[A-Za-z]:[\\/][^\s"'()\[\],;:|]*|\\\\[^\s"'()\[\],;:|]+)`)

// clean is what any error text crossing into JS or back to the host goes
// through: secrets redacted, absolute paths replaced with "<path>".
func (v *VM) clean(s string) string { return ScrubPaths(v.redact(s)) }

// ScrubPaths replaces absolute paths in s with "<path>".
func ScrubPaths(s string) string { return absPath.ReplaceAllString(s, "${1}<path>") }

// jsError is a plain JS Error carrying only msg (no Go value behind it).
func (v *VM) jsError(msg string) goja.Value {
	o, err := v.rt.New(v.errorCtor, v.rt.ToValue(msg))
	if err != nil {
		return v.rt.ToValue(msg)
	}
	return o
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
// throw, fn panics; JS always gets a plain Error (no Go value behind it, so
// e.value is undefined) whose message went through Redact and path scrubbing:
//   - an error: "<name>: " + its message
//   - a goja error value (rt.NewTypeError(...), ...): its message, keeping
//     TypeError as TypeError
//   - anything else, Go runtime errors included: "<name>: internal error"
//
// Interrupts and stack overflows pass through untouched.
func (v *VM) HostFunc(name string, fn func(c *CallScope, call goja.FunctionCall) goja.Value) goja.Value {
	return v.rt.ToValue(func(call goja.FunctionCall) goja.Value {
		v.mu.Lock()
		c := v.cur
		v.mu.Unlock()
		if c == nil {
			panic(v.jsError(name + ": not in a call"))
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
	if err, ok := r.(error); ok {
		var intr *goja.InterruptedError
		var so *goja.StackOverflowError
		if errors.As(err, &intr) || errors.As(err, &so) {
			return r
		}
	}
	if exc, ok := r.(*goja.Exception); ok {
		r = exc.Value()
	}
	switch r := r.(type) {
	case *goja.Object:
		msg := v.clean(v.safeString(r.Get("message")))
		if v.safeString(r.Get("name")) == "TypeError" {
			return v.rt.NewTypeError("%s", msg)
		}
		return v.jsError(msg)
	case goja.Value:
		return v.jsError(v.clean(v.safeString(r)))
	case runtime.Error:
	case error:
		return v.jsError(name + ": " + v.clean(r.Error()))
	}
	return v.jsError(name + ": internal error")
}

// Runtime is the goja runtime; use it only on the owner goroutine during the call.
func (c *CallScope) Runtime() *goja.Runtime { return c.vm.rt }

func (c *CallScope) VM() *VM { return c.vm }

func (c *CallScope) Invocation() Invocation { return c.inv }

// Context ends with the call; synchronous host work should honour it, since
// Interrupt cannot stop a running Go function.
func (c *CallScope) Context() context.Context { return c.ctx }

// Async returns a promise settled by fn, which runs on its own goroutine with
// the call's context. The result goes through rt.ToValue (json tags name
// struct fields); an error rejects with a plain Error whose message is
// redacted and path-scrubbed. The settlement is
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
				_ = reject(v.jsError(v.clean(err.Error())))
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

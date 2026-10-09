package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var (
	ctxType   = reflect.TypeFor[context.Context]()
	errType   = reflect.TypeFor[error]()
	stopType  = reflect.TypeFor[func()]()
	errNilStr = errors.New("rpc: method returned a nil stream")
)

type resultKind int

const (
	resValues     resultKind = iota // (), (T), (error), (T, error), (T1, …, error)
	resStreamErr                    // (<-chan T, error)
	resStreamStop                   // (<-chan T, func())
)

type method struct {
	fn     reflect.Value
	ctx    bool
	params []reflect.Type
	kind   resultKind
	nvals  int  // value results before the optional error
	err    bool // last result is error
}

// Dispatcher calls a target's exported methods by name.
type Dispatcher struct{ methods map[string]*method }

// NewDispatcher reflects target's exported methods; expose (nil = all) picks
// which ones are callable. An exposed method with an unsupported signature is
// an error, never silently skipped.
func NewDispatcher(target any, expose func(method string) bool) (*Dispatcher, error) {
	v := reflect.ValueOf(target)
	d := &Dispatcher{methods: map[string]*method{}}
	for i := range v.NumMethod() {
		name := v.Type().Method(i).Name
		if expose != nil && !expose(name) {
			continue
		}
		m, err := analyze(v.Method(i))
		if err != nil {
			return nil, fmt.Errorf("rpc: method %s: %w", name, err)
		}
		d.methods[name] = m
	}
	return d, nil
}

func analyze(fn reflect.Value) (*method, error) {
	t := fn.Type()
	if t.IsVariadic() {
		return nil, errors.New("variadic parameters are not supported")
	}
	m := &method{fn: fn}
	for i := range t.NumIn() {
		p := t.In(i)
		if i == 0 && p == ctxType {
			m.ctx = true
			continue
		}
		if p == ctxType || !wireType(p) {
			return nil, fmt.Errorf("parameter %d has unsupported type %s", i, p)
		}
		m.params = append(m.params, p)
	}
	n := t.NumOut()
	if n == 2 && t.Out(0).Kind() == reflect.Chan {
		if t.Out(0).ChanDir()&reflect.RecvDir == 0 || !wireType(t.Out(0).Elem()) {
			return nil, fmt.Errorf("unsupported stream type %s", t.Out(0))
		}
		switch t.Out(1) {
		case errType:
			m.kind = resStreamErr
		case stopType:
			m.kind = resStreamStop
		default:
			return nil, fmt.Errorf("a stream must be (<-chan T, error) or (<-chan T, func()), not (%s, %s)", t.Out(0), t.Out(1))
		}
		return m, nil
	}
	m.nvals = n
	if n > 0 && t.Out(n-1) == errType {
		m.err = true
		m.nvals--
	} else if n > 1 {
		return nil, errors.New("multiple results must end with error")
	}
	for i := range m.nvals {
		if r := t.Out(i); r == errType || !wireType(r) {
			return nil, fmt.Errorf("result %d has unsupported type %s", i, r)
		}
	}
	return m, nil
}

func wireType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return false
	}
	return true
}

func (m *method) decode(params []json.RawMessage) ([]reflect.Value, *WireError) {
	if len(params) != len(m.params) {
		return nil, &WireError{Code: CodeInvalidParams, Message: fmt.Sprintf("want %d params, got %d", len(m.params), len(params))}
	}
	args := make([]reflect.Value, len(params))
	for i, p := range params {
		v := reflect.New(m.params[i])
		if err := json.Unmarshal(p, v.Interface()); err != nil {
			return nil, &WireError{Code: CodeInvalidParams, Message: fmt.Sprintf("param %d: want %s", i, m.params[i])}
		}
		args[i] = v.Elem()
	}
	return args, nil
}

// call runs the method. For a stream, ch is the channel and stop (maybe nil)
// the func the method returned; otherwise result is the JSON-ready value.
func (m *method) call(ctx context.Context, args []reflect.Value) (result any, ch reflect.Value, stop func(), err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("rpc: panic: %v", p)
		}
	}()
	if m.ctx {
		args = append([]reflect.Value{reflect.ValueOf(ctx)}, args...)
	}
	out := m.fn.Call(args)
	switch m.kind {
	case resStreamErr:
		if e := out[1].Interface(); e != nil {
			return nil, ch, nil, e.(error)
		}
	case resStreamStop:
		stop, _ = out[1].Interface().(func())
	default:
		if m.err {
			if e := out[len(out)-1].Interface(); e != nil {
				return nil, ch, nil, e.(error)
			}
		}
		switch m.nvals {
		case 0:
			return nil, ch, nil, nil
		case 1:
			return out[0].Interface(), ch, nil, nil
		}
		vals := make([]any, m.nvals)
		for i := range vals {
			vals[i] = out[i].Interface()
		}
		return vals, ch, nil, nil
	}
	if out[0].IsNil() {
		if stop != nil {
			stop()
		}
		return nil, ch, nil, errNilStr
	}
	return nil, out[0], stop, nil
}

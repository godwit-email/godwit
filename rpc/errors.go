package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// CodeApp is the wire code of a registered (application) error.
const CodeApp = -32003

// ErrBadErrorData: a registered type's wire detail did not decode.
var ErrBadErrorData = errors.New("rpc: malformed error data")

// RemoteError is a wire error the Codec has no registration for.
type RemoteError struct {
	Code    int
	Message string
}

func (e *RemoteError) Error() string { return e.Message }

// Codec maps errors to stable wire codes and back. Only registered errors
// cross the wire with their own text; anything else is "internal error".
// Register before use; a Codec is then safe for concurrent use.
type Codec struct {
	types     []regType
	sentinels []sentinel
	codes     map[string]func(json.RawMessage) error
}

type regType struct {
	code   string
	encode func(error) (detail any, message string, ok bool)
}

type sentinel struct {
	code, message string
	err           error
}

func (c *Codec) add(code string, decode func(json.RawMessage) error) {
	if _, dup := c.codes[code]; dup || code == "internal" {
		panic("rpc: error code registered twice: " + code)
	}
	if c.codes == nil {
		c.codes = map[string]func(json.RawMessage) error{}
	}
	c.codes[code] = decode
}

// RegisterType registers error type T under code. encode returns the safe
// fields to send (every string in them is redacted); decode rebuilds a T or
// fails, and Decode then returns ErrBadErrorData (see DecodeDetail).
func RegisterType[T error](c *Codec, code string, encode func(T) any, decode func(json.RawMessage) (T, error)) {
	c.add(code, func(raw json.RawMessage) error {
		t, err := decode(raw)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadErrorData, code, err)
		}
		return t
	})
	c.types = append(c.types, regType{code, func(err error) (any, string, bool) {
		var t T
		if !errors.As(err, &t) {
			return nil, "", false
		}
		return encode(t), t.Error(), true
	}})
}

// RegisterSentinel registers err under code; it crosses as the fixed message
// and decodes to err itself.
func (c *Codec) RegisterSentinel(code string, err error, message string) {
	c.add(code, func(json.RawMessage) error { return err })
	c.sentinels = append(c.sentinels, sentinel{code, message, err})
}

// Encode turns err into a wire error: registered types first (errors.As, in
// registration order; the first sentinel err also is goes along as Cause),
// then sentinels (errors.Is); redact (nil = none) cleans every string sent
// for a type.
func (c *Codec) Encode(err error, redact func(string) string) *WireError {
	if err == nil {
		return nil
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	for _, t := range c.types {
		detail, msg, ok := t.encode(err)
		if !ok {
			continue
		}
		raw, derr := redactJSON(detail, redact)
		if derr != nil {
			break
		}
		data := &WireErrorData{Code: t.code, Detail: raw}
		if s := c.sentinelFor(err); s != nil {
			data.Cause = s.code
		}
		return &WireError{Code: CodeApp, Message: redact(msg), Data: data}
	}
	if s := c.sentinelFor(err); s != nil {
		return &WireError{Code: CodeApp, Message: s.message, Data: &WireErrorData{Code: s.code}}
	}
	return internalError()
}

// sentinelFor is the first registered sentinel err is, or nil.
func (c *Codec) sentinelFor(err error) *sentinel {
	for i := range c.sentinels {
		if errors.Is(err, c.sentinels[i].err) {
			return &c.sentinels[i]
		}
	}
	return nil
}

// withCause is a decoded type that also is a sentinel, as it was on the server.
type withCause struct{ err, cause error }

func (w *withCause) Error() string   { return w.err.Error() }
func (w *withCause) Unwrap() []error { return []error{w.err, w.cause} }

// DecodeDetail decodes raw into a *V strictly: a JSON object holding every
// field of struct V (by Go field name; nested values are not checked for
// completeness) and nothing else.
func DecodeDetail[V any](raw json.RawMessage) (*V, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return nil, fmt.Errorf("want a JSON object: %v", err)
	}
	for f := range reflect.TypeFor[V]().Fields() {
		if _, ok := keys[f.Name]; f.IsExported() && !ok {
			return nil, fmt.Errorf("missing field %s", f.Name)
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	v := new(V)
	if err := d.Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}

// Decode maps a wire error back: a registered sentinel to the same value, a
// registered type to that type (wrapped so it also is its registered cause;
// an unknown cause is dropped), anything else to a *RemoteError.
func (c *Codec) Decode(we *WireError) error {
	if we == nil {
		return nil
	}
	if we.Data != nil {
		if decode, ok := c.codes[we.Data.Code]; ok {
			err := decode(we.Data.Detail)
			for _, s := range c.sentinels {
				if s.code == we.Data.Cause && !errors.Is(err, ErrBadErrorData) && !errors.Is(err, s.err) {
					return &withCause{err, s.err}
				}
			}
			return err
		}
	}
	return &RemoteError{Code: we.Code, Message: we.Message}
}

// redactJSON encodes v with every string (object keys too) passed through
// redact. Strings are cleaned before they are encoded, so a secret JSON would
// escape is still found.
func redactJSON(v any, redact func(string) string) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // int64 IDs keep their precision
	var tree any
	if err := d.Decode(&tree); err != nil {
		return nil, err
	}
	return json.Marshal(clean(tree, redact))
}

func clean(v any, redact func(string) string) any {
	switch v := v.(type) {
	case string:
		return redact(v)
	case []any:
		for i := range v {
			v[i] = clean(v[i], redact)
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[redact(k)] = clean(x, redact)
		}
		return out
	}
	return v
}

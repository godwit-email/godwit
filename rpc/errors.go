package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
)

// CodeApp is the wire code of a registered (application) error.
const CodeApp = -32003

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
// fields to send (every string in them is redacted); decode rebuilds a T.
func RegisterType[T error](c *Codec, code string, encode func(T) any, decode func(json.RawMessage) T) {
	c.add(code, func(raw json.RawMessage) error { return decode(raw) })
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
// registration order), then sentinels (errors.Is); redact (nil = none) cleans
// every string sent for a type.
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
		return &WireError{Code: CodeApp, Message: redact(msg), Data: &WireErrorData{Code: t.code, Detail: raw}}
	}
	for _, s := range c.sentinels {
		if errors.Is(err, s.err) {
			return &WireError{Code: CodeApp, Message: s.message, Data: &WireErrorData{Code: s.code}}
		}
	}
	return &WireError{Code: CodeInternal, Message: "internal error", Data: &WireErrorData{Code: "internal"}}
}

// Decode maps a wire error back: a registered sentinel to the same value, a
// registered type to that type, anything else to a *RemoteError.
func (c *Codec) Decode(we *WireError) error {
	if we == nil {
		return nil
	}
	if we.Data != nil {
		if decode, ok := c.codes[we.Data.Code]; ok {
			return decode(we.Data.Detail)
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

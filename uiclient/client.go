// Package uiclient implements the UI's Service interfaces over the Local API
// socket under the ui role.
//
//go:generate go run ./cmd/gen
package uiclient

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
)

// Client calls *core.Service over a RoleUI JSON-RPC connection.
type Client struct {
	rpc *rpc.Client

	mu         sync.Mutex
	errs       []error
	streamErrs map[any]error
}

// New wraps an authenticated RoleUI JSON-RPC client.
func New(c *rpc.Client) *Client {
	return &Client{
		rpc:        c,
		streamErrs: map[any]error{},
	}
}

// Dial connects to <dataDir>/godwit.sock as rpc.RoleUI with rpcbind's error codec.
func Dial(dataDir, clientName string) (*Client, error) {
	c, err := rpc.Dial(dataDir, rpc.RoleUI, cmp.Or(clientName, "godwit-ui"), rpcbind.NewCodec())
	if err != nil {
		return nil, err
	}
	return New(c), nil
}

// RPC returns the underlying JSON-RPC client.
func (c *Client) RPC() *rpc.Client { return c.rpc }

// Done is closed once the underlying connection is gone.
func (c *Client) Done() <-chan struct{} { return c.rpc.Done() }

// Close closes the underlying connection; pending calls and streams end with rpc.ErrClosed.
func (c *Client) Close() error { return c.rpc.Close() }

// StoreLatestVersion is core.Service's, for the platform's About check (app
// does not call it, so it is not generated from app.Service).
func (c *Client) StoreLatestVersion(ctx context.Context) (string, error) {
	return call[string](c, ctx, "StoreLatestVersion")
}

// StreamErr reports why a typed stream channel returned by Client closed: nil
// for a normal end or a caller cancel, rpc.ErrClosed on disconnect,
// rpc.ErrStreamOverflow if the reader fell behind, or a decoded wire error.
func (c *Client) StreamErr(ch any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.streamErrs[ch]
	delete(c.streamErrs, ch)
	return err
}

// StreamErrs returns every abnormal stream or void-call failure recorded so far.
func (c *Client) StreamErrs() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]error(nil), c.errs...)
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	c.errs = append(c.errs, err)
	c.mu.Unlock()
}

func (c *Client) recordStreamErr(key any, method string, err error) {
	wrapped := fmt.Errorf("%s: %w", method, err)
	c.mu.Lock()
	if key != nil && c.streamErrs[key] == nil {
		c.streamErrs[key] = wrapped
	}
	c.errs = append(c.errs, wrapped)
	c.mu.Unlock()
}

func (c *Client) syncTimeZone(ctx context.Context) error {
	if zone, ok := core.TimeZone(ctx); ok {
		return c.rpc.Call(ctx, "SetTimeZone", nil, zone)
	}
	return nil
}

func callErr(c *Client, ctx context.Context, method string, args ...any) error {
	return c.rpc.Call(ctx, method, nil, args...)
}

func call[T any](c *Client, ctx context.Context, method string, args ...any) (T, error) {
	var v T
	if err := c.rpc.Call(ctx, method, &v, args...); err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// call2 decodes a two-value result, which crosses as a JSON array.
func call2[A, B any](c *Client, ctx context.Context, method string, args ...any) (A, B, error) {
	var a A
	var b B
	var raw []json.RawMessage
	err := c.rpc.Call(ctx, method, &raw, args...)
	if err == nil && len(raw) != 2 {
		err = fmt.Errorf("uiclient: %s returned %d values, want 2", method, len(raw))
	}
	if err == nil {
		err = json.Unmarshal(raw[0], &a)
	}
	if err == nil {
		err = json.Unmarshal(raw[1], &b)
	}
	if err != nil {
		var za A
		var zb B
		return za, zb, err
	}
	return a, b, nil
}

func callVal[T any](c *Client, ctx context.Context, method string, args ...any) T {
	var v T
	if err := c.rpc.Call(ctx, method, &v, args...); err != nil {
		c.fail(fmt.Errorf("%s: %w", method, err))
	}
	return v
}

func callVoid(c *Client, ctx context.Context, method string, args ...any) {
	if err := c.rpc.Call(ctx, method, nil, args...); err != nil {
		c.fail(fmt.Errorf("%s: %w", method, err))
	}
}

func stream[T any](c *Client, ctx context.Context, method string, args ...any) (<-chan T, func(), error) {
	if err := c.syncTimeZone(ctx); err != nil {
		return nil, nil, err
	}
	_, wit := any(*new(T)).(core.WitEvent)
	var drop func(json.RawMessage) bool
	if wit { // a stalled forwarder below drops old text on the rpc side too, never a run's end
		drop = func(b json.RawMessage) bool {
			var ev core.WitEvent
			return json.Unmarshal(b, &ev) == nil && witText(ev.Kind)
		}
	}
	raw, cancel, err := c.rpc.StreamDropping(ctx, method, drop, args...)
	if err != nil {
		return nil, nil, err
	}
	out, done, exited := make(chan T, rpc.StreamBuffer), make(chan struct{}), make(chan struct{})
	if wit {
		out = make(chan T) // what waits is the queue below, as in core's witStream
	}
	var key <-chan T = out
	var recOnce sync.Once
	record := func() {
		recOnce.Do(func() {
			if serr := c.rpc.StreamErr(raw); serr != nil {
				c.recordStreamErr(key, method, serr)
			}
		})
	}
	go func() {
		defer close(exited)
		defer close(out)
		var q []T
		for in := raw; in != nil || len(q) > 0; {
			var send chan<- T
			var next T
			if len(q) > 0 {
				send, next = out, q[0]
			}
			recv := in
			if !wit && len(q) > 0 {
				recv = nil // a reader that falls behind overflows the rpc stream
			}
			select {
			case b, ok := <-recv:
				if !ok {
					in = nil
					continue
				}
				var v T
				if err := json.Unmarshal(b, &v); err != nil {
					c.recordStreamErr(key, method, fmt.Errorf("event %s: %w", b, err))
					continue
				}
				if q = append(q, v); wit {
					q = dropText(q)
				}
			case send <- next:
				q = q[1:]
			case <-done:
				return
			}
		}
		record()
		cancel()
	}()
	// Record StreamErr before cancel() forgets it, and wait for the forwarder to exit.
	var stopOnce sync.Once
	return out, func() { stopOnce.Do(func() { close(done); record(); cancel(); <-exited }) }, nil
}

// witBuffer is core's: a Wit stream's reader that falls behind loses the
// oldest text-like events once more than witBuffer wait, never a run's end,
// on this side of the wire as in process.
const witBuffer = 64

// witText: a Wit event of kind k may be dropped, being no run's end.
func witText(k string) bool { return k != "done" && k != "error" && k != "interrupted" }

func dropText[T any](q []T) []T {
	text := func(v T) bool { return witText(any(v).(core.WitEvent).Kind) }
	waiting := 0
	for _, v := range q {
		if text(v) {
			waiting++
		}
	}
	if waiting > witBuffer {
		i := slices.IndexFunc(q, text)
		q = slices.Delete(q, i, i+1)
	}
	return q
}

func streamErr[T any](c *Client, ctx context.Context, method string, args ...any) (<-chan T, error) {
	ch, _, err := stream[T](c, ctx, method, args...)
	return ch, err
}

func streamStop[T any](c *Client, method string, args ...any) (<-chan T, func()) {
	ch, stop, err := stream[T](c, context.Background(), method, args...)
	if err != nil {
		closed := make(chan T)
		close(closed)
		var key <-chan T = closed
		c.recordStreamErr(key, method, err)
		return closed, func() {}
	}
	return ch, stop
}

func consumeStream[T any](c *Client, ctx context.Context, method string, fn func(T) error, args ...any) error {
	raw, cancel, err := c.rpc.Stream(ctx, method, args...)
	if err != nil {
		return err
	}
	for b := range raw {
		var v T
		if err := json.Unmarshal(b, &v); err != nil {
			cancel()
			return fmt.Errorf("%s: event %s: %w", method, b, err)
		}
		if fn != nil {
			if err := fn(v); err != nil {
				cancel()
				return err
			}
		}
	}
	serr := c.rpc.StreamErr(raw)
	cancel()
	if serr != nil {
		return serr
	}
	return ctx.Err()
}

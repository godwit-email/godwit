// Package rpc is Godwit's line-delimited JSON-RPC 2.0 transport: a strict
// subset of the spec (positive integer IDs, positional params, no batches),
// reflective dispatch over a target's methods, and cancellable streams.
//
// Control messages, all notifications:
//
//	$/stream        [S, event]   server → client, one stream event
//	$/stream.end    [S] | [S, error]
//	$/stream.cancel [S]          client → server
//	$/cancel        [id]         client → server, cancel an in-flight request
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// WireError is a JSON-RPC error object.
type WireError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    *WireErrorData `json:"data,omitempty"`
}

// WireErrorData carries a stable error code and safe fields.
type WireErrorData struct {
	Code   string          `json:"code"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

func (e *WireError) Error() string { return e.Message }

// Error codes.
const (
	CodeParse           = -32700
	CodeInvalidRequest  = -32600
	CodeMethodNotFound  = -32601
	CodeInvalidParams   = -32602
	CodeInternal        = -32603
	CodeTooMany         = -32000
	CodeForbidden       = -32001
	CodeUnauthenticated = -32002
)

// ConnOptions bounds one connection. Zero fields take the defaults.
type ConnOptions struct {
	MaxMessage    int           // largest inbound line, 16 MB
	MaxActive     int           // in-flight requests plus live streams, 32
	QueueMessages int           // send queue, 1024 messages …
	QueueBytes    int           // … or 32 MB; overflow drops the connection
	WriteTimeout  time.Duration // per write, 10 s
	// Hello, if set, must authenticate the connection: the first request
	// must be "hello"; its allow func then gates every later call.
	Hello       func(params []json.RawMessage) (allow func(method string, params []json.RawMessage) bool, result any, err error)
	AuthTimeout time.Duration // 5 s
	// EncodeError turns a target's error into a wire error; the default
	// reveals nothing ("internal error").
	EncodeError func(error) *WireError
}

func (o ConnOptions) withDefaults() ConnOptions {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&o.MaxMessage, 16<<20)
	def(&o.MaxActive, 32)
	def(&o.QueueMessages, 1024)
	def(&o.QueueBytes, 32<<20)
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.AuthTimeout <= 0 {
		o.AuthTimeout = 5 * time.Second
	}
	if o.EncodeError == nil {
		o.EncodeError = func(error) *WireError {
			return &WireError{Code: CodeInternal, Message: "internal error", Data: &WireErrorData{Code: "internal"}}
		}
	}
	return o
}

var (
	errSlow      = errors.New("rpc: send queue full")
	errDuplicate = errors.New("rpc: duplicate in-flight request id")
	errAuth      = errors.New("rpc: not authenticated")
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *WireError      `json:"error,omitempty"`
}

var nullID = json.RawMessage("null")

// parseID accepts only positive integers.
func parseID(raw json.RawMessage) (uint64, bool) {
	id, err := strconv.ParseUint(string(raw), 10, 63)
	return id, err == nil && id > 0
}

type serverStream struct {
	once   sync.Once
	cancel func() // cancels the stream's ctx, then calls the target's stop
}

func (s *serverStream) stop() { s.once.Do(s.cancel) }

type conn struct {
	opts ConnOptions
	d    *Dispatcher
	rwc  io.ReadWriteCloser
	ctx  context.Context
	kill context.CancelCauseFunc // closes rwc via AfterFunc
	wg   sync.WaitGroup

	qmu    sync.Mutex
	queue  [][]byte // nil entry = close after flushing what precedes it
	qbytes int
	qdone  bool
	qready chan struct{}

	mu         sync.Mutex
	active     int
	requests   map[uint64]context.CancelFunc
	streams    map[uint64]*serverStream // nil once the connection is torn down
	nextStream uint64
	allow      func(string, []json.RawMessage) bool
}

// ServeConn serves one connection until it closes, breaks a budget, or ctx
// ends. On return every stream has been cancelled and drained.
func ServeConn(ctx context.Context, rwc io.ReadWriteCloser, d *Dispatcher, opts ConnOptions) error {
	c := &conn{
		opts:     opts.withDefaults(),
		d:        d,
		rwc:      rwc,
		qready:   make(chan struct{}, 1),
		requests: map[uint64]context.CancelFunc{},
		streams:  map[uint64]*serverStream{},
	}
	c.ctx, c.kill = context.WithCancelCause(ctx)
	context.AfterFunc(c.ctx, func() { rwc.Close() }) // kill always fires it
	c.wg.Add(1)
	go c.writeLoop()
	if c.opts.Hello != nil {
		defer time.AfterFunc(c.opts.AuthTimeout, func() {
			c.mu.Lock()
			authed := c.allow != nil
			c.mu.Unlock()
			if !authed {
				c.kill(errAuth)
			}
		}).Stop()
	}

	sc := bufio.NewScanner(rwc)
	sc.Buffer(make([]byte, 0, 64<<10), c.opts.MaxMessage)
	for sc.Scan() {
		if !c.handle(sc.Bytes()) {
			// The final error response is queued; give it a chance to go out.
			select {
			case <-c.ctx.Done():
			case <-time.After(c.opts.WriteTimeout):
			}
			break
		}
	}
	err := sc.Err()
	if c.ctx.Err() != nil {
		err = context.Cause(c.ctx)
	}
	c.kill(err)
	c.mu.Lock()
	streams := c.streams
	c.streams = nil
	c.mu.Unlock()
	for _, s := range streams {
		s.stop()
	}
	c.wg.Wait()
	return err
}

// handle processes one inbound line; false stops reading.
func (c *conn) handle(line []byte) bool {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return true
	}
	if line[0] == '[' {
		c.replyError(nullID, &WireError{Code: CodeInvalidRequest, Message: "batch requests are not supported"})
		return true
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		c.replyError(nullID, &WireError{Code: CodeParse, Message: "parse error"})
		return true
	}
	if len(m.ID) == 0 {
		if m.JSONRPC == "2.0" && len(m.Method) > 2 && m.Method[:2] == "$/" {
			c.control(m)
		} else {
			c.replyError(nullID, &WireError{Code: CodeInvalidRequest, Message: "notifications are only for $/ control messages"})
		}
		return true
	}
	id, ok := parseID(m.ID)
	if !ok {
		c.replyError(nullID, &WireError{Code: CodeInvalidRequest, Message: "id must be a positive integer"})
		return true
	}
	if m.JSONRPC != "2.0" || m.Method == "" {
		c.replyError(m.ID, &WireError{Code: CodeInvalidRequest, Message: "invalid request"})
		return true
	}
	var params []json.RawMessage
	if len(m.Params) > 0 && (m.Params[0] != '[' || json.Unmarshal(m.Params, &params) != nil) {
		c.replyError(m.ID, &WireError{Code: CodeInvalidParams, Message: "params must be an array"})
		return true
	}

	c.mu.Lock()
	allow := c.allow
	c.mu.Unlock()
	if c.opts.Hello != nil {
		if allow == nil {
			return c.hello(m, params)
		}
		if m.Method == "hello" {
			c.replyError(m.ID, &WireError{Code: CodeInvalidRequest, Message: "already authenticated"})
			return true
		}
		if !allow(m.Method, params) {
			c.replyError(m.ID, &WireError{Code: CodeForbidden, Message: "forbidden", Data: &WireErrorData{Code: "forbidden"}})
			return true
		}
	}

	meth := c.d.methods[m.Method]
	if meth == nil {
		c.replyError(m.ID, &WireError{Code: CodeMethodNotFound, Message: "method not found"})
		return true
	}
	args, werr := meth.decode(params)
	if werr != nil {
		c.replyError(m.ID, werr)
		return true
	}
	c.mu.Lock()
	if _, dup := c.requests[id]; dup {
		c.mu.Unlock()
		c.kill(errDuplicate)
		return false
	}
	if c.active >= c.opts.MaxActive {
		c.mu.Unlock()
		c.replyError(m.ID, &WireError{Code: CodeTooMany, Message: "too many requests", Data: &WireErrorData{Code: "too_many_requests"}})
		return true
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.requests[id] = cancel
	c.active++
	c.mu.Unlock()
	c.wg.Add(1)
	go c.run(id, m.ID, meth, ctx, cancel, args)
	return true
}

func (c *conn) hello(m message, params []json.RawMessage) bool {
	if m.Method != "hello" {
		c.replyError(m.ID, &WireError{Code: CodeUnauthenticated, Message: "hello first", Data: &WireErrorData{Code: "unauthenticated"}})
		c.closeAfterFlush()
		return false
	}
	allow, result, err := c.opts.Hello(params)
	if err != nil {
		c.replyError(m.ID, c.encodeError(err))
		c.closeAfterFlush()
		return false
	}
	if allow == nil {
		allow = func(string, []json.RawMessage) bool { return true }
	}
	c.mu.Lock()
	c.allow = allow
	c.mu.Unlock()
	c.replyResult(m.ID, result)
	return true
}

func (c *conn) control(m message) {
	var params []json.RawMessage
	var n uint64
	if json.Unmarshal(m.Params, &params) != nil || len(params) < 1 || json.Unmarshal(params[0], &n) != nil {
		return
	}
	c.mu.Lock()
	cancel := c.requests[n]
	s := c.streams[n]
	c.mu.Unlock()
	switch {
	case m.Method == "$/cancel" && cancel != nil:
		cancel()
	case m.Method == "$/stream.cancel" && s != nil && c.endStream(n):
		s.stop()
	}
}

func (c *conn) release() {
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
}

func (c *conn) run(id uint64, rawID json.RawMessage, meth *method, ctx context.Context, cancel context.CancelFunc, args []reflect.Value) {
	defer c.wg.Done()
	result, ch, stop, err := meth.call(ctx, args)
	c.mu.Lock()
	delete(c.requests, id)
	c.mu.Unlock()
	if err != nil || !ch.IsValid() {
		cancel()
		c.release() // before replying, so the client may reuse the slot at once
		if err != nil {
			c.replyError(rawID, c.encodeError(err))
		} else {
			c.replyResult(rawID, result)
		}
		return
	}
	defer c.release()
	s := &serverStream{cancel: func() {
		cancel()
		if stop != nil {
			stop()
		}
	}}
	c.mu.Lock()
	live := c.streams != nil
	var sid uint64
	if live {
		c.nextStream++
		sid = c.nextStream
		c.streams[sid] = s
	}
	c.mu.Unlock()
	if live {
		// Queued before the pump starts, so the response precedes every event.
		c.replyResult(rawID, map[string]uint64{"stream": sid})
	} else {
		s.stop()
	}
	for {
		v, ok := ch.Recv()
		if !ok {
			break
		}
		if ctx.Err() != nil {
			continue // cancelled: drain and discard so the target's pump can exit
		}
		ev, err := json.Marshal(v.Interface())
		if err == nil {
			c.notify("$/stream", sid, json.RawMessage(ev))
			continue
		}
		if c.endStream(sid) {
			c.notify("$/stream.end", sid, c.encodeError(err))
		}
		s.stop()
	}
	if ctx.Err() == nil && c.endStream(sid) {
		c.notify("$/stream.end", sid)
	}
	s.stop()
}

// endStream unregisters a live stream; false if it was already gone.
func (c *conn) endStream(sid uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.streams[sid]
	delete(c.streams, sid)
	return ok
}

func (c *conn) encodeError(err error) *WireError {
	if we, ok := err.(*WireError); ok {
		return we
	}
	return c.opts.EncodeError(err)
}

func (c *conn) replyResult(id json.RawMessage, result any) {
	b, err := json.Marshal(result)
	if err != nil {
		c.replyError(id, c.encodeError(err))
		return
	}
	c.send(message{JSONRPC: "2.0", ID: id, Result: b})
}

func (c *conn) replyError(id json.RawMessage, we *WireError) {
	c.send(message{JSONRPC: "2.0", ID: id, Error: we})
}

func (c *conn) notify(method string, params ...any) {
	b, _ := json.Marshal(params) // params are pre-encoded or plain
	c.send(message{JSONRPC: "2.0", Method: method, Params: b})
}

// send queues a message; a full queue drops the connection.
func (c *conn) send(m message) {
	b, err := json.Marshal(m)
	if err != nil {
		c.kill(err)
		return
	}
	b = append(b, '\n')
	c.qmu.Lock()
	if c.qdone {
		c.qmu.Unlock()
		return
	}
	if len(c.queue) >= c.opts.QueueMessages || c.qbytes+len(b) > c.opts.QueueBytes {
		c.qdone = true
		c.qmu.Unlock()
		c.kill(errSlow)
		return
	}
	c.queue = append(c.queue, b)
	c.qbytes += len(b)
	c.qmu.Unlock()
	c.signal()
}

func (c *conn) closeAfterFlush() {
	c.qmu.Lock()
	if !c.qdone {
		c.queue = append(c.queue, nil)
		c.qdone = true
	}
	c.qmu.Unlock()
	c.signal()
}

func (c *conn) signal() {
	select {
	case c.qready <- struct{}{}:
	default:
	}
}

func (c *conn) writeLoop() {
	defer c.wg.Done()
	dl, _ := c.rwc.(interface{ SetWriteDeadline(time.Time) error })
	for {
		c.qmu.Lock()
		if len(c.queue) == 0 {
			c.qmu.Unlock()
			select {
			case <-c.qready:
				continue
			case <-c.ctx.Done():
				return
			}
		}
		b := c.queue[0]
		c.queue[0] = nil
		c.queue = c.queue[1:]
		c.qmu.Unlock()
		if b == nil {
			c.kill(errAuth)
			return
		}
		if dl != nil {
			dl.SetWriteDeadline(time.Now().Add(c.opts.WriteTimeout))
		}
		if _, err := c.rwc.Write(b); err != nil {
			c.kill(err)
			return
		}
		c.qmu.Lock()
		c.qbytes -= len(b)
		c.qmu.Unlock()
	}
}

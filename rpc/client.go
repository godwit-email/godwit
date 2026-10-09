package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
)

// StreamBuffer is how many unread events a client stream holds before the
// client cancels it.
const StreamBuffer = 256

var (
	// ErrClosed ends calls and streams when the connection is gone.
	ErrClosed = errors.New("rpc: connection closed")
	// ErrStreamOverflow ends a stream whose reader fell StreamBuffer behind.
	ErrStreamOverflow = errors.New("rpc: stream reader too slow")
)

// Client is a JSON-RPC client. One read loop handles messages in order, so a
// stream is registered before any of its events are dispatched.
type Client struct {
	// DecodeError maps a wire error back to a Go error; the default returns
	// the *WireError itself.
	DecodeError func(*WireError) error

	conn io.ReadWriteCloser
	wmu  sync.Mutex

	mu      sync.Mutex
	closed  bool
	nextID  uint64
	pending map[uint64]*pending
	streams map[uint64]*clientStream
	errs    map[<-chan json.RawMessage]error
}

type pending struct {
	ch        chan message // closed if the connection drops
	stream    bool
	abandoned bool // caller gave up on a stream call; cancel the stream on arrival
	s         *clientStream
}

type clientStream struct {
	id uint64
	ch chan json.RawMessage
}

// NewClient starts reading conn.
func NewClient(conn io.ReadWriteCloser) *Client {
	c := &Client{
		conn:    conn,
		pending: map[uint64]*pending{},
		streams: map[uint64]*clientStream{},
		errs:    map[<-chan json.RawMessage]error{},
	}
	go c.readLoop()
	return c
}

// Close closes the connection; pending calls and streams end with ErrClosed.
func (c *Client) Close() error { return c.conn.Close() }

// Call invokes method and decodes its result into result (nil discards it;
// a pointer to a slice receives multiple results). Cancelling ctx sends
// $/cancel and returns ctx.Err().
func (c *Client) Call(ctx context.Context, method string, result any, args ...any) error {
	m, _, err := c.request(ctx, method, args, false)
	if err != nil || result == nil {
		return err
	}
	return json.Unmarshal(m.Result, result)
}

// Stream invokes a stream method. events closes when the stream ends, the
// caller cancels, ctx ends, or the caller falls StreamBuffer events behind;
// StreamErr then says why. cancel is idempotent and releases the stream.
func (c *Client) Stream(ctx context.Context, method string, args ...any) (events <-chan json.RawMessage, cancel func(), err error) {
	_, s, err := c.request(ctx, method, args, true)
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	release := func() { c.cancelStream(s) }
	stopAF := context.AfterFunc(ctx, func() { once.Do(release) })
	return s.ch, func() { once.Do(func() { stopAF(); release() }) }, nil
}

// StreamErr reports why events closed: nil for a normal end or a cancel by
// the caller. Ask before calling cancel, which forgets it.
func (c *Client) StreamErr(events <-chan json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.errs[events]
	delete(c.errs, events)
	return err
}

func (c *Client) request(ctx context.Context, method string, args []any, stream bool) (message, *clientStream, error) {
	if args == nil {
		args = []any{}
	}
	params, err := json.Marshal(args)
	if err != nil {
		return message{}, nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return message{}, nil, ErrClosed
	}
	c.nextID++
	id := c.nextID
	p := &pending{ch: make(chan message, 1), stream: stream}
	c.pending[id] = p
	c.mu.Unlock()
	if err := c.write(message{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatUint(id, 10)), Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return message{}, nil, err
	}
	select {
	case m, ok := <-p.ch:
		if !ok {
			return message{}, nil, ErrClosed
		}
		if m.Error != nil {
			return message{}, nil, c.decode(m.Error)
		}
		if stream && p.s == nil {
			return message{}, nil, errors.New("rpc: " + method + " did not return a stream")
		}
		return m, p.s, nil
	case <-ctx.Done():
		c.mu.Lock()
		var s *clientStream
		if c.pending[id] == p {
			if stream {
				p.abandoned = true
			} else {
				delete(c.pending, id)
			}
		} else {
			s = p.s // the stream arrived just now
		}
		c.mu.Unlock()
		c.notify("$/cancel", id)
		if s != nil {
			c.cancelStream(s)
		}
		return message{}, nil, ctx.Err()
	}
}

func (c *Client) decode(we *WireError) error {
	if c.DecodeError != nil {
		return c.DecodeError(we)
	}
	return we
}

func (c *Client) write(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.conn.Write(append(b, '\n'))
	return err
}

func (c *Client) notify(method string, params ...any) {
	b, _ := json.Marshal(params)
	c.write(message{JSONRPC: "2.0", Method: method, Params: b})
}

// cancelStream ends s locally and tells the server, once.
func (c *Client) cancelStream(s *clientStream) {
	c.mu.Lock()
	live := c.streams[s.id] == s
	if live {
		delete(c.streams, s.id)
		close(s.ch)
	}
	delete(c.errs, s.ch)
	c.mu.Unlock()
	if live {
		c.notify("$/stream.cancel", s.id)
	}
}

// endLocked closes a stream with err (nil = normal end). Caller holds c.mu.
func (c *Client) endLocked(s *clientStream, err error) {
	delete(c.streams, s.id)
	close(s.ch)
	if err != nil {
		c.errs[s.ch] = err
	}
}

func (c *Client) readLoop() {
	r := bufio.NewReader(c.conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if m.Method != "" {
			c.handleNotification(m)
		} else if id, ok := parseID(m.ID); ok {
			c.handleResponse(id, m)
		}
	}
	c.conn.Close()
	c.mu.Lock()
	c.closed = true
	for id, p := range c.pending {
		delete(c.pending, id)
		close(p.ch)
	}
	for _, s := range c.streams {
		c.endLocked(s, ErrClosed)
	}
	c.mu.Unlock()
}

func (c *Client) handleResponse(id uint64, m message) {
	c.mu.Lock()
	p := c.pending[id]
	delete(c.pending, id)
	var sid uint64
	if p != nil && p.stream && m.Error == nil {
		var r struct {
			Stream uint64 `json:"stream"`
		}
		if json.Unmarshal(m.Result, &r) == nil && r.Stream > 0 {
			sid = r.Stream
			if !p.abandoned {
				p.s = &clientStream{id: sid, ch: make(chan json.RawMessage, StreamBuffer)}
				c.streams[sid] = p.s
			}
		}
	}
	c.mu.Unlock()
	if p == nil {
		return
	}
	if p.abandoned {
		if sid > 0 {
			go c.notify("$/stream.cancel", sid)
		}
		return
	}
	p.ch <- m
}

func (c *Client) handleNotification(m message) {
	var params []json.RawMessage
	var sid uint64
	if json.Unmarshal(m.Params, &params) != nil || len(params) < 1 || json.Unmarshal(params[0], &sid) != nil {
		return
	}
	c.mu.Lock()
	s := c.streams[sid]
	if s == nil {
		c.mu.Unlock()
		return
	}
	overflow := false
	switch m.Method {
	case "$/stream":
		if len(params) == 2 {
			select {
			case s.ch <- params[1]:
			default:
				c.endLocked(s, ErrStreamOverflow)
				overflow = true
			}
		}
	case "$/stream.end":
		var err error
		if len(params) == 2 {
			var we WireError
			if json.Unmarshal(params[1], &we) == nil {
				err = c.decode(&we)
			} else {
				err = errors.New("rpc: stream ended with an undecodable error")
			}
		}
		c.endLocked(s, err)
	}
	c.mu.Unlock()
	if overflow {
		go c.notify("$/stream.cancel", sid) // never block the read loop on a write
	}
}

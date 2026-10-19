package plugins

// NativeProcess is the Godwit side of a native plugin (spec 4.5): one
// `godwit plugin-host` child per plugin, JSON lines on its stdio. A call past
// its deadline kills the process; a process that died starts again on the
// next call.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	nativeInFlight = 4
	nativeTimeout  = 60 * time.Second
	nativeGrace    = 2 * time.Second
	nativeStderr   = 4 << 10 // the tail of stderr an exit error carries
)

var (
	// ErrNativeExited: the plugin's process ended (a crash, or it never loaded).
	ErrNativeExited = errors.New("plugin: native plugin exited")
	// ErrNativeABI: the call failed below the envelope.
	ErrNativeABI = errors.New("plugin: native call failed")
)

// NativeError is a native plugin's own error envelope: its answer, not a failure.
type NativeError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e *NativeError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// IsNativeFailure reports whether err means the plugin misbehaved (exited,
// broke the ABI or timed out), as opposed to answering with an error.
func IsNativeFailure(err error) bool {
	return errors.Is(err, ErrNativeExited) || errors.Is(err, ErrNativeABI) || errors.Is(err, ErrTimeout)
}

// NativeOptions start a plugin's process: Command then --library Library
// --sha256 SHA256, in the environment Env alone. Timeout 0 is 60 s; Redact
// nil leaves error text as it is. OnStart, if set, runs once per new
// process before any call reaches it (calls made meanwhile wait); its error
// fails the call that started the process, which is then ended. Idle, if
// set, ends a process no call has used for that long; the next call starts
// a new one.
type NativeOptions struct {
	Command         []string
	Env             []string
	Library, SHA256 string
	Timeout         time.Duration
	Redact          func(string) string
	OnStart         func(ctx context.Context, call func(ctx context.Context, method string, params any) (json.RawMessage, error)) error
	Idle            time.Duration
	OnIdle          func() // the process was told to exit for being idle; no call or start runs meanwhile
}

type NativeProcess struct {
	o        NativeOptions
	sem      chan struct{}
	mu       sync.Mutex
	p        *nativeProc
	next     int64
	closed   bool
	inflight int         // calls (and Starts) under way
	idle     *time.Timer // ends p once no call has used it for o.Idle
}

type nativeLine struct {
	ID        int64           `json:"id"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Error     *NativeError    `json:"error"`
	HostError string          `json:"host_error"`
}

type nativeProc struct {
	cmd     *exec.Cmd
	kill    func() // ends the process and whatever it started
	stdin   io.WriteCloser
	dead    chan struct{}
	mu      sync.Mutex // pending, and writes to stdin
	pending map[int64]chan nativeLine
	stderr  *stderrTail

	ready    chan struct{} // closed once OnStart is done
	readyErr error         // OnStart's error, set before ready closes
	closing  bool          // stdin closed for being idle: it takes no more calls (n.mu)
}

// NewNativeProcess returns the plugin's process handle; nothing starts until the first Call.
func NewNativeProcess(o NativeOptions) *NativeProcess {
	if o.Timeout == 0 {
		o.Timeout = nativeTimeout
	}
	if o.Redact == nil {
		o.Redact = func(s string) string { return s }
	}
	if o.Env == nil {
		o.Env = []string{"PATH=" + os.Getenv("PATH")}
	}
	return &NativeProcess{o: o, sem: make(chan struct{}, nativeInFlight)}
}

// proc returns the running process, starting one if there is none; fresh
// reports that this call started it, and must run OnStart.
func (n *NativeProcess) proc() (p *nativeProc, fresh bool, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, false, ErrClosed
	}
	if p := n.p; p != nil && !p.closing {
		select {
		case <-p.dead:
		default:
			return p, false, nil
		}
	}
	args := append(append([]string{}, n.o.Command[1:]...), "--library", n.o.Library, "--sha256", n.o.SHA256)
	cmd := exec.Command(n.o.Command[0], args...)
	cmd.Env = n.o.Env
	setNativeGroup(cmd)
	p = &nativeProc{cmd: cmd, dead: make(chan struct{}), pending: map[int64]chan nativeLine{}, stderr: &stderrTail{}, ready: make(chan struct{})}
	cmd.Stderr = p.stderr
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	p.kill = nativeKiller(cmd)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), maxNativeResponse+64<<10)
		for sc.Scan() {
			var l nativeLine
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				continue
			}
			p.mu.Lock()
			ch := p.pending[l.ID]
			delete(p.pending, l.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- l
			}
		}
		cmd.Wait()
		p.kill() // also after a clean exit: helpers it started go, and on Windows its job is released
		close(p.dead)
	}()
	n.p = p
	return p, true, nil
}

// ready returns a process that finished OnStart, starting one if needed.
func (n *NativeProcess) ready(ctx context.Context) (*nativeProc, error) {
	p, fresh, err := n.proc()
	if err != nil {
		return nil, err
	}
	if fresh {
		if n.o.OnStart != nil {
			p.readyErr = n.o.OnStart(ctx, func(ctx context.Context, method string, params any) (json.RawMessage, error) {
				res, _, err := n.send(ctx, p, method, params, nil)
				return res, err
			})
		}
		close(p.ready)
		if p.readyErr != nil {
			p.kill()
			<-p.dead
			return nil, p.readyErr
		}
		return p, nil
	}
	select {
	case <-p.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.readyErr != nil {
		return nil, p.readyErr
	}
	return p, nil
}

// busy and done bracket a call for the idle timer.
func (n *NativeProcess) busy() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.inflight++
	if n.idle != nil {
		n.idle.Stop()
	}
}

func (n *NativeProcess) done() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.inflight--; n.inflight > 0 || n.o.Idle == 0 || n.p == nil || n.closed {
		return
	}
	p := n.p
	n.idle = time.AfterFunc(n.o.Idle, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.p != p || n.inflight > 0 || p.closing {
			return
		}
		p.closing = true // it exits on its own once stdin closes
		p.mu.Lock()
		p.stdin.Close()
		p.mu.Unlock()
		if n.o.OnIdle != nil {
			n.o.OnIdle() // under n.mu: a new process cannot start before it returns
		}
	})
}

// Start makes sure the process runs and finished OnStart.
func (n *NativeProcess) Start(ctx context.Context) error {
	n.busy()
	defer n.done()
	_, err := n.ready(ctx)
	return err
}

// Call runs method with params (marshalled to JSON) and returns the result.
// A plugin's error envelope is a *NativeError; see IsNativeFailure for the rest.
func (n *NativeProcess) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	req, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	select {
	case n.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	n.busy()
	held := true
	release := func() { n.done(); <-n.sem }
	defer func() {
		if held {
			release()
		}
	}()
	p, err := n.ready(ctx)
	if err != nil {
		return nil, err
	}
	res, detached, err := n.send(ctx, p, method, json.RawMessage(req), release)
	held = !detached // an abandoned call keeps its slot until the plugin is done with it
	return res, err
}

// send runs one call on p (OnStart's calls come here directly). When ctx
// ends first and detach is set, the call is left to finish in the plugin:
// send returns at once with detached true, and detach runs once the plugin
// answers, exits or runs out of time.
func (n *NativeProcess) send(ctx context.Context, p *nativeProc, method string, params any, detach func()) (_ json.RawMessage, detached bool, _ error) {
	req, err := json.Marshal(params)
	if err != nil {
		return nil, false, err
	}
	n.mu.Lock()
	n.next++
	id := n.next
	n.mu.Unlock()
	ch := make(chan nativeLine, 1)
	line, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(req)})
	p.mu.Lock()
	p.pending[id] = ch
	_, werr := p.stdin.Write(append(line, '\n'))
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}
	if werr != nil { // it stopped reading: whether or not it exits by itself, it is done
		forget()
		p.kill()
		<-p.dead
		return nil, false, n.exited(p)
	}
	t := time.NewTimer(n.o.Timeout)
	select {
	case l := <-ch:
		t.Stop()
		forget()
		res, err := n.answer(l)
		return res, false, err
	case <-p.dead:
		t.Stop()
		forget()
		select {
		case l := <-ch: // answered just before it exited
			res, err := n.answer(l)
			return res, false, err
		default:
			return nil, false, n.exited(p)
		}
	case <-t.C:
		forget()
		p.kill()
		<-p.dead
		return nil, false, fmt.Errorf("%w (%s after %s)", ErrTimeout, method, n.o.Timeout)
	case <-ctx.Done():
		if detach == nil {
			t.Stop()
			forget()
			return nil, false, ctx.Err()
		}
		go func() { // nothing to cancel across the ABI: the call runs on in the plugin
			defer detach()
			defer forget()
			defer t.Stop()
			select {
			case <-ch:
			case <-p.dead:
			case <-t.C:
				p.kill()
				<-p.dead
			}
		}()
		return nil, true, ctx.Err()
	}
}

func (n *NativeProcess) answer(l nativeLine) (json.RawMessage, error) {
	switch {
	case l.HostError != "":
		return nil, fmt.Errorf("%w: %s", ErrNativeABI, n.o.Redact(l.HostError))
	case !l.OK:
		if l.Error == nil {
			return nil, fmt.Errorf("%w: an error envelope without an error", ErrNativeABI)
		}
		e := *l.Error
		e.Message = n.o.Redact(e.Message)
		return nil, &e
	}
	return l.Result, nil
}

func (n *NativeProcess) exited(p *nativeProc) error {
	msg := strings.TrimSpace(p.stderr.String())
	if msg == "" {
		return fmt.Errorf("%w (%s)", ErrNativeExited, p.cmd.ProcessState)
	}
	return fmt.Errorf("%w: %s", ErrNativeExited, n.o.Redact(msg))
}

// Close ends the process: stdin first, then after nativeGrace the process
// group. Calls after it return ErrClosed.
func (n *NativeProcess) Close() {
	n.mu.Lock()
	p := n.p
	n.closed = true
	if n.idle != nil {
		n.idle.Stop()
	}
	n.mu.Unlock()
	if p == nil {
		return
	}
	p.stdin.Close()
	select {
	case <-p.dead:
	case <-time.After(nativeGrace):
		p.kill()
		<-p.dead
	}
}

// stderrTail keeps the last nativeStderr bytes written to it.
type stderrTail struct {
	mu sync.Mutex
	b  []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > nativeStderr {
		t.b = t.b[len(t.b)-nativeStderr:]
	}
	return len(p), nil
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

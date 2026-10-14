package laya

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// startTimeout bounds the worker's start: the graph maps in a fraction of a
// second when its pages are cached, a few seconds from a cold disk.
const startTimeout = 30 * time.Second

// maxStderr is how much of the worker's stderr an error keeps.
const maxStderr = 4 << 10

// ErrWorker is a worker that failed to start or died.
var ErrWorker = errors.New("laya: worker failed")

// Client runs questions in a worker it starts with Command, one at a time,
// and stops it once it has been idle for Idle (0 stops it after every Run).
// Its zero value with Command set is ready; a Client must not be copied.
type Client struct {
	Command []string // the worker: an executable and its arguments, which run Serve
	Idle    time.Duration

	once  sync.Once
	sem   chan struct{} // held by Run, Close and the idle stop
	w     *worker       // nil when none runs; guarded by sem
	timer *time.Timer
	gen   int // bumped by every Run, so a stale idle stop does nothing
}

type worker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	dec    *json.Decoder
	stderr *tailBuffer
	exited chan struct{}
}

// lock takes c's semaphore, or fails with ctx.
func (c *Client) lock(ctx context.Context) error {
	c.once.Do(func() { c.sem = make(chan struct{}, 1) })
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) unlock() { <-c.sem }

// Run answers in. Cancelling ctx while the worker runs it kills the worker:
// ONNX Runtime has no cheaper way to stop a run, and the next Run starts a
// new one. Errors of a worker that failed wrap ErrWorker.
func (c *Client) Run(ctx context.Context, in Input) (Output, error) {
	if err := in.Validate(); err != nil {
		return Output{}, err
	}
	if err := c.lock(ctx); err != nil {
		return Output{}, err
	}
	defer c.unlock()
	c.gen++
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.w != nil && c.w.done() { // died while idle
		c.w = nil
	}
	if c.w == nil {
		w, err := c.start(ctx)
		if err != nil {
			return Output{}, err
		}
		c.w = w
	}
	out, err := c.ask(ctx, in)
	if c.w != nil {
		gen := c.gen
		c.timer = time.AfterFunc(c.Idle, func() { c.stopIdle(gen) })
	}
	return out, err
}

// Close stops the worker, after a Run in progress.
func (c *Client) Close() error {
	c.lock(context.Background())
	defer c.unlock()
	c.gen++
	if c.timer != nil {
		c.timer.Stop()
	}
	c.stop()
	return nil
}

// stopIdle stops the worker if no Run came after the one of gen.
func (c *Client) stopIdle(gen int) {
	c.lock(context.Background())
	defer c.unlock()
	if c.gen == gen {
		c.stop()
	}
}

// ask sends in to the running worker and reads its answer.
func (c *Client) ask(ctx context.Context, in Input) (Output, error) {
	w := c.w
	type result struct {
		resp response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		if r.err = json.NewEncoder(w.stdin).Encode(in); r.err == nil {
			r.err = w.dec.Decode(&r.resp)
		}
		done <- r
	}()
	select {
	case r := <-done:
		switch {
		case r.err != nil:
			c.kill()
			return Output{}, w.died(r.err)
		case r.resp.Error != "":
			return Output{}, errors.New(r.resp.Error)
		case r.resp.Output == nil || len(r.resp.Output.Logits) != len(in.MarkerPos):
			c.kill()
			return Output{}, fmt.Errorf("%w: a malformed answer", ErrWorker)
		}
		return *r.resp.Output, nil
	case <-ctx.Done():
		c.kill()
		<-done // the killed worker's pipes are closed: it returns
		return Output{}, ctx.Err()
	}
}

// start starts the worker and waits for its hello.
func (c *Client) start(ctx context.Context) (*worker, error) {
	if len(c.Command) == 0 {
		return nil, errors.New("laya: no worker command")
	}
	cmd := exec.Command(c.Command[0], c.Command[1:]...)
	w := &worker{cmd: cmd, stderr: &tailBuffer{max: maxStderr}, exited: make(chan struct{})}
	cmd.Stderr = w.stderr
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorker, err)
	}
	go func() { cmd.Wait(); close(w.exited) }()
	w.stdin, w.dec = stdin, json.NewDecoder(stdout)

	helloc := make(chan error, 1)
	go func() {
		var h hello
		err := w.dec.Decode(&h)
		switch {
		case err != nil:
		case h.Error != "":
			err = errors.New(h.Error)
		case !h.Ready:
			err = errors.New("no hello")
		}
		helloc <- err
	}()
	hctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	select {
	case err := <-helloc:
		if err == nil {
			return w, nil
		}
		w.kill()
		return nil, w.died(err)
	case <-hctx.Done():
		w.kill()
		<-helloc
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: no hello in %s", ErrWorker, startTimeout)
	}
}

// stop ends the running worker as it ends itself: stdin closed. One that
// does not exit within a second is killed.
func (c *Client) stop() {
	w := c.w
	if w == nil {
		return
	}
	c.w = nil
	w.stdin.Close()
	select {
	case <-w.exited:
	case <-time.After(time.Second):
		w.kill()
	}
}

func (c *Client) kill() {
	if c.w != nil {
		c.w.kill()
		c.w = nil
	}
}

// kill kills the worker and reaps it.
func (w *worker) kill() {
	w.cmd.Process.Kill()
	<-w.exited
}

func (w *worker) done() bool {
	select {
	case <-w.exited:
		return true
	default:
		return false
	}
}

// died is err of a worker that ended, with the end of its stderr.
func (w *worker) died(err error) error {
	if s := strings.TrimSpace(w.stderr.String()); s != "" {
		return fmt.Errorf("%w: %v: %s", ErrWorker, err, s)
	}
	return fmt.Errorf("%w: %v", ErrWorker, err)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = bytes.Clone(t.b[over:])
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

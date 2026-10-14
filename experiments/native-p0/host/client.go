package main

// client: what core/plugins/native.go would be. One subprocess per plugin,
// JSON lines, at most maxInFlight calls, a timeout kills the process group,
// a dead process restarts on the next call.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	errExited  = errors.New("plugin exited")
	errTimeout = errors.New("plugin call timed out")
)

type envelope struct {
	ID        int64           `json:"id"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Error     *struct{ Code, Message string } `json:"error"`
	HostError string          `json:"host_error"`
}

type proc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	dead    chan struct{}
	mu      sync.Mutex
	pending map[int64]chan envelope
	stderr  *tail
}

type client struct {
	lib, sha string
	abi      uint
	env      []string
	sem      chan struct{}
	mu       sync.Mutex
	next     int64
	p        *proc
	starts   int
}

type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tail) String() string { t.mu.Lock(); defer t.mu.Unlock(); return string(t.b) }

func newClient(lib, sha string, maxInFlight int) *client {
	return &client{lib: lib, sha: sha, abi: 1, sem: make(chan struct{}, maxInFlight), env: []string{"PATH=" + os.Getenv("PATH")}}
}

func (c *client) proc() (*proc, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p != nil {
		select {
		case <-c.p.dead:
		default:
			return c.p, nil
		}
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "plugin-host", "--library", c.lib, "--sha256", c.sha, "--abi", fmt.Sprint(c.abi))
	cmd.Env = c.env
	setGroup(cmd)
	p := &proc{cmd: cmd, dead: make(chan struct{}), pending: map[int64]chan envelope{}, stderr: &tail{}}
	cmd.Stderr = p.stderr
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c.starts++
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 5<<20)
		for sc.Scan() {
			var e envelope
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			p.mu.Lock()
			ch := p.pending[e.ID]
			delete(p.pending, e.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- e
			}
		}
		cmd.Wait()
		close(p.dead)
	}()
	c.p = p
	return p, nil
}

func (c *client) Call(ctx context.Context, method string, params any, timeout time.Duration) (envelope, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return envelope{}, ctx.Err()
	}
	defer func() { <-c.sem }()
	p, err := c.proc()
	if err != nil {
		return envelope{}, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return envelope{}, err
	}
	c.mu.Lock()
	c.next++
	id := c.next
	c.mu.Unlock()
	ch := make(chan envelope, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	line, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(raw)})
	p.mu.Lock()
	_, err = p.stdin.Write(append(line, '\n'))
	p.mu.Unlock()
	if err != nil {
		return envelope{}, errExited
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case e := <-ch:
		if e.HostError != "" {
			return e, errors.New("abi: " + e.HostError)
		}
		return e, nil
	case <-p.dead:
		return envelope{}, fmt.Errorf("%w: %s", errExited, p.stderr.String())
	case <-t.C:
		killGroup(p.cmd)
		<-p.dead
		return envelope{}, errTimeout
	case <-ctx.Done():
		return envelope{}, ctx.Err() // the call keeps running in the plugin; nothing to cancel across the ABI
	}
}

// Close: stdin first, then a grace period, then the group.
func (c *client) Close() {
	c.mu.Lock()
	p := c.p
	c.mu.Unlock()
	if p == nil {
		return
	}
	p.stdin.Close()
	select {
	case <-p.dead:
	case <-time.After(2 * time.Second):
		killGroup(p.cmd)
		<-p.dead
	}
}

func (c *client) pid() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return 0
	}
	return c.p.cmd.Process.Pid
}

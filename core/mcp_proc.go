package core

import (
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP stdio server processes.

// mcpProc is a stdio server's process, in its own process group; it is the
// transport's writer, so closing the connection closes it.
type mcpProc struct {
	stdin  *os.File
	stderr *tailBuffer
	kill   func() // the whole group
	exited chan struct{}
	once   sync.Once
}

func (p *mcpProc) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// Close closes stdin, gives the server mcpGrace to exit, then kills its
// process group (children it left too) and reaps it, waiting at most
// mcpGrace more (Wait itself gives up on the pipes after WaitDelay).
func (p *mcpProc) Close() error {
	p.once.Do(func() {
		p.stdin.Close()
		select {
		case <-p.exited:
		case <-time.After(mcpGrace):
		}
		p.kill()
		select {
		case <-p.exited:
		case <-time.After(mcpGrace):
		}
	})
	return nil
}

// startMCPProcess starts srv's command with only PATH and its Env in its
// environment. The user granted running it: it is not sandboxed.
func startMCPProcess(srv MCPServer, env map[string]string) (mcp.Transport, *mcpProc, error) {
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	p := &mcpProc{stderr: &tailBuffer{max: maxMCPStderr}, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	cmd.WaitDelay = mcpGrace // a child left holding stderr cannot hold Wait
	setProcessGroup(cmd)
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, nil, err
	}
	cmd.Stdin, cmd.Stdout, p.stdin = inR, outW, inW
	err = cmd.Start()
	inR.Close() // the child's ends
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, nil, err
	}
	p.kill = groupKiller(cmd)
	go func() { cmd.Wait(); close(p.exited) }()
	return &mcp.IOTransport{Reader: outR, Writer: p}, p, nil
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
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.b), "")
}

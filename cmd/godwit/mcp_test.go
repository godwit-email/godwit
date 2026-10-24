package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPNoInstance(t *testing.T) {
	var stdout, stderr syncBuf
	code := run(context.Background(), []string{"mcp", "--data-dir", shortDir(t)}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Godwit is not running, or its Local API is off") || stdout.String() != "" {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

// godwit mcp speaks MCP on its stdin and stdout and exits 0 when stdin closes.
func TestMCPOverStdio(t *testing.T) {
	dir := shortDir(t)
	startHeadless(t, dir)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &syncBuf{}
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), []string{"mcp", "--data-dir", dir}, inR, outW, stderr)
		outW.Close()
	}()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(res.Tools) != 13 {
		t.Fatalf("ListTools = %v, %v", res, err)
	}
	text, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_accounts"})
	if err != nil || text.IsError || text.Content[0].(*mcp.TextContent).Text != "no accounts" {
		t.Fatalf("list_accounts = %+v, %v", text, err)
	}
	cs.Close() // closes stdin
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d, stderr %q", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("godwit mcp did not exit when stdin closed")
	}
}

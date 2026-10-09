package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/mcpserver"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpCmd runs `godwit mcp [--data-dir DIR]`: an MCP server on stdin and
// stdout, bridged to the running Godwit as the mcp role, until stdin closes
// or ctx ends. Without a running Godwit it exits 2.
func mcpCmd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("godwit mcp", stderr)
	dataDir := fs.String("data-dir", "", "")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	dir, err := dataDirOr(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	c, err := rpc.Dial(dir, rpc.RoleMCP, "godwit-mcp/"+app.Version, rpcbind.NewCodec())
	if err != nil {
		fmt.Fprintf(stderr, "godwit mcp: Godwit is not running, or its Local API is off (%v)\n", err)
		return 2
	}
	defer c.Close()
	err = mcpserver.New(c, app.Version).Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}})
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(stderr, "godwit mcp:", err)
		return 1
	}
	return 0
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

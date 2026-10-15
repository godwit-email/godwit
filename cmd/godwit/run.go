package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core/plugins"
)

const usage = `usage:
  godwit [--data-dir DIR]                                the desktop app
  godwit --headless [--data-dir DIR]                     serve the Local API without a window
  godwit call [--data-dir DIR] METHOD [JSON_ARGS_ARRAY]  call the running Godwit once
  godwit mcp [--data-dir DIR]                            an MCP server on stdio for agents: read mail, write drafts
  godwit --version
`

// run is the whole command: it returns the exit code, and ends what it
// started when ctx is cancelled. Usage errors exit 2.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "call" {
		return call(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "mcp" {
		return mcpCmd(ctx, args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "plugin-host" { // a native plugin's process (core/plugins/native_host.go)
		return plugins.ServeNative(args[1:], stdin, stdout, stderr)
	}
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return strings.HasPrefix(a, "-psn_") }) // from macOS LaunchServices
	fs := newFlagSet("godwit", stderr)
	isHeadless := fs.Bool("headless", false, "")
	version := fs.Bool("version", false, "")
	dataDir := fs.String("data-dir", "", "")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "godwit: unknown command %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, "godwit", app.Version)
		return 0
	}
	dir, err := dataDirOr(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	if *isHeadless {
		return headless(ctx, dir, stderr)
	}
	return gui(ctx, dir)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

// parse parses args; when it fails, code is the exit code (0 for -h).
func parse(fs *flag.FlagSet, args []string) (code int, ok bool) {
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0, false
	}
	return 2, err == nil
}

// dataDirOr is dir made absolute and clean (a later change of working
// directory, or a copied MCP config, must not move it), or defaultDataDir
// if dir is empty.
func dataDirOr(dir string) (string, error) {
	if dir != "" {
		return filepath.Abs(dir)
	}
	return defaultDataDir()
}

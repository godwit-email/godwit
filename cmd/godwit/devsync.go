package main

import (
	"context"
	"fmt"
	"io"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
)

func devSyncDB(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("dev-sync-db", stderr)
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if !mygo.IsDev() {
		fmt.Fprintln(stderr, "godwit: dev-sync-db requires a development build; use mise run dev:sync-db")
		return 2
	}
	src, err := productionDataDir()
	if err == nil {
		err = core.SyncDevDatabase(ctx, src, src+"-dev")
	}
	if err != nil {
		fmt.Fprintln(stderr, "godwit: dev-sync-db:", err)
		return 1
	}
	fmt.Fprintln(stdout, "Copied production database to", src+"-dev", "(dev credentials must be entered again)")
	return 0
}

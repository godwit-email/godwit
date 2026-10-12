// Command godwit is the Godwit desktop app, its headless server and the
// command-line client of its Local API (see run).
//
// Release builds take the version Settings › About and --version show from
// mygo.json, which `mygo build` packages with the app.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
)

// main turns SIGINT and SIGTERM (Ctrl-C on Windows) into cancelling run's ctx.
func main() {
	if v := mygo.App.Version(); v != "" {
		app.Version = v
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

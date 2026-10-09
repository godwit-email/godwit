// Command godwit is the Godwit desktop app, its headless server and the
// command-line client of its Local API (see run).
//
// Release builds set the version Settings › About and --version show:
//
//	go build -ldflags "-X github.com/godwit-email/godwit/app.Version=1.2.3" ./cmd/godwit
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// main turns SIGINT and SIGTERM (Ctrl-C on Windows) into cancelling run's ctx.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

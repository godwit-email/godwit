package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpcbind"
)

// anotherOwner is what a second owner of a data directory is told.
const anotherOwner = "Godwit 已在运行（GUI 或 headless）"

// headless owns dataDir and serves the Local API until ctx ends, then closes
// the server, the Service and the lock, in that order. Logs go to stderr
// through Service.Redact.
func headless(ctx context.Context, dataDir string, stderr io.Writer) int {
	release, err := ownerlock.Acquire(dataDir)
	if errors.Is(err, ownerlock.ErrAnotherOwner) {
		fmt.Fprintln(stderr, anotherOwner)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	defer release()
	svc, err := core.New(ctx, core.Options{DataDir: dataDir, AutoSync: true})
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	old := slog.Default() // the log package follows slog's default
	slog.SetDefault(slog.New(slog.NewTextHandler(redactWriter{stderr, svc.Redact}, nil)))
	defer func() {
		svc.Close() // still logging through Redact
		slog.SetDefault(old)
	}()

	srv, err := rpcbind.Listen(dataDir, svc, app.Version)
	if err != nil {
		slog.Error("listen", "err", err)
		return 1
	}
	slog.Info("serving the Local API", "socket", srv.Path())
	if err := srv.Serve(ctx); err != nil { // Serve closes srv once ctx ends
		srv.Close()
		slog.Error("serve", "err", err)
		return 1
	}
	return 0
}

// redactWriter writes each log line through redact.
type redactWriter struct {
	w      io.Writer
	redact func(string) string
}

func (r redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, r.redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

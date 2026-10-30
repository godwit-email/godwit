package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/syslang"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpcbind"
)

// headless owns dataDir and serves the Local API until ctx ends, then closes
// the server, the Service and the lock, in that order. Logs go to stderr
// through Service.Redact.
func headless(ctx context.Context, dataDir string, stderr io.Writer) int {
	release, err := ownerlock.Acquire(dataDir)
	if errors.Is(err, ownerlock.ErrAnotherOwner) {
		i18n.SetLanguage(syslang.Detect())
		fmt.Fprintln(stderr, i18n.T("app.another_owner"))
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	defer release()
	reg, err := registrySource()
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	svc, err := core.New(ctx, core.Options{DataDir: dataDir, AutoSync: true, LabelMail: true, Registry: reg, ModelsDev: ai.ModelsDevURL, AppVersion: app.Version,
		AppleIntelligence: appleIntelligence(), GuardCoreML: guardCoreML()}) // no SystemTranslator: Translation needs the AppKit main loop headless mode lacks
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	old := slog.Default() // the log package follows slog's default
	slog.SetDefault(slog.New(newLogHandler(stderr, svc.Redact)))
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

// newLogHandler is a text handler on w that redacts the message and every
// attribute (keys, string and other values, groups) before it formats them,
// so escaping cannot hide a secret from redact.
func newLogHandler(w io.Writer, redact func(string) string) slog.Handler {
	return redactHandler{slog.NewTextHandler(w, nil), redact}
}

type redactHandler struct {
	slog.Handler
	redact func(string) string
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { out.AddAttrs(h.attr(a)); return true })
	return h.Handler.Handle(ctx, out)
}

func (h redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = h.attr(a)
	}
	return redactHandler{h.Handler.WithAttrs(clean), h.redact}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h.Handler.WithGroup(h.redact(name)), h.redact}
}

func (h redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		g := v.Group()
		clean := make([]slog.Attr, len(g))
		for i, ga := range g {
			clean[i] = h.attr(ga)
		}
		v = slog.GroupValue(clean...)
	case slog.KindString, slog.KindAny:
		v = slog.StringValue(h.redact(v.String()))
	}
	return slog.Attr{Key: h.redact(a.Key), Value: v}
}

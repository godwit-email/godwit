package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/syslang"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpcbind"
)

// inMain is true only when main() entered on the process's main OS thread;
// in-process tests calling run on spawned goroutines stay off mygo.App.Run.
var inMain bool

type headlessDesktop struct {
	translator func() core.SystemTranslator
	openURL    func(string) error
	printPDF   func(context.Context, string) ([]byte, error)
	post       func(func())
	newSink    func(*core.Service) app.NotificationSink
	run        func(ctx context.Context, ready func(stop func())) (started bool, err error)
}

var chooseHeadlessDesktop = defaultHeadlessDesktop

func defaultHeadlessDesktop() *headlessDesktop {
	if !inMain || os.Getenv("GODWIT_HEADLESS_DESKTOP") == "0" || !hasGraphicalSession() {
		return nil
	}
	return mygoHeadlessDesktop()
}

func linuxHasDisplay(getenv func(string) string) bool {
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// mygoHeadlessDesktop runs the native event loop as an accessory process (no
// Dock or switcher entry, no single-instance lock, no persistent window) so
// notifications, system translation, link opening and temporary hidden PDF
// pages work as they do in the GUI.
func mygoHeadlessDesktop() *headlessDesktop {
	return &headlessDesktop{
		translator: systemTranslator,
		openURL:    mygo.Shell.OpenExternal,
		printPDF:   printPDF,
		post:       mygo.RunOnMain,
		newSink:    func(svc *core.Service) app.NotificationSink { return desktopNotifySink{svc: svc} },
		run: func(ctx context.Context, ready func(stop func())) (bool, error) {
			mygo.App.SetName("Godwit")
			mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
			offClosed := mygo.App.OnWindowAllClosed(func() {}) // temporary PDF windows close without quitting
			defer offClosed()
			defer context.AfterFunc(ctx, mygo.App.Quit)()
			started := false
			mygo.App.WhenReady(func() {
				started = true
				if ctx.Err() != nil {
					mygo.App.Quit()
					return
				}
				ready(mygo.App.Quit)
			})
			err := mygo.App.Run()
			return started, err
		},
	}
}

// headless owns dataDir and serves the Local API until ctx ends, then closes
// the server, the notifier, the Service and the lock, in that order. Logs go
// to stderr through Service.Redact.
func headless(ctx context.Context, dataDir string, stderr io.Writer) int {
	lang := syslang.Detect()
	release, err := ownerlock.Acquire(dataDir)
	if errors.Is(err, ownerlock.ErrAnotherOwner) {
		i18n.SetLanguage(lang)
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
	if d := chooseHeadlessDesktop(); d != nil && d.run != nil {
		var shutdown func() int
		code := 0
		started, err := d.run(ctx, func(stop func()) {
			shutdown, code = startHeadlessServer(ctx, dataDir, stderr, lang, reg, d, stop)
			if shutdown == nil {
				stop()
			}
		})
		if started {
			if shutdown != nil {
				code = shutdown()
			}
			if err != nil && code == 0 {
				fmt.Fprintln(stderr, "godwit:", err)
				return 1
			}
			return code
		}
		if ctx.Err() != nil {
			return 0
		}
	}
	waitCtx, stopWait := context.WithCancel(ctx)
	defer stopWait()
	shutdown, code := startHeadlessServer(ctx, dataDir, stderr, lang, reg, nil, stopWait)
	if shutdown == nil {
		return code
	}
	<-waitCtx.Done()
	return shutdown()
}

func startHeadlessServer(ctx context.Context, dataDir string, stderr io.Writer, lang string, reg *core.RegistrySource, d *headlessDesktop, onStop func()) (func() int, int) {
	opts := core.Options{
		DataDir:           dataDir,
		AutoSync:          true,
		LabelMail:         true,
		SecurityMail:      true,
		SystemLanguage:    lang,
		Registry:          reg,
		ModelsDev:         ai.ModelsDevURL,
		AppVersion:        app.Version,
		AppleIntelligence: appleIntelligence(),
		GuardCoreML:       guardCoreML(),
	}
	if d != nil {
		if d.translator != nil {
			opts.SystemTranslator = d.translator()
		}
		opts.OpenURL = d.openURL
		opts.PrintPDF = d.printPDF
	}
	svc, err := core.New(ctx, opts)
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return nil, 1
	}
	old := slog.Default() // the log package follows slog's default
	slog.SetDefault(slog.New(newLogHandler(logOutput(stderr), svc.Redact)))
	closeSvc := func() {
		svc.Close() // still logging through Redact
		slog.SetDefault(old)
	}

	srv, err := rpcbind.Listen(dataDir, svc, app.Version)
	if err != nil {
		slog.Error("listen", "err", err)
		closeSvc()
		return nil, 1
	}

	var sink app.NotificationSink = noNotifySink{}
	var post func(func())
	if d != nil {
		post = d.post
		if d.newSink != nil {
			sink = d.newSink(svc)
		}
	}
	notifier := app.NewMailNotifier(app.MailNotifierDeps{
		Svc:      svc,
		Sink:     sink,
		Language: func() string { return lang },
		Post:     post,
		Logf:     func(format string, args ...any) { slog.Warn(fmt.Sprintf(format, args...)) },
	})
	ch, unsub := svc.Subscribe()
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for ev := range ch {
			notifier.HandleEvent(ev)
		}
	}()

	slog.Info("serving the Local API", "socket", srv.Path())
	serveCtx, stopServe := context.WithCancel(ctx)
	serveDone := make(chan error, 1)
	go func() {
		err := srv.Serve(serveCtx) // Serve closes srv once serveCtx ends
		if err != nil {
			srv.Close()
		}
		serveDone <- err
		if onStop != nil {
			onStop()
		}
	}()

	return func() int {
		stopServe()
		err := <-serveDone
		unsub()
		<-pumpDone
		notifier.Close()
		if err != nil {
			slog.Error("serve", "err", err)
		}
		closeSvc()
		if err != nil {
			return 1
		}
		return 0
	}, 0
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

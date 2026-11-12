package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/syslang"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
)

// inMain is true only when main() entered on the process's main OS thread;
// in-process tests calling run on spawned goroutines stay off mygo.App.Run.
var inMain bool

type headlessPlatform interface {
	app.NotificationSink
	BindTray(trayActions)
	SetTrayVisible(visible bool)
	SetTrayQuitStop(on bool)
	SetTrayUnread(n int)
	SetSignInCodes(codes []core.SignInCode)
	ShowSignInCode(code *core.SignInCode)
	SetThemeSource(theme string)
	SetLanguage(uiLang, sysLang string)
}

type headlessDesktop struct {
	translator  func() core.SystemTranslator
	openURL     func(string) error
	printPDF    func(context.Context, string) ([]byte, error)
	post        func(func())
	clock       app.Clock
	newPlatform func(dataDir string, svc *core.Service) headlessPlatform
	run         func(ctx context.Context, ready func(stop func())) (started bool, err error)
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
// the tray, notifications, system translation, link opening and temporary
// hidden PDF pages work as they do in the GUI.
func mygoHeadlessDesktop() *headlessDesktop {
	var plat *platform
	var svc *core.Service // main thread, as the clicks
	var dataDir string
	return &headlessDesktop{
		translator: systemTranslator,
		openURL:    mygo.Shell.OpenExternal,
		printPDF:   printPDF,
		post:       mygo.RunOnMain,
		newPlatform: func(dir string, s *core.Service) headlessPlatform {
			svc, dataDir = s, dir
			plat = &platform{dataDir: dir, svc: s}
			return plat
		},
		run: func(ctx context.Context, ready func(stop func())) (bool, error) {
			mygo.App.SetName("Godwit")
			mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
			offClosed := mygo.App.OnWindowAllClosed(func() {}) // temporary PDF or banner windows close without quitting
			defer offClosed()
			offLocale := mygo.App.OnLocaleChanged(func(locale string) {
				i18n.SetLanguage(locale)
				if plat != nil && plat.updateTray != nil {
					plat.updateTray()
				}
			})
			defer offLocale()
			offClick := mygo.App.OnNotificationClick(func(id string) {
				if svc != nil {
					showUI(svc, dataDir, id)
				}
			})
			defer offClick()
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
// to stderr through Service.Redact. uiStarted: the interface started it, so
// external access follows Settings.LocalAPI and the interface's quit stops it
// (StopUIBackend); a user's --headless serves external clients always and
// outlives any interface. A failed start exits with the code the interface
// tells the user by (exitAnotherOwner, exitLockFailed, exitOpenDenied, else 1).
func headless(ctx context.Context, dataDir string, stderr io.Writer, uiStarted bool) int {
	lang := syslang.Detect()
	release, err := ownerlock.Acquire(dataDir)
	if errors.Is(err, ownerlock.ErrAnotherOwner) {
		i18n.SetLanguage(lang)
		fmt.Fprintln(stderr, i18n.T("app.another_owner"))
		return exitAnotherOwner
	}
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return exitLockFailed
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
			shutdown, code = startHeadlessServer(ctx, dataDir, stderr, lang, reg, d, stop, uiStarted)
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
	shutdown, code := startHeadlessServer(ctx, dataDir, stderr, lang, reg, nil, stopWait, uiStarted)
	if shutdown == nil {
		return code
	}
	<-waitCtx.Done()
	return shutdown()
}

func startHeadlessServer(ctx context.Context, dataDir string, stderr io.Writer, lang string, reg *core.RegistrySource, d *headlessDesktop, onStop func(), uiStarted bool) (func() int, int) {
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
	if uiStarted {
		opts.StopUIBackend = onStop
	}
	var notifyMCP func(id int64) bool // set with the platform, before the Local API serves
	if d != nil && d.newPlatform != nil {
		opts.NotifyMCPSend = func(id int64) bool { return notifyMCP != nil && notifyMCP(id) }
	}
	svc, err := core.New(ctx, opts)
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		if errors.Is(err, fs.ErrPermission) {
			return nil, exitOpenDenied
		}
		return nil, 1
	}
	old := slog.Default() // the log package follows slog's default
	slog.SetDefault(slog.New(newLogHandler(logOutput(stderr), svc.Redact)))
	closeSvc := func() {
		svc.Close() // still logging through Redact
		slog.SetDefault(old)
	}

	external := true
	if uiStarted {
		st, err := svc.GetSettings(ctx)
		external = err == nil && st.LocalAPI
	}
	srv, err := rpcbind.ListenWith(dataDir, svc, rpcbind.ListenOptions{Version: app.Version, External: external, UIStarted: uiStarted})
	if err != nil {
		slog.Error("listen", "err", err)
		closeSvc()
		return nil, 1
	}

	var sink app.NotificationSink = noNotifySink{}
	var post func(func())
	var clock app.Clock
	var tray *backendTray
	logWarn := func(format string, args ...any) { slog.Warn(fmt.Sprintf(format, args...)) }
	if d != nil {
		clock = d.clock
		post = d.post
		if post == nil {
			var postMu sync.Mutex
			post = func(fn func()) {
				postMu.Lock()
				defer postMu.Unlock()
				fn()
			}
		}
		if d.newPlatform != nil {
			if plat := d.newPlatform(dataDir, svc); plat != nil {
				sink = plat
				notifyMCP = func(id int64) bool { return notifyMCPSend(plat, id) }
				tray = newBackendTray(svc, plat, lang, post, clock, logWarn, func(what string) { showUI(svc, dataDir, what) },
					func() { quitFromTray(svc, onStop) })
			}
		}
	}
	notifier := app.NewMailNotifier(app.MailNotifierDeps{
		Svc:        svc,
		Sink:       sink,
		Clock:      clock,
		Foreground: svc.UIForeground,
		Language:   func() string { return lang },
		Post:       post,
		Logf:       logWarn,
	})
	ch, unsub := svc.Subscribe()
	if tray != nil {
		post(tray.Start)
	}
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for ev := range ch {
			if uiStarted && ev.Type == events.SettingsChanged {
				followLocalAPI(ctx, svc, srv)
			}
			notifier.HandleEvent(ev)
			if tray != nil {
				tray.HandleEvent(ev)
			}
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
		if tray != nil {
			tray.Close()
		}
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

// notifyMCPSend posts send request id's notification on sink, naming no mail
// content; its click opens the request's confirmation (app.App.Open).
func notifyMCPSend(sink app.NotificationSink, id int64) bool {
	if !sink.NotificationsSupported() {
		return false
	}
	err := sink.Notify(app.Notification{ID: "mcp_send:" + strconv.FormatInt(id, 10), Title: "Godwit", Body: i18n.McpSendWaiting()})
	if err != nil {
		slog.Warn("notify: MCP send", "err", err)
	}
	return err == nil
}

// followLocalAPI turns external access on srv on or off as Settings.LocalAPI
// now says (接入 AI 应用), for a backend the interface started.
func followLocalAPI(ctx context.Context, svc *core.Service, srv *rpc.Server) {
	st, err := svc.GetSettings(ctx)
	if err == nil {
		err = srv.SetExternal(st.LocalAPI)
	}
	if err != nil && ctx.Err() == nil {
		slog.Warn("local API: following the setting", "err", err)
	}
}

// quitWait bounds how long the tray's Quit waits for the interface to quit first.
const quitWait = 10 * time.Second

// quitFromTray is the tray's Quit: with an interface connected, it asks it to
// quit first (QuitRequested), so it saves what it holds, then stops once it has
// gone or quitWait passed; stop is the backend's usual close path.
func quitFromTray(svc *core.Service, stop func()) {
	if stop == nil {
		return
	}
	if !svc.UIConnected() {
		stop()
		return
	}
	svc.Publish(events.Event{Type: events.QuitRequested})
	go func() {
		for end := time.Now().Add(quitWait); svc.UIConnected() && time.Now().Before(end); {
			time.Sleep(50 * time.Millisecond)
		}
		stop()
	}()
}

// uiCmd is the interface the backend starts on dataDir to open what: this
// executable, not headless, outliving the backend (detachBackend). Tests swap it.
var uiCmd = func(dataDir, what string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "--data-dir", dataDir, "--open", what)
	detachBackend(cmd)
	return cmd, nil
}

// showUI opens what in the interface (app.App.Open: app.OpenWindow,
// app.OpenCompose or a notification id), for the tray's Open and Compose and
// a click on one of the backend's notifications: a connected interface is asked on its events, else
// one is started (uiCmd). The interface dedupes clicks, as macOS may hand one
// to it as well; a second interface started meanwhile hands its --open to the
// first (OnSecondInstance).
func showUI(svc *core.Service, dataDir, what string) {
	if svc.UIConnected() {
		ev := events.Event{Type: events.NotificationClicked, Notification: what}
		switch what {
		case app.OpenWindow:
			ev = events.Event{Type: events.OpenRequested}
		case app.OpenCompose:
			ev = events.Event{Type: events.ComposeRequested}
		}
		svc.Publish(ev)
		return
	}
	cmd, err := uiCmd(dataDir, what)
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		slog.Warn("starting the interface", "err", err)
		return
	}
	go func() { _ = cmd.Wait() }() // reaps it; its exit is its own
}

type systemClock struct{}

func (systemClock) Now() time.Time                                  { return time.Now() }
func (systemClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

type traySnapshot struct {
	st          core.Settings
	inboxUnread int
	codes       []core.SignInCode
}

type backendTray struct {
	svc     *core.Service
	plat    headlessPlatform
	sysLang string
	post    func(func())
	clock   app.Clock
	logf    func(string, ...any)
	show    func(what string) // showUI
	onQuit  func()

	ctx    context.Context
	cancel context.CancelFunc

	syncMu     sync.Mutex
	syncingAll bool

	timerMu   sync.Mutex
	codesStop func() bool

	settings     core.Settings
	trayUnread   int
	codesStarted time.Time
	codes        []core.SignInCode
	codesReady   bool
	loading      bool
	again        bool
	closed       bool
}

func newBackendTray(svc *core.Service, plat headlessPlatform, sysLang string, post func(func()), clock app.Clock, logf func(string, ...any), show func(string), onQuit func()) *backendTray {
	if clock == nil {
		clock = systemClock{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &backendTray{
		svc:        svc,
		plat:       plat,
		sysLang:    sysLang,
		post:       post,
		clock:      clock,
		logf:       logf,
		show:       show,
		onQuit:     onQuit,
		ctx:        ctx,
		cancel:     cancel,
		trayUnread: -1,
	}
}

func (t *backendTray) Start() {
	t.codesStarted = t.clock.Now().Truncate(time.Second)
	t.plat.BindTray(trayActions{
		open:     func() { t.show(app.OpenWindow) },
		compose:  func() { t.show(app.OpenCompose) },
		syncAll:  t.syncAll,
		hide:     t.hideTrayIcon,
		quit:     t.quit,
		quitStop: t.onQuit,
	})
	snap, err := t.readSnapshot(t.ctx, t.clock.Now())
	if err != nil {
		t.logf("tray: reading initial state: %v", err)
		snap.st.ShowTrayIcon = true
	}
	t.settings = snap.st
	t.plat.SetThemeSource(snap.st.Theme)
	t.plat.SetLanguage(snap.st.UILanguage, t.sysLang)
	t.plat.SetTrayVisible(snap.st.ShowTrayIcon)
	t.plat.SetTrayQuitStop(snap.st.BackgroundReceive == core.BackgroundReceiveAlways)
	t.updateUnread(snap.inboxUnread)
	if err == nil {
		t.applyCodes(snap.codes)
	}
	t.scheduleCodesTimer()
}

// quit is the tray's Quit: under 一直继续 (BackgroundReceiveAlways) it only
// asks the interface to quit, receiving going on; otherwise it is onQuit.
func (t *backendTray) quit() {
	go func() {
		st, err := t.svc.GetSettings(t.ctx)
		if err == nil && st.BackgroundReceive == core.BackgroundReceiveAlways {
			t.svc.Publish(events.Event{Type: events.QuitRequested})
			return
		}
		t.onQuit()
	}()
}

func (t *backendTray) syncAll() {
	t.syncMu.Lock()
	if t.syncingAll || t.ctx.Err() != nil {
		t.syncMu.Unlock()
		return
	}
	t.syncingAll = true
	ctx := t.ctx
	t.syncMu.Unlock()
	go func() {
		defer func() {
			t.syncMu.Lock()
			t.syncingAll = false
			t.syncMu.Unlock()
		}()
		accs, err := t.svc.ListAccounts(ctx)
		if err != nil {
			if ctx.Err() == nil {
				t.logf("tray: listing accounts: %v", err)
			}
			return
		}
		var wg sync.WaitGroup
		for _, acc := range accs {
			if acc.Kind == core.KindLocalArchive {
				continue
			}
			id := acc.ID
			wg.Go(func() {
				if err := t.svc.SyncNow(ctx, id); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
					t.logf("tray: syncing account %d: %v", id, err)
				}
			})
		}
		wg.Wait()
	}()
}

func (t *backendTray) hideTrayIcon() {
	ctx := t.ctx
	go func() {
		st, err := t.svc.GetSettings(ctx)
		if err != nil {
			if ctx.Err() == nil {
				t.logf("tray: reading settings: %v", err)
			}
			return
		}
		if !st.ShowTrayIcon {
			return
		}
		st.ShowTrayIcon = false
		if _, err := t.svc.SetSettings(ctx, st); err != nil {
			if ctx.Err() == nil {
				t.logf("tray: hiding tray icon: %v", err)
			}
			return
		}
		t.post(t.requestLoad)
	}()
}

func (t *backendTray) HandleEvent(ev events.Event) {
	switch ev.Type {
	case events.RunsChanged, events.ApprovalsChanged, events.MCPSendsChanged,
		events.ImportProgress, events.SkillsChanged, events.SyncStarted,
		events.SyncFailed, events.MCPChanged, events.ModelsChanged,
		events.OutboxChanged, events.SignaturesChanged:
		return
	}
	t.post(t.requestLoad)
}

func (t *backendTray) Close() {
	t.cancel()
	t.stopCodesTimer()
	t.post(func() {
		t.closed = true
		t.plat.ShowSignInCode(nil)
	})
}

func (t *backendTray) stopCodesTimer() {
	t.timerMu.Lock()
	stop := t.codesStop
	t.codesStop = nil
	t.timerMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (t *backendTray) scheduleCodesTimer() {
	t.stopCodesTimer()
	if t.closed || t.ctx.Err() != nil {
		return
	}
	wait := 30 * time.Second
	now := t.clock.Now()
	for _, code := range t.codes {
		wait = min(wait, max(time.Second, code.ReceivedAt.Add(15*time.Minute).Sub(now)))
	}
	t.timerMu.Lock()
	if t.ctx.Err() == nil {
		t.codesStop = t.clock.AfterFunc(wait, func() {
			t.post(t.requestLoad)
		})
	}
	t.timerMu.Unlock()
}

func (t *backendTray) requestLoad() {
	if t.closed || t.ctx.Err() != nil {
		return
	}
	if t.loading {
		t.again = true
		return
	}
	t.stopCodesTimer()
	t.loading = true
	ctx := t.ctx
	now := t.clock.Now()
	go func() {
		snap, err := t.readSnapshot(ctx, now)
		t.post(func() { t.applySnapshot(snap, err) })
	}()
}

func (t *backendTray) readSnapshot(ctx context.Context, now time.Time) (traySnapshot, error) {
	st, err := t.svc.GetSettings(ctx)
	if err != nil {
		return traySnapshot{}, err
	}
	accs, err := t.svc.ListAccounts(ctx)
	if err != nil {
		return traySnapshot{}, err
	}
	inbox := 0
	for _, acc := range accs {
		fs, err := t.svc.ListFolders(ctx, acc.ID)
		if err != nil {
			return traySnapshot{}, err
		}
		for _, f := range fs {
			if f.Role == "inbox" {
				inbox += f.Unread
			}
		}
	}
	codes, err := t.svc.RecentSignInCodes(ctx, now)
	if err != nil {
		return traySnapshot{}, err
	}
	return traySnapshot{st: st, inboxUnread: inbox, codes: codes}, nil
}

func (t *backendTray) applySnapshot(snap traySnapshot, err error) {
	t.loading = false
	if t.closed || t.ctx.Err() != nil {
		return
	}
	if err != nil {
		t.logf("tray: refreshing state: %v", err)
	} else {
		if snap.st.Theme != t.settings.Theme {
			t.plat.SetThemeSource(snap.st.Theme)
		}
		if snap.st.UILanguage != t.settings.UILanguage {
			t.plat.SetLanguage(snap.st.UILanguage, t.sysLang)
		}
		if snap.st.ShowTrayIcon != t.settings.ShowTrayIcon {
			t.plat.SetTrayVisible(snap.st.ShowTrayIcon)
		}
		if snap.st.BackgroundReceive != t.settings.BackgroundReceive {
			t.plat.SetTrayQuitStop(snap.st.BackgroundReceive == core.BackgroundReceiveAlways)
		}
		t.settings = snap.st
		t.updateUnread(snap.inboxUnread)
		t.applyCodes(snap.codes)
	}
	if t.again {
		t.again = false
		t.requestLoad()
		return
	}
	t.scheduleCodesTimer()
}

func (t *backendTray) applyCodes(codes []core.SignInCode) {
	fg := t.svc.UIForeground()
	if len(codes) > 0 && (t.codesReady || !codes[0].ReceivedAt.Before(t.codesStarted)) && !fg &&
		t.settings.NotifyNewMail && t.settings.NotifyPreview && t.plat.SignInCodeBannerSupported() &&
		!slices.ContainsFunc(t.codes, func(old core.SignInCode) bool { return old.MessageID == codes[0].MessageID }) {
		t.plat.ShowSignInCode(&codes[0])
	}
	t.codes, t.codesReady = codes, true
	t.plat.SetSignInCodes(codes)
	if !t.settings.NotifyNewMail || !t.settings.NotifyPreview || len(codes) == 0 || fg {
		t.plat.ShowSignInCode(nil)
	}
}

func (t *backendTray) updateUnread(inbox int) {
	badge := inbox
	if !t.settings.UnreadInMenuBar || !t.settings.ShowTrayIcon {
		badge = 0
	}
	if badge != t.trayUnread {
		t.trayUnread = badge
		t.plat.SetTrayUnread(badge)
	}
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

// Package app is Godwit's desktop interface: one App whose view is rebuilt
// every frame from state that only the UI thread changes.
package app

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
)

// Platform is what the App needs from the operating system. OpenFiles,
// OpenDirectory and SaveFile block: call them only off the UI thread.
type Platform interface {
	OpenURL(url string) error
	OpenPath(path string) error
	OpenFiles(multiple bool) ([]string, error)
	OpenDirectory() (string, error)
	SaveFile(defaultName string) (string, error)
	Notify(n Notification) error
	NotificationsSupported() bool
	SetOpenAtLogin(on bool) error
	SetTrayUnread(n int)
	SetThemeSource(theme string)
	ClipboardHTML() string
	TempDir() string
}

// Notification is a system notification about a message.
type Notification struct {
	ID, Title, Body string
	MessageID       int64
}

// OAuthFlow signs in with an OAuth provider; the default wraps the Service.
type OAuthFlow interface {
	Begin(ctx context.Context, provider, loginHint string) (flowID, authURL string, err error)
	Await(ctx context.Context, flowID string) (email string, err error)
	Cancel(flowID string)
}

type serviceOAuth struct{ svc *core.Service }

func (o serviceOAuth) Begin(ctx context.Context, provider, loginHint string) (string, string, error) {
	return o.svc.BeginOAuth(ctx, provider, loginHint)
}
func (o serviceOAuth) Await(ctx context.Context, flowID string) (string, error) {
	return o.svc.AwaitOAuth(ctx, flowID)
}
func (o serviceOAuth) Cancel(flowID string) { o.svc.CancelOAuth(flowID) }

// Deps are the App's collaborators. Post runs a function on the UI thread
// (win.Update in production). Clock and OAuth default to the real ones.
type Deps struct {
	Svc      *core.Service
	Platform Platform
	Post     func(func())
	Clock    Clock
	OAuth    OAuthFlow
}

// Page is a top-level screen.
type Page int

const (
	PageMail Page = iota
	PageCompose
	PageWit
	PageSettings
	PageOnboarding
)

// Route is where the App is.
type Route struct {
	Page                                         Page
	View                                         core.View
	ThreadID, MessageID, DraftID, ConversationID int64
	Settings                                     string
}

// App is the interface's state. Every field is read and written on the UI thread.
type App struct {
	svc   *core.Service
	plat  Platform
	post  func(func())
	clock Clock
	oauth OAuthFlow

	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	unsub  func()

	route Route
	gen   uint64 // bumped on every navigation; async results from an older gen are dropped

	loaded     bool
	accounts   []core.Account
	settings   core.Settings
	foreground bool
}

// New builds an App; Start brings it up.
func New(d Deps) *App {
	a := &App{svc: d.Svc, plat: d.Platform, post: d.Post, clock: d.Clock, oauth: d.OAuth}
	if a.clock == nil {
		a.clock = realClock{}
	}
	if a.oauth == nil {
		a.oauth = serviceOAuth{d.Svc}
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	return a
}

// Start loads accounts and settings and starts the event pump. Cancelling
// ctx closes the App's work as Close does.
func (a *App) Start(ctx context.Context) {
	context.AfterFunc(ctx, a.cancel)
	ch, unsub := a.svc.Subscribe()
	a.unsub = unsub
	go func() {
		for ev := range ch {
			a.onUI(func() { a.onEvent(ev) })
		}
	}()
	a.load()
}

func (a *App) load() {
	var accs []core.Account
	var st core.Settings
	a.do(func(ctx context.Context) error {
		var err error
		if accs, err = a.svc.ListAccounts(ctx); err != nil {
			return err
		}
		st, err = a.svc.GetSettings(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return // ponytail: load errors surface with the shell's status bar (Task 4)
		}
		a.accounts, a.settings, a.loaded = accs, st, true
	})
}

func (a *App) onEvent(ev events.Event) {
	if ev.Type == events.SettingsChanged {
		a.load()
	}
}

// Close cancels all work in flight; their done callbacks never run.
func (a *App) Close() {
	a.cancel()
	if a.unsub != nil {
		a.unsub()
	}
}

// SetForeground records whether the window is in front.
func (a *App) SetForeground(on bool) { a.foreground = on }

// NotificationClicked handles a click on a notification the App showed.
func (a *App) NotificationClicked(id string) {
	_ = id // ponytail: no notifications yet; the new-mail task routes the click to its message
}

// View builds the window's content.
func (a *App) View(c *ui.Context) {
	ui.Column(c).Fill().Center().Children(func() {
		ui.Text(c, "Godwit")
	})
}

// onUI posts f to the UI thread unless the App has closed by then.
func (a *App) onUI(f func()) {
	a.post(func() {
		if a.ctx.Err() == nil {
			f()
		}
	})
}

// do runs fn on a goroutine and then done on the UI thread, unless Close
// cancelled fn's context first.
func (a *App) do(fn func(ctx context.Context) error, done func(err error)) {
	ctx := a.ctx
	go func() {
		err := fn(ctx)
		if done != nil {
			a.onUI(func() { done(err) })
		}
	}()
}

// query is do for a read whose result belongs to the current route: done is
// dropped if the App navigated meanwhile.
func (a *App) query(fn func(ctx context.Context) error, done func(err error)) {
	gen := a.gen
	a.do(fn, func(err error) {
		if gen == a.gen {
			done(err)
		}
	})
}

// after runs f on the UI thread once d has passed, unless stop was called
// before f ran.
func (a *App) after(d time.Duration, f func()) (stop func()) {
	stopped := false // UI thread only
	cancel := a.clock.AfterFunc(d, func() {
		a.onUI(func() {
			if !stopped {
				f()
			}
		})
	})
	return func() { stopped = true; cancel() }
}

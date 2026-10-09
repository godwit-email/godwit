// Package app is Godwit's desktop interface: one App whose view is rebuilt
// every frame from state that only the UI thread changes.
package app

import (
	"context"
	"maps"
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

	ctx      context.Context // cancelled by Close or Start's ctx
	cancel   context.CancelFunc
	pumpDone chan struct{} // closed when the event pump has stopped and unsubscribed

	route Route
	gen   uint64 // bumped on every navigation; async results from an older gen are dropped

	fetch       func(ctx context.Context) (core.Settings, []core.Account, error) // what load reads; tests swap it
	loadGen     uint64                                                           // bumped per load; older completions are dropped
	loaded      bool
	loadErr     error // the newest load's error; nil once a load succeeds
	loadFailing bool  // loads have failed since the last success: toast once per streak
	accounts    []core.Account
	settings    core.Settings

	foreground bool

	history, ahead []Route           // navigation: routes behind and, after back, ahead
	pages          map[Page]pageView // by section; PageCompose belongs to PageMail
	sidebarHidden  bool

	dirty             []events.Event // events since the last refresh
	refreshStop       func()         // the pending throttled refresh; nil when none
	events, refreshes int            // counted for tests: refreshes stay far fewer than events

	syncing  map[int64]bool   // accounts syncing now
	syncErr  map[int64]string // each account's last sync failure, until it syncs
	syncedAt time.Time        // the last successful sync; zero before one
	ticking  bool             // a minute tick redraws the sync age

	unread map[int64]int // per account, summed over its folders
	toasts []ui.Toast    // shown on the next frame
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
	a.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
		accs, err := a.svc.ListAccounts(ctx)
		if err != nil {
			return core.Settings{}, nil, err
		}
		st, err := a.svc.GetSettings(ctx)
		return st, accs, err
	}
	a.pages = map[Page]pageView{PageMail: placeholder{}, PageWit: placeholder{}, PageSettings: placeholder{}, PageOnboarding: placeholder{}}
	a.syncing, a.syncErr = map[int64]bool{}, map[int64]string{}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.pumpDone = make(chan struct{})
	return a
}

// Start loads accounts and settings and starts the event pump. Cancelling
// ctx closes the App's work as Close does.
func (a *App) Start(ctx context.Context) {
	context.AfterFunc(ctx, a.cancel)
	ch, unsub := a.svc.Subscribe()
	go func() {
		defer close(a.pumpDone)
		defer unsub()
		for {
			select {
			case <-a.ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				a.onUI(func() { a.onEvent(ev) })
			}
		}
	}()
	a.load()
}

// load re-reads accounts, settings and unread counts; only the newest
// load's result applies. The first load picks the start route.
func (a *App) load() {
	a.loadGen++
	gen := a.loadGen
	var accs []core.Account
	var st core.Settings
	var unread map[int64]int
	fetch := a.fetch
	a.do(func(ctx context.Context) error {
		var err error
		if st, accs, err = fetch(ctx); err != nil {
			return err
		}
		unread = map[int64]int{}
		for _, acc := range accs {
			fs, err := a.svc.ListFolders(ctx, acc.ID)
			if err != nil {
				return err
			}
			unread[acc.ID] = 0 // every account has a key, folders or not
			for _, f := range fs {
				unread[acc.ID] += f.Unread
			}
		}
		return nil
	}, func(err error) {
		if gen != a.loadGen {
			return
		}
		a.loadErr = err
		if err != nil {
			if a.loaded && !a.loadFailing { // the first load's error shows in place of the shell
				a.toastErr(err)
			}
			a.loadFailing = true
			return
		}
		a.loadFailing = false
		first := !a.loaded
		a.accounts, a.settings, a.unread, a.loaded = accs, st, unread, true
		gone := func(id int64, _ bool) bool { _, ok := unread[id]; return !ok } // a removed account
		maps.DeleteFunc(a.syncing, gone)
		maps.DeleteFunc(a.syncErr, func(id int64, _ string) bool { return gone(id, false) })
		switch {
		case len(accs) == 0 && a.route.Page != PageOnboarding:
			a.history, a.ahead = nil, nil
			a.setRoute(Route{Page: PageOnboarding})
		case len(accs) > 0 && first:
			a.setRoute(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
		}
	})
}

// Close cancels all work in flight; their done callbacks never run.
func (a *App) Close() {
	a.cancel() // the event pump unsubscribes as it stops
}

// SetForeground records whether the window is in front.
func (a *App) SetForeground(on bool) { a.foreground = on }

// NotificationClicked handles a click on a notification the App showed.
func (a *App) NotificationClicked(id string) {
	_ = id // ponytail: no notifications yet; the new-mail task routes the click to its message
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

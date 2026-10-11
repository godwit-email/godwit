package main

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/webui"
)

// trafficLightSize is the height of macOS's window buttons; trafficLightInset
// is how far the close button sits from the window's left edge.
const trafficLightSize, trafficLightInset = 14, 16

// windowOptions is the main window on goos.
func windowOptions(goos string) mygo.WindowOptions {
	o := mygo.WindowOptions{Title: "Godwit", Width: 1280, Height: 820, MinWidth: 960, MinHeight: 600, StateKey: "main",
		TitleBarStyle: mygo.TitleBarHiddenInset}
	switch goos {
	case "darwin":
		// Not hiddenInset: its toolbar makes the title bar as tall as AppKit's
		// (66 on macOS 26). Center the traffic lights in our own top bar.
		o.Vibrancy, o.TitleBarStyle = mygo.VibrancySidebar, mygo.TitleBarHidden
		o.TrafficLightPosition = &mygo.Point{X: trafficLightInset, Y: (theme.TopBarHeight - trafficLightSize) / 2}
	case "windows":
		o.Vibrancy, o.TitleBarHeight = mygo.VibrancyMica, 40
	}
	return o
}

// gui runs the desktop app on dataDir until it quits or ctx ends. On the
// default directory a second GUI focuses the first (singleInstance). It owns
// dataDir (ownerlock) before opening it; if another process does, it says so
// in an error dialog and exits 2.
func gui(ctx context.Context, dataDir string) int {
	mygo.App.SetName("Godwit")
	if singleInstance(dataDir) && !mygo.App.RequestSingleInstanceLock() {
		return 0 // the first Godwit shows its window
	}
	defer context.AfterFunc(ctx, mygo.App.Quit)() // SIGINT / SIGTERM quit as the menu does
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := 0
	var release func() error
	var svc *core.Service
	var plat *platform
	var a *app.App
	var clicked []string                           // notification clicks before the App exists, as the one that launched it
	mygo.App.OnNotificationClick(func(id string) { // before Run, to get that one
		if a == nil {
			clicked = append(clicked, id)
			return
		}
		a.NotificationClicked(id) // it shows the window
	})
	// Compose pages reach only their own window's draft; bound before Run.
	pages := webui.NewCompose()
	// Embedded pages and their roles, by their *mygo.Page.
	roles := webui.NewPages(func(ctx context.Context) any { return mygo.CallerPage(ctx) })
	var windows *webui.Windows
	emit := func(t webui.Target, ev webui.WitEvent) {
		var err error
		if t.Page != 0 {
			if p, ok := roles.Key(t.Page); ok {
				err = webui.WitStream.EmitPage(p.(*mygo.Page), ev)
			}
		} else if w := mygo.WindowByID(t.Window); w != nil {
			err = webui.WitStream.Emit(w, ev)
		}
		if err != nil {
			log.Printf("wit event to %+v: %v", t, err)
		}
	}
	openCompose := func(id int64) { windows.Open(id) } // set below, before any page runs
	mygo.Bind(webui.NewComposePage(pages, webui.PageDeps{
		WinOf: func(ctx context.Context) (int, bool) {
			if w := mygo.CallerWindow(ctx); w != nil {
				return w.ID(), true
			}
			return 0, false
		},
		PickFiles:   pickFiles,
		Emit:        emit,
		OpenURL:     mygo.Shell.OpenExternal,
		OpenCompose: openCompose,
	}))
	// The Wit page, embedded in the main window: its callbacks run on the
	// App's UI thread; its events go to its page only.
	var witView *mygo.WebView
	onApp := func(f func(*app.App)) {
		if plat != nil && plat.win != nil {
			plat.win.Update(func() { f(a) })
		}
	}
	toWit := func(what string, emit func(*mygo.Page) error) {
		if witView == nil {
			return
		}
		if err := emit(witView.Page()); err != nil {
			log.Printf("%s to the Wit page: %v", what, err)
		}
	}
	wit := webui.NewWit(roles, webui.WitPageDeps{
		Emit: emit,
		Open: func(m webui.WitOpenMsg) {
			toWit("witOpen", func(p *mygo.Page) error { return webui.WitOpen.EmitPage(p, m) })
		},
		State: func(st webui.WitPageState) {
			toWit("witState", func(p *mygo.Page) error { return webui.WitState.EmitPage(p, st) })
		},
		OpenMessage:         func(id int64) { onApp(func(a *app.App) { a.OpenMessage(id) }) },
		OpenModels:          func() { onApp((*app.App).OpenModels) },
		Navigate:            func(dir int) { onApp(func(a *app.App) { a.Navigate(dir) }) },
		ConversationCreated: func(id, v int64) { onApp(func(a *app.App) { a.ConversationCreated(id, v) }) },
		OpenURL:             mygo.Shell.OpenExternal,
		OpenCompose:         openCompose,
	})
	mygo.Bind(webui.NewWitPage(wit))
	var cas cascade
	windows = webui.NewWindows(pages, func(int64) webui.Window { return newComposeWindow(windows, &cas) }, askUnsaved)
	mygo.App.WhenReady(func() {
		lang := mygo.App.Locale()
		var err error
		release, err = ownerlock.Acquire(dataDir)
		if err != nil {
			i18n.SetLanguage(lang)
			var msg string
			msg, code = lockFailure(err)
			mygo.Dialog.Error("Godwit", msg)
			mygo.App.Quit()
			return
		}
		svc, err = core.New(ctx, core.Options{DataDir: dataDir, AutoSync: true, SystemLanguage: lang})
		if err != nil {
			i18n.SetLanguage(lang)
			code = 1
			mygo.Dialog.Error("Godwit", openFailure(err))
			mygo.App.Quit()
			return
		}
		pages.SetService(svc)
		wit.SetService(svc)
		uiLang := lang // for the menus; the App applies the language to itself
		if st, err := svc.GetSettings(ctx); err != nil {
			log.Printf("reading settings: %v; the menus use the system language", err)
		} else {
			uiLang = cmp.Or(st.UILanguage, lang)
		}
		i18n.SetLanguage(uiLang)

		opts := windowOptions(runtime.GOOS)
		plat = &platform{dataDir: dataDir, svc: svc, windows: windows, wit: wit}
		var win *mygo.Window
		a = app.New(app.Deps{Svc: svc, Platform: plat, Post: func(f func()) { win.Update(f) },
			SystemLanguage: lang, Vibrancy: opts.Vibrancy != mygo.VibrancyNone, Dev: mygo.IsDev()})
		plat.onAPIStop = a.LocalAPIStopped
		opts.Content = ui.View(a.View)
		win = mygo.NewWindow(opts)
		plat.win = win
		if v, err := win.NewWebView(mygo.WebViewOptions{URL: "/wit.html"}); err != nil {
			log.Printf("the Wit page: %v", err) // the Ask page shows no content
		} else {
			witView = v
			lockPage(v.Page())
			roles.Register(v.Page(), "wit")
			a.SetWitPage(v, wit.SetConversation)
		}
		win.OnFocus(func() { a.SetForeground(true) })
		win.OnBlur(func() { a.SetForeground(false) })
		mygo.App.OnSecondInstance(func([]string, string) { plat.ShowWindow() })
		mygo.App.OnActivate(func(visible bool) {
			if !visible {
				plat.ShowWindow()
			}
		})

		compose := func(*mygo.MenuItem, *mygo.Window) { a.Compose() }
		// The Mail menu stands in for File, which on macOS holds Close (⌘W).
		mailMenu := []*mygo.MenuItem{{Label: i18n.T("mail.compose"), Accelerator: "CmdOrCtrl+N", Click: compose},
			mygo.Separator(), {Role: mygo.RoleClose}}
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu},
			{Label: i18n.T("shell.rail.mail"), Submenu: mailMenu},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleWindowMenu},
		}))
		if icon, err := trayIcon(trayBoxFor(runtime.GOOS)); err != nil {
			log.Print(err)
		} else if plat.tray, err = mygo.NewTray(mygo.TrayOptions{Icon: icon, IconIsTemplate: true, ToolTip: "Godwit",
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{Label: i18n.T("tray.open"), Click: func(*mygo.MenuItem, *mygo.Window) { plat.ShowWindow() }},
				{Label: i18n.T("mail.compose"), Click: compose},
				{Label: i18n.T("tray.sync_all"), Click: func(*mygo.MenuItem, *mygo.Window) { a.SyncAll() }},
				mygo.Separator(),
				{Label: i18n.T("tray.quit"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
			})}); err != nil {
			log.Print(err) // no tray here (Linux without an indicator host)
		}

		// Closing the window hides it while the App keeps running in the
		// background; otherwise closing quits. Every quit goes through
		// quitHook: first the compose windows close, each flushing its page,
		// then nq waits for the main window's writes (a failed or slow one
		// asks first) and quits again. Hiding needs no save: nothing closes.
		nq := &nativeQuit{beforeClose: a.BeforeClose, close: a.Close, quit: func() { go mygo.App.Quit() }} // mygo's calls are goroutine-safe
		win.OnClose(func(e *mygo.CloseEvent) {
			if nq.flushed {
				return // the quit's last step closes it
			}
			e.PreventDefault()
			if a.KeepInBackground() {
				win.Hide()
				return
			}
			mygo.App.Quit()
		})
		mygo.App.OnBeforeQuit(quitHook(windows, nq.onQuit, nq.reset))
		a.Start(ctx)
		a.SetForeground(win.IsFocused())
		for _, id := range clicked { // the App takes them once it has loaded; the last one wins
			a.NotificationClicked(id)
		}
		clicked = nil
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	if plat != nil {
		plat.SetLocalAPI(0, false) // after BeforeClose, before the Service closes
	}
	if svc != nil {
		svc.Close()
	}
	if release != nil {
		release()
	}
	return code
}

// quitApp quits again once the compose windows closed; tests swap it.
var quitApp = func() { mygo.App.Quit() }

// quitHook is the one OnBeforeQuit listener: mygo runs every listener even
// after one prevented the quit. With compose windows open it prevents the
// quit, calls reset (the native flush must run after them, again if it ran)
// and closes them, each flushing its page; once all have gone it quits
// again, or stops when the user kept one. With none it runs native.
func quitHook(windows interface {
	Count() int
	CloseAll(func(bool))
}, native func(e *mygo.QuitEvent), reset func()) func(*mygo.QuitEvent) {
	return func(e *mygo.QuitEvent) {
		if windows.Count() == 0 {
			native(e)
			return
		}
		e.PreventDefault()
		reset()
		windows.CloseAll(func(ok bool) {
			if ok {
				quitApp()
			}
		})
	}
}

// nativeQuit is the main window's part of a quit: it waits for the main
// window's writes (beforeClose), then quits again; that pass closes the App.
// flushed holds for the current quit attempt only: reset forgets it and
// starts a new attempt (gen), so a flush still running from an earlier
// attempt neither marks this one flushed nor quits. Used on the UI thread only.
type nativeQuit struct {
	beforeClose func(proceed func()) // App.BeforeClose
	close       func()               // App.Close
	quit        func()
	flushed     bool
	gen         int
}

// onQuit prevents a quit not flushed yet and flushes; a flushed one closes
// the App and goes on.
func (n *nativeQuit) onQuit(e *mygo.QuitEvent) {
	if n.flushed {
		n.close()
		return
	}
	e.PreventDefault()
	gen := n.gen
	n.beforeClose(func() {
		if gen != n.gen {
			return // reset since: a later quit owns the outcome
		}
		n.flushed = true
		n.quit()
	})
}

// reset makes the next quit flush again and voids flushes still running.
func (n *nativeQuit) reset() { n.flushed = false; n.gen++ }

// openFailure is what the GUI tells the user when core.New fails: a class
// of reason, never err's text.
func openFailure(err error) string {
	reason := "app.open_failed.other"
	if errors.Is(err, fs.ErrPermission) {
		reason = "app.open_failed.permission"
	}
	return i18n.Tf("app.open_failed", i18n.T(reason))
}

// lockFailure is what the GUI tells the user when it cannot own the data
// directory, and its exit code. The message never carries err's text.
func lockFailure(err error) (msg string, code int) {
	if errors.Is(err, ownerlock.ErrAnotherOwner) {
		return i18n.T("app.another_owner"), 2
	}
	return i18n.T("app.lock_failed"), 1
}

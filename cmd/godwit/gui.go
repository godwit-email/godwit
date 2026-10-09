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
	if err := theme.RegisterFonts(); err != nil {
		log.Print(err) // the system's fonts stand in
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
		uiLang := lang // for the menus; the App applies the language to itself
		if st, err := svc.GetSettings(ctx); err != nil {
			log.Printf("reading settings: %v; the menus use the system language", err)
		} else {
			uiLang = cmp.Or(st.UILanguage, lang)
		}
		i18n.SetLanguage(uiLang)

		opts := windowOptions(runtime.GOOS)
		plat = &platform{dataDir: dataDir, svc: svc}
		var win *mygo.Window
		a = app.New(app.Deps{Svc: svc, Platform: plat, Post: func(f func()) { win.Update(f) },
			SystemLanguage: lang, Vibrancy: opts.Vibrancy != mygo.VibrancyNone})
		plat.onAPIStop = a.LocalAPIStopped
		opts.Content = ui.View(a.View)
		win = mygo.NewWindow(opts)
		plat.win = win
		win.OnFocus(func() { a.SetForeground(true) })
		win.OnBlur(func() { a.SetForeground(false) })
		mygo.App.OnSecondInstance(func([]string, string) { plat.ShowWindow() })
		mygo.App.OnActivate(func(visible bool) {
			if !visible {
				plat.ShowWindow()
			}
		})

		compose := func(*mygo.MenuItem, *mygo.Window) { plat.ShowWindow(); a.Compose() }
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu},
			{Label: i18n.T("shell.rail.mail"), Submenu: []*mygo.MenuItem{
				{Label: i18n.T("mail.compose"), Accelerator: "CmdOrCtrl+N", Click: compose},
			}},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleWindowMenu},
		}))
		if icon, err := trayIcon(); err != nil {
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
		// background; otherwise closing quits. Quitting first saves every
		// draft's last edits (a failed or slow save asks first), then quits
		// again. Hiding needs no save: the drafts stay open.
		flushed := false
		saveThen := func(prevent, then func()) {
			if !flushed {
				prevent()
				a.BeforeClose(func() { flushed = true; go then() }) // mygo's calls are goroutine-safe
			}
		}
		win.OnClose(func(e *mygo.CloseEvent) {
			if !flushed && a.KeepInBackground() {
				e.PreventDefault()
				win.Hide()
				return
			}
			saveThen(e.PreventDefault, mygo.App.Quit)
		})
		mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
			if saveThen(e.PreventDefault, mygo.App.Quit); flushed {
				a.Close()
			}
		})
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

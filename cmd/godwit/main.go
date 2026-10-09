// Command godwit is the Godwit desktop app.
//
// Release builds set the version Settings › About shows:
//
//	go build -ldflags "-X github.com/godwit-email/godwit/app.Version=1.2.3" ./cmd/godwit
package main

import (
	"cmp"
	"context"
	"log"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// windowOptions is the main window on goos.
func windowOptions(goos string) mygo.WindowOptions {
	o := mygo.WindowOptions{Title: "Godwit", Width: 1280, Height: 820, MinWidth: 960, MinHeight: 600, StateKey: "main",
		TitleBarStyle: mygo.TitleBarHiddenInset}
	switch goos {
	case "darwin":
		o.Vibrancy = mygo.VibrancySidebar
	case "windows":
		o.Vibrancy, o.TitleBarHeight = mygo.VibrancyMica, 40
	}
	return o
}

func main() {
	mygo.App.SetName("Godwit")
	if !mygo.App.RequestSingleInstanceLock() {
		return // the first Godwit shows its window
	}
	if err := theme.RegisterFonts(); err != nil {
		log.Print(err) // the system's fonts stand in
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var svc *core.Service
	mygo.App.WhenReady(func() {
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		lang := mygo.App.Locale()
		svc, err = core.New(ctx, core.Options{DataDir: dir, AutoSync: true, SystemLanguage: lang})
		if err != nil {
			log.Fatal(err)
		}
		st, _ := svc.GetSettings(ctx)
		i18n.SetLanguage(cmp.Or(st.UILanguage, lang)) // for the menus; the App applies it to itself

		opts := windowOptions(runtime.GOOS)
		plat := &platform{}
		var win *mygo.Window
		a := app.New(app.Deps{Svc: svc, Platform: plat, Post: func(f func()) { win.Update(f) },
			SystemLanguage: lang, Vibrancy: opts.Vibrancy != mygo.VibrancyNone})
		opts.Content = ui.View(a.View)
		win = mygo.NewWindow(opts)
		plat.win = win
		win.OnFocus(func() { a.SetForeground(true) })
		win.OnBlur(func() { a.SetForeground(false) })
		mygo.App.OnNotificationClick(a.NotificationClicked) // it shows the window
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
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	if svc != nil {
		svc.Close()
	}
}

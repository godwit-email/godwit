// Command godwit is the Godwit desktop app.
package main

import (
	"context"
	"log"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
)

func main() {
	mygo.App.SetName("Godwit")
	if !mygo.App.RequestSingleInstanceLock() {
		return // another Godwit owns the data directory
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var svc *core.Service
	mygo.App.WhenReady(func() {
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		svc, err = core.New(ctx, core.Options{DataDir: dir, AutoSync: true, SystemLanguage: mygo.App.Locale()})
		if err != nil {
			log.Fatal(err)
		}
		var win *mygo.Window
		a := app.New(app.Deps{Svc: svc, Platform: platform{}, Post: func(f func()) { win.Update(f) }})
		win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Godwit", Width: 1280, Height: 820, MinWidth: 960, MinHeight: 600, StateKey: "main",
			TitleBarStyle: mygo.TitleBarHiddenInset,
			Content:       ui.View(a.View),
		})
		win.OnFocus(func() { a.SetForeground(true) })
		win.OnBlur(func() { a.SetForeground(false) })
		mygo.App.OnNotificationClick(a.NotificationClicked)
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.Close() })
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

// platform is app.Platform on mygo.
type platform struct{}

func (platform) OpenURL(url string) error   { return mygo.Shell.OpenExternal(url) }
func (platform) OpenPath(path string) error { return mygo.Shell.OpenPath(path) }
func (platform) OpenFiles(multiple bool) ([]string, error) {
	return mygo.Dialog.Open(mygo.OpenDialogOptions{Multiple: multiple})
}
func (platform) OpenDirectory() (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Directory: true, CreateDirectories: true})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}
func (platform) SaveFile(defaultName string) (string, error) {
	return mygo.Dialog.Save(mygo.SaveDialogOptions{DefaultPath: defaultName, CreateDirectories: true})
}
func (platform) Notify(n app.Notification) error {
	return mygo.NewNotification(mygo.NotificationOptions{ID: n.ID, Title: n.Title, Body: n.Body}).Show()
}
func (platform) NotificationsSupported() bool { return mygo.NotificationsSupported() }
func (platform) SetOpenAtLogin(on bool) error { return mygo.App.SetOpenAtLogin(on) }
func (platform) SetTrayUnread(int)            {} // ponytail: no tray yet; the tray task fills this
func (platform) SetThemeSource(theme string)  { mygo.Theme.SetSource(mygo.ThemeSource(theme)) }
func (platform) ClipboardHTML() string        { return mygo.Clipboard.ReadHTML() }
func (platform) TempDir() string              { return os.TempDir() }

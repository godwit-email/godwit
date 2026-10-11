package main

import (
	"bytes"
	"image/png"
	"strconv"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/assets"
)

// platform is app.Platform on mygo. win and tray are set once they exist;
// tray stays nil where the system has none.
type platform struct {
	win  *mygo.Window
	tray *mygo.Tray
}

func (*platform) OpenURL(url string) error   { return mygo.Shell.OpenExternal(url) }
func (*platform) OpenPath(path string) error { return mygo.Shell.OpenPath(path) }
func (*platform) OpenFiles(multiple bool) ([]string, error) {
	return mygo.Dialog.Open(mygo.OpenDialogOptions{Multiple: multiple})
}
func (*platform) OpenDirectory() (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Directory: true, CreateDirectories: true})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}
func (*platform) SaveFile(defaultName string) (string, error) {
	return mygo.Dialog.Save(mygo.SaveDialogOptions{DefaultPath: defaultName, CreateDirectories: true})
}
func (*platform) Notify(n app.Notification) error {
	return mygo.NewNotification(mygo.NotificationOptions{ID: n.ID, Title: n.Title, Body: n.Body}).Show()
}
func (*platform) NotificationsSupported() bool { return mygo.NotificationsSupported() }
func (*platform) SetOpenAtLogin(on bool) error { return mygo.App.SetOpenAtLogin(on) }
func (p *platform) SetTrayUnread(n int) {
	if p.tray != nil {
		title := ""
		if n > 0 {
			title = strconv.Itoa(n)
		}
		p.tray.SetTitle(title) // shown on macOS only
	}
	mygo.App.SetBadgeCount(n)
}
func (p *platform) ShowWindow() {
	if p.win.IsMinimized() {
		p.win.Restore()
	}
	p.win.Show()
	p.win.Focus()
}
func (*platform) SetThemeSource(theme string) { mygo.Theme.SetSource(mygo.ThemeSource(theme)) }
func (*platform) ClipboardHTML() string       { return mygo.Clipboard.ReadHTML() }
func (*platform) TempDir() string {
	dir, err := mygo.App.Path(mygo.PathTemp)
	if err != nil {
		return "" // os.CreateTemp then uses the default
	}
	return dir
}

// trayPx is the tray icon's size in pixels: 16 points at 2x.
const trayPx = 32

// trayIcon draws the Godwit mark in black on transparent, as a macOS
// template image (the system tints it).
func trayIcon() ([]byte, error) {
	svg, err := ui.ParseSVG(assets.Logo)
	if err != nil {
		return nil, err
	}
	img := ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Icon(c, svg).Size(trayPx, trayPx).TextColor(ui.Color{A: 255})
	}, trayPx, trayPx, 1)
	var b bytes.Buffer
	err = png.Encode(&b, img)
	return b.Bytes(), err
}

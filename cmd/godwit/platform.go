package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/assets"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/sysproxy"
	"github.com/godwit-email/godwit/internal/theme"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/webui"
)

// avatarService reads a message's sender avatar for notifications and sign-in banners.
type avatarService interface {
	SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (core.Avatar, error)
}

// platform is app.Platform on mygo. win and tray are set once they exist;
// tray stays nil where the system has none, and in the interface, whose
// tray is the backend's.
type platform struct {
	win            *mygo.Window
	tray           *mygo.Tray
	windows        *webui.Windows // the compose windows
	wit            *webui.Wit
	roles          *webui.Pages                 // the embedded pages' roles
	reader         *webui.Reader                // the reader pages' messages
	sig            *webui.Signature             // the signature page's signature and flushes
	bodies         map[*mygo.Page]*mygo.WebView // the reader pages open; UI thread
	signInCodes    []core.SignInCode
	codeIcons      map[int64]*mygo.MenuIcon // recent codes' menu icons by message; UI thread
	codeBanner     *signInBanner
	updateTray     func()
	setTrayVisible func(visible bool)
	trayQuitStop   bool // 退出并停止收信 shows; UI thread

	dataDir      string
	svc          avatarService
	saveMu       sync.Mutex
	saveDir      string                 // last directory chosen in SaveFile or OpenDirectory
	downloadsDir func() (string, error) // mygo.App.Path(PathDownloads); tests swap it
}

func (p *platform) DataDir() (string, bool) { return p.dataDir, !singleInstance(p.dataDir) }

// SetLocalAPI reports the socket external clients reach while on: the
// backend the interface started turns their access on and off itself as
// Settings.LocalAPI changes (followLocalAPI).
func (p *platform) SetLocalAPI(_ uint64, on bool) (string, error) {
	if !on {
		return "", nil
	}
	return filepath.Join(p.dataDir, rpc.SocketName), nil
}

func (*platform) OpenURL(url string) error   { return mygo.Shell.OpenExternal(url) }
func (*platform) OpenPath(path string) error { return mygo.Shell.OpenPath(path) }
func (*platform) OpenFiles(multiple bool) ([]string, error) {
	return mygo.Dialog.Open(mygo.OpenDialogOptions{Multiple: multiple})
}
func (*platform) OpenImage() (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Multiple: false,
		Filters: []mygo.FileFilter{
			{Name: "Images", Extensions: []string{"png", "jpg", "jpeg", "webp", "svg", "gif"}},
		},
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

type savedDir struct {
	Dir string `json:"dir"`
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (p *platform) saveDirFile() string {
	if p.dataDir == "" {
		return ""
	}
	return filepath.Join(p.dataDir, "save-dir.json")
}

func (p *platform) defaultSaveDir() string {
	p.saveMu.Lock()
	dir := p.saveDir
	if dir == "" {
		if path := p.saveDirFile(); path != "" {
			var s savedDir
			if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &s) == nil {
				dir = s.Dir
				p.saveDir = dir
			}
		}
	}
	p.saveMu.Unlock()
	if isDir(dir) {
		return dir
	}
	downloads := p.downloadsDir
	if downloads == nil {
		downloads = func() (string, error) { return mygo.App.Path(mygo.PathDownloads) }
	}
	if d, err := downloads(); err == nil && isDir(d) {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && isDir(home) {
		return home
	}
	return ""
}

func (p *platform) rememberSaveDir(dir string) {
	if dir == "" {
		return
	}
	p.saveMu.Lock()
	p.saveDir = dir
	path := p.saveDirFile()
	p.saveMu.Unlock()
	if path == "" {
		return
	}
	if data, err := json.Marshal(savedDir{Dir: dir}); err == nil {
		_ = os.WriteFile(path, data, 0o600) // a torn write only loses the last folder: loading falls back to Downloads
	}
}

func (p *platform) saveDefaultPath(defaultName string) string {
	if defaultName == "" {
		return p.defaultSaveDir()
	}
	if !filepath.IsAbs(defaultName) {
		if dir := p.defaultSaveDir(); dir != "" {
			return filepath.Join(dir, defaultName)
		}
	}
	return defaultName
}

func (p *platform) OpenDirectory() (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		DefaultPath:       p.defaultSaveDir(),
		Directory:         true,
		CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	p.rememberSaveDir(paths[0])
	return paths[0], nil
}
func (p *platform) SaveFile(defaultName string) (string, error) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		DefaultPath:       p.saveDefaultPath(defaultName),
		CreateDirectories: true,
	})
	if err == nil && path != "" {
		p.rememberSaveDir(filepath.Dir(path))
	}
	return path, err
}
func (p *platform) Notify(n app.Notification) error { return showNotification(p.svc, n) }
func (*platform) NotificationsSupported() bool      { return mygo.NotificationsSupported() }
func (*platform) SetOpenAtLogin(on bool) error      { return setOpenAtLogin(on) }
func (*platform) OpenAtLogin() bool                 { return openAtLogin() }

type trayActions struct {
	open    func()
	compose func()
	syncAll func()
	hide    func()
	quit    func()
	// quitStop is 退出并停止收信, shown while SetTrayQuitStop is on.
	quitStop func()
}

func (p *platform) trayMenuItems(act trayActions, now time.Time) []*mygo.MenuItem {
	var items []*mygo.MenuItem
	if act.open != nil {
		items = append(items, &mygo.MenuItem{Label: i18n.T("tray.open"), Click: func(*mygo.MenuItem, *mygo.Window) { act.open() }})
	}
	if act.compose != nil {
		items = append(items, &mygo.MenuItem{Label: i18n.T("mail.compose"), Click: func(*mygo.MenuItem, *mygo.Window) { act.compose() }})
	}
	items = append(items, &mygo.MenuItem{Label: i18n.T("tray.sync_all"), Click: func(*mygo.MenuItem, *mygo.Window) { act.syncAll() }})
	items = append(items, recentCodeMenu(p.signInCodes, now, p.signInCodeIcon, p.useSignInCode)...)
	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{Label: i18n.T("tray.hide"), Click: func(*mygo.MenuItem, *mygo.Window) { act.hide() }},
		mygo.Separator(),
		&mygo.MenuItem{Label: i18n.T("tray.quit"), Click: func(*mygo.MenuItem, *mygo.Window) { act.quit() }},
	)
	if p.trayQuitStop && act.quitStop != nil {
		items = append(items, &mygo.MenuItem{Label: i18n.T("tray.quit_stop"), Click: func(*mygo.MenuItem, *mygo.Window) { act.quitStop() }})
	}
	return items
}

// SetTrayQuitStop shows or hides 退出并停止收信 (BackgroundReceiveAlways).
func (p *platform) SetTrayQuitStop(on bool) {
	if p.trayQuitStop != on {
		p.trayQuitStop = on
		if p.updateTray != nil {
			p.updateTray()
		}
	}
}

func (p *platform) BindTray(act trayActions) {
	menu := func() *mygo.Menu { return mygo.NewMenu(p.trayMenuItems(act, time.Now())) }
	p.updateTray = func() {
		if p.tray != nil {
			p.tray.SetMenu(menu())
		}
	}
	p.setTrayVisible = func(visible bool) {
		if visible == (p.tray != nil) {
			return
		}
		if !visible {
			p.ShowSignInCode(nil) // the banner is a flyout of the tray
			p.tray.Destroy()
			p.tray = nil
			return
		}
		if icon, err := trayIcon(trayBoxFor(runtime.GOOS)); err != nil {
			log.Print(err)
		} else if p.tray, err = mygo.NewTray(mygo.TrayOptions{Icon: icon, IconIsTemplate: true, ToolTip: "Godwit",
			Menu: menu()}); err != nil {
			log.Print(err) // no tray here (Linux without an indicator host)
		} else if runtime.GOOS == "windows" && act.open != nil {
			p.tray.OnClick(act.open) // Windows opens the app on a left click; the menu stays on the right
		}
	}
}

func (p *platform) SetTrayVisible(visible bool) {
	if p.setTrayVisible != nil {
		p.setTrayVisible(visible)
	}
}
func (p *platform) SetTrayUnread(n int) {
	if p.tray != nil {
		title := ""
		if n > 0 {
			title = strconv.Itoa(n)
		}
		p.tray.SetTitle(title) // shown on macOS only
	}
}

// SetAppUnread badges the app icon; the badge goes with the interface process,
// leaving only the backend's tray count while no interface runs (ADR 0038).
func (*platform) SetAppUnread(n int) { mygo.App.SetBadgeCount(n) }
func (p *platform) ShowWindow() {
	if p.win.IsMinimized() {
		p.win.Restore()
	}
	p.win.Show()
	p.win.Focus()
}
func (*platform) SetThemeSource(theme string) { mygo.Theme.SetSource(mygo.ThemeSource(theme)) }
func (p *platform) SetLanguage(uiLang, sysLang string) {
	if os.Getenv("MYGO_LOCALE") == "" {
		mygo.App.SetLocale(uiLocale(uiLang, sysLang))
	}
	i18n.SetLanguage(mygo.App.Locale())
	if p.updateTray != nil {
		p.updateTray()
	}
}
func (p *platform) OpenCompose(draftID int64) { p.windows.Open(draftID) }
func (p *platform) DraftDeleted(id int64)     { p.windows.CloseDraft(id) }
func (p *platform) ComposeOpen(id int64) bool { return p.windows.IsOpen(id) }

// NewBodyView makes a reader page in the main window, kept on its entry
// (lockPage), registered as "reader" and bound to message before it
// loads (loadAfterSetup), so that its first call finds it so; close
// undoes each and destroys it. UI thread.
func (p *platform) NewBodyView(message int64) (ui.NativeWebView, func(), error) {
	// ScrollToContainer: the body fills its own height, and the wheel and
	// touchpad over it scroll the reader's pane as over any native element
	// of it (MyGo's own notches glide, a touchpad's moves follow), rather
	// than a page that scrolls itself.
	v, err := p.win.NewWebView(mygo.WebViewOptions{BackgroundColor: panelBackground(), ScrollToContainer: true})
	if err != nil {
		return nil, nil, err
	}
	page := v.Page()
	closeView := func() {
		delete(p.bodies, page)
		p.reader.Unbind(page)
		p.roles.Unregister(page)
		v.Destroy()
	}
	err = loadAfterSetup(func() {
		lockPage(page)
		p.roles.Register(page, "reader")
		p.reader.Bind(page, message)
		if p.bodies == nil {
			p.bodies = map[*mygo.Page]*mygo.WebView{}
		}
		p.bodies[page] = v
	}, func() error { return page.LoadURL("/reader.html") }, closeView)
	if err != nil {
		return nil, nil, err
	}
	return v, closeView, nil
}

// NewSignatureView makes the signature page in the main window, kept on
// its entry and registered as "signature" before it loads. show tells it
// the signature (also kept for a page that loads later); flush's done runs
// on the window's UI thread once the page answers (at once while the page
// never ran); close undoes it all.
func (p *platform) NewSignatureView() (ui.NativeWebView, func(int64), func(func(bool)), func(), error) {
	v, err := p.win.NewWebView(mygo.WebViewOptions{})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	page := v.Page()
	closeView := func() {
		p.sig.Close()
		p.roles.Unregister(page)
		v.Destroy()
	}
	err = loadAfterSetup(func() {
		lockPage(page)
		p.roles.Register(page, "signature")
	}, func() error { return page.LoadURL("/signature.html") }, closeView)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// Answers to the page's PickImage and AskLink.
	p.sig.EmitImage = func(pick webui.SignaturePick) {
		if err := webui.SignatureImage.EmitPage(page, pick); err != nil {
			log.Printf("an image to the signature page: %v", err)
		}
	}
	p.sig.EmitLink = func(a webui.SignatureLinkAnswer) {
		if err := webui.SignatureLink.EmitPage(page, a); err != nil {
			log.Printf("a link to the signature page: %v", err)
		}
	}
	show := func(id int64) {
		p.sig.Show(id)
		if err := webui.SignatureShow.EmitPage(page, id); err != nil {
			log.Printf("the signature to the signature page: %v", err)
		}
	}
	flush := func(done func(bool)) {
		p.sig.Flush(func(tok int64) {
			if err := webui.SignatureFlush.EmitPage(page, tok); err != nil {
				log.Printf("a flush to the signature page: %v", err)
			}
		}, func(ok bool) { p.win.Update(func() { done(ok) }) })
	}
	return v, show, flush, closeView, nil
}

// panelBackground is the panel's color for a web view whose page is
// transparent (the reader's body, the About page's bird): a transparent web
// view shows the window's material, not the panel. In light-dark, it follows
// the app's theme as it changes.
func panelBackground() string {
	hex := func(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
	return "light-dark(" + hex(theme.Light().Background) + ", " + hex(theme.Dark().Background) + ")"
}

// NewBirdView makes the About page's bird in the main window, kept on its
// entry; it calls nothing back. UI thread.
func (p *platform) NewBirdView() (ui.NativeWebView, func(), error) {
	v, err := p.win.NewWebView(mygo.WebViewOptions{BackgroundColor: panelBackground()})
	if err != nil {
		return nil, nil, err
	}
	lockPage(v.Page())
	if err := v.Page().LoadURL("/about.html"); err != nil {
		v.Destroy()
		return nil, nil, err
	}
	return v, v.Destroy, nil
}

func (p *platform) CheckForUpdates() {
	if !packaged() {
		updater.CheckForUpdates() // a dev build says why it cannot
		return
	}
	// The Store updates the MSIX: its page shows and installs a newer version.
	if err := p.OpenURL("ms-windows-store://pdp/?ProductId=" + storeProduct); err != nil {
		log.Printf("open the Store: %v", err)
	}
}
func (p *platform) LatestVersion(ctx context.Context) (string, error) {
	if packaged() {
		return storeLatest(ctx, sysproxy.For) // shortcut: the system's proxy, not a custom one set in Godwit (the backend's, off the wire); ask the backend if users need it
	}
	u, err := mygo.Updater.Check(ctx)
	if err != nil || u == nil {
		return "", err
	}
	return u.Version, nil
}

func (p *platform) UpdatesInstallThemselves() bool {
	return !packaged() && updater.AutomaticDownloads() // the Store's package: the Store's page installs it
}

// bodyView is the web view of the reader page page, while it is open as
// one: in bodies, registered as "reader", and bound (to message). UI
// thread.
func (p *platform) bodyView(page *mygo.Page) (v *mygo.WebView, message int64, ok bool) {
	v = p.bodies[page]
	if v == nil {
		return nil, 0, false
	}
	if role, ok := p.roles.Role(page); !ok || role != "reader" {
		return nil, 0, false
	}
	message, ok = p.reader.Bound(page)
	return v, message, ok
}

// loadAfterSetup readies an embedded page before it loads: setup
// (lockPage, its role, its binding) runs first, then load; when load
// fails, undo runs.
func loadAfterSetup(setup func(), load func() error, undo func()) error {
	setup()
	if err := load(); err != nil {
		undo()
		return err
	}
	return nil
}

// AllowImages lets message's reader pages load its remote images.
func (p *platform) AllowImages(message int64) { p.reader.Allow(message) }

func (p *platform) BodyGlass(view ui.NativeWebView, top, bottom int) {
	if v, ok := view.(*mygo.WebView); ok {
		if err := webui.ReaderGlassed.EmitPage(v.Page(), webui.ReaderGlass{Top: top, Bottom: bottom}); err != nil {
			log.Printf("the glass to a reader page: %v", err)
		}
	}
}

func (p *platform) TranslateBody(message int64, texts map[string]string) {
	p.reader.Translate(message, texts)
}

func (p *platform) Print(message int64) {
	for page, v := range p.bodies {
		if id, ok := p.reader.Bound(page); ok && id == message {
			v.Page().Print()
			return
		}
	}
}

func (p *platform) SettingsChanged(st core.Settings) {
	if !st.NotifyNewMail || !st.NotifyPreview {
		p.ShowSignInCode(nil)
	}
	if err := webui.ThemeChanged.Broadcast(webui.ThemeOf(st)); err != nil {
		log.Printf("theme to the pages: %v", err)
	}
	if p.wit != nil {
		p.wit.SettingsChanged(st)
	}
}

func (*platform) TypeIcon(ext string, size int) ([]byte, error) {
	return mygo.Shell.TypeIcon(ext, size)
}

func (*platform) TempDir() string {
	dir, err := mygo.App.Path(mygo.PathTemp)
	if err != nil {
		return "" // os.CreateTemp then uses the default
	}
	return dir
}

// trayBox is the tray icon's picture, w by h pixels at 2x, with the mark
// inkW wide. macOS shows it 18 points tall at its own aspect, so the wide,
// flat bird gets a wide picture, about 23 by 11 points where a square
// showed it 14 by 7; Windows squares the icon, so there it stays square.
type trayBox struct{ w, h, inkW float32 }

// closeMinimizes reports whether closing the window while it keeps running
// minimizes it rather than hiding it: without a tray icon, only macOS's Dock
// can bring back a hidden window, and a minimized one keeps its taskbar entry.
func closeMinimizes(goos string, hasTray bool) bool { return !hasTray && goos != "darwin" }

// closeExitsUI: closing the main window while Godwit keeps running in the
// background exits the interface process; the backend and its tray stay.
// shortcut: false hides the window instead, the interface staying; switch if
// the interface's cold start proves too slow (#738).
const closeExitsUI = true

// closeAction is what closing the main window does (mainCloseAction).
type closeAction int

const (
	closeQuit     closeAction = iota // the quit, which stops a backend the interface started
	closeExit                        // the interface quits; the backend stays
	closeHide                        // the window hides
	closeMinimize                    // the window minimizes
)

// mainCloseAction is what closing the main window does: with keep
// (App.KeepInBackground) off, the quit; on, the interface exits (exits:
// closeExitsUI) once none of its compose windows is open, hiding the window
// meanwhile, or else the window hides or minimizes (closeMinimizes).
func mainCloseAction(keep, exits bool, compose int, goos string, hasTray bool) closeAction {
	switch {
	case !keep:
		return closeQuit
	case exits && compose == 0:
		return closeExit
	case !exits && closeMinimizes(goos, hasTray):
		return closeMinimize
	}
	return closeHide
}

func trayBoxFor(goos string) trayBox {
	if goos == "darwin" {
		return trayBox{50, 36, 46}
	}
	return trayBox{32, 32, 30}
}

// logoInk is where the mark's ink sits in its 512 viewBox: x 44..487,
// y 144..351.
var logoInk = struct{ x0, y0, x1, y1 float32 }{44, 144, 488, 352}

// trayIcon draws the Godwit mark in black on transparent, as a macOS
// template image (the system tints it), its ink centred and box.inkW wide.
func trayIcon(box trayBox) ([]byte, error) {
	svg, err := ui.ParseSVG(assets.Logo)
	if err != nil {
		return nil, err
	}
	k := box.inkW / (logoInk.x1 - logoInk.x0) // px per viewBox unit
	img := ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Icon(c, svg).Size(512*k, 512*k).TextColor(ui.Color{A: 255}).Absolute().
			Left(box.w/2 - (logoInk.x0+logoInk.x1)/2*k).Top(box.h/2 - (logoInk.y0+logoInk.y1)/2*k)
	}, int(box.w), int(box.h), 1)
	var b bytes.Buffer
	err = png.Encode(&b, img)
	return b.Bytes(), err
}

// composeWindowOptions is a compose window on goos: the compose page of the
// frontend, the dev server's under `go tool mygo dev`, else web/dist's. On
// macOS its title bar is hidden, the traffic lights centered in the page's
// top bar as the main window's are; Title still names it in the Window menu.
func composeWindowOptions(goos string) mygo.WindowOptions {
	o := mygo.WindowOptions{URL: "/compose.html", Width: 860, Height: 720, MinWidth: 640, MinHeight: 480,
		StateKey: "compose", Title: i18n.T("compose.new")}
	if goos == "darwin" {
		// The main window's material; ?material tells the page to tint it rather than cover it.
		o.URL, o.Vibrancy = "/compose.html?material", mygo.VibrancySidebar
		o.Transparent = true
		o.TitleBarStyle = mygo.TitleBarHidden
		o.TrafficLightPosition = &mygo.Point{X: trafficLightInset, Y: (theme.TopBarHeight - trafficLightSize) / 2}
	}
	return o
}

// placed is a window with a position on the screen.
type placed interface {
	Position() (x, y int)
	SetPosition(x, y int)
}

// cascade places each new compose window (24, 24) past the last one still
// open, so it does not hide it; the first goes where its saved state says.
type cascade struct {
	mu   sync.Mutex
	open []placed
}

// place moves w past the last open window and counts it open.
func (c *cascade) place(w placed) {
	// ponytail: no check that the offset stays on the screen; clamp to the display if windows pile off it.
	c.mu.Lock()
	var last placed
	if n := len(c.open); n > 0 {
		last = c.open[n-1]
	}
	c.open = append(c.open, w)
	c.mu.Unlock()
	if last != nil {
		x, y := last.Position()
		w.SetPosition(x+24, y+24)
	}
}

// closed forgets w.
func (c *cascade) closed(w placed) {
	c.mu.Lock()
	c.open = slices.DeleteFunc(c.open, func(o placed) bool { return o == w })
	c.mu.Unlock()
}

// pickFiles asks for files to attach in a dialog on window winID; a window
// that is gone gets an error, never a dialog of its own.
func pickFiles(_ context.Context, winID int) ([]string, error) {
	w := mygo.WindowByID(winID)
	// ponytail: the window can still go between this check and the panel
	// showing, and then mygo shows it app-modal; harmless, since the files are
	// dropped (ErrNoDraft) once the window is released. Needs a mygo
	// Dialog.Open that fails when its parent is gone.
	if w == nil || w.IsDestroyed() {
		return nil, errors.New("no window to attach the file dialog to")
	}
	return mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w, Multiple: true})
}

// pickImage asks for one signature image in a dialog on the calling page's
// window; "" for none.
func pickImage(ctx context.Context) (string, error) {
	w := mygo.CallerWindow(ctx)
	if w == nil || w.IsDestroyed() {
		return "", errors.New("no window to attach the image dialog to")
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w,
		Filters: []mygo.FileFilter{{Name: "Images", Extensions: []string{"png", "jpg", "jpeg", "gif", "webp"}}}})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// composeWindow is webui.Window on mygo.
type composeWindow struct{ w *mygo.Window }

func (c composeWindow) ID() int { return c.w.ID() }
func (c composeWindow) Focus() {
	if c.w.IsMinimized() {
		c.w.Restore()
	}
	c.w.Show()
	c.w.Focus()
}
func (c composeWindow) Destroy() { c.w.Destroy() }
func (c composeWindow) EmitClosing(gen int) {
	if err := webui.Closing.Emit(c.w, gen); err != nil {
		log.Printf("closing compose window %d: %v", c.w.ID(), err)
	}
}

// newComposeWindow opens a compose window, placed by cas, whose closing goes through ws:
// closing is always prevented and asked of ws, and the page never leaves
// its entry nor opens windows.
func newComposeWindow(ws *webui.Windows, cas *cascade) webui.Window {
	w := mygo.NewWindow(composeWindowOptions(runtime.GOOS))
	cas.place(w)
	lockPage(w.Page())
	w.OnClose(func(e *mygo.CloseEvent) {
		e.PreventDefault()
		ws.RequestClose(w.ID())
	})
	w.OnClosed(func() {
		cas.closed(w)
		ws.Closed(w.ID())
	})
	return composeWindow{w}
}

// lockPage keeps p on its entry document and from opening windows (without
// a handler, http(s) links would open in the browser).
func lockPage(p *mygo.Page) {
	entry := "" // the first page committed: its own entry
	p.OnDidNavigate(func(u string) {
		if entry == "" {
			entry = u
		}
	})
	p.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !sameDocument(entry, e.URL) {
			e.PreventDefault()
		}
	})
	p.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
}

// sameDocument reports whether next is entry itself, give or take its query
// and fragment: the only navigation a compose page may make.
func sameDocument(entry, next string) bool {
	e, err1 := url.Parse(entry)
	n, err2 := url.Parse(next)
	return entry != "" && err1 == nil && err2 == nil && e.Scheme != "" &&
		strings.EqualFold(e.Scheme, n.Scheme) && strings.EqualFold(e.Host, n.Host) && e.Path == n.Path
}

// askUnsaved asks over w whether to close it with its last input unsaved:
// 「有修改没有保存」, 关闭 or 返回; true is 关闭.
func askUnsaved(w webui.Window) bool {
	var parent *mygo.Window
	if c, ok := w.(composeWindow); ok {
		parent = c.w
	}
	r, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning,
		Message: i18n.T("close.unsaved.title"), Buttons: []string{i18n.T("common.close"), i18n.T("close.unsaved.back")},
		DefaultButton: 1, CancelButton: 1})
	return err == nil && r.Button == 0
}

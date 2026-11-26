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
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/assets"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
	"github.com/godwit-email/godwit/webui"
)

// backendService is what the platform asks the backend: a message's sender
// avatar for notifications and sign-in banners, and the Store's latest version.
type backendService interface {
	SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (core.Avatar, error)
	StoreLatestVersion(ctx context.Context) (string, error)
}

// platform is app.Platform on mygo. win and tray are set once they exist;
// tray stays nil where the system has none, and in the interface, whose
// tray is the backend's.
type platform struct {
	win            *mygo.Window
	tray           *mygo.Tray
	windows        *webui.Windows // the compose windows
	wit            *webui.Wit
	roles          *webui.Pages      // the embedded pages' roles
	reader         *webui.Reader     // the reader pages' messages
	sig            *webui.Signature  // the signature page's signature and flushes
	bodies         map[int]*bodyView // live reader registrations; UI thread
	idleBody       *bodyView         // physical shell for reuse; its registration is revoked
	bodiesClosed   bool
	makeBody       func() (readerShell, error)
	signInCodes    []core.SignInCode
	codeIcons      map[int64]*mygo.MenuIcon // recent codes' menu icons by message; UI thread
	codeBanner     *signInBanner
	updateTray     func()
	setTrayVisible func(visible bool)
	trayQuitStop   bool        // 退出并停止收信 shows; UI thread
	trayUp         atomic.Bool // tray is set: read off the UI thread (TrayShown)

	dataDir      string
	svc          backendService
	saveMu       sync.Mutex
	saveDir      string                 // last directory chosen in SaveFile or OpenDirectory
	downloadsDir func() (string, error) // mygo.App.Path(PathDownloads); tests swap it
}

func (p *platform) DataDir() (string, bool) { return p.dataDir, !singleInstance(p.dataDir) }

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

// TrayShown reports whether the tray shows Godwit's icon: on, and the system
// has a tray (Linux may have no indicator host). Any goroutine.
func (p *platform) TrayShown() bool { return p.trayUp.Load() }

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
			p.trayUp.Store(false)
			return
		}
		if icon, err := trayIcon(trayBoxFor(runtime.GOOS)); err != nil {
			log.Print(err)
		} else if p.tray, err = mygo.NewTray(mygo.TrayOptions{Icon: icon, IconIsTemplate: true, ToolTip: "Godwit",
			Menu: menu()}); err != nil {
			log.Print(err) // no tray here (Linux without an indicator host)
		} else if p.trayUp.Store(true); runtime.GOOS == "windows" && act.open != nil {
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

// readerShell is the physical WebView. Only its first assignment loads the document.
type readerShell interface {
	ui.NativeWebView
	Page() *mygo.Page
	CapturePage() ([]byte, error)
	load() error
	signal(webui.ReaderBinding) error
	Destroy()
}

type nativeReaderShell struct{ *mygo.WebView }

func (s nativeReaderShell) load() error { return s.Page().LoadURL("/reader.html") }
func (s nativeReaderShell) signal(b webui.ReaderBinding) error {
	return webui.ReaderBound.EmitPage(s.Page(), b)
}

func (p *platform) newReaderShell() (readerShell, error) {
	// The body fills its own height; wheel and touchpad scroll the native reader.
	v, err := p.win.NewWebView(mygo.WebViewOptions{BackgroundColor: panelBackground(), ScrollToContainer: true})
	if err != nil {
		return nil, err
	}
	lockPage(v.Page())
	return nativeReaderShell{v}, nil
}

// bodyView is one assignment, also the native layout and screenshot cache key.
type bodyView struct {
	ui.NativeWebView
	shell readerShell
	id    int
	roles *webui.Pages
}

// CapturePage may wait for the UI thread while its shell is reassigned.
func (v *bodyView) CapturePage() ([]byte, error) {
	if _, ok := v.roles.Key(v.id); !ok {
		return nil, webui.ErrWrongPage
	}
	b, err := v.shell.CapturePage()
	if _, ok := v.roles.Key(v.id); !ok {
		return nil, webui.ErrWrongPage
	}
	return b, err
}

// NewBodyView assigns a shell to message. A cold shell is locked, registered
// and bound before its first load; a warm shell only hears ReaderBound. UI thread.
func (p *platform) NewBodyView(message int64, initial *core.Message) (ui.NativeWebView, func(bool), error) {
	if p.bodiesClosed {
		return nil, nil, webui.ErrWrongPage
	}
	var shell readerShell
	cold := p.idleBody == nil
	if cold {
		var err error
		shell, err = p.makeBody()
		if err != nil {
			return nil, nil, err
		}
		if p.bodiesClosed { // NewWebView may have run the event loop
			shell.Destroy()
			return nil, nil, webui.ErrWrongPage
		}
	} else {
		shell, p.idleBody = p.idleBody.shell, nil
	}
	id := p.roles.Register(shell.Page(), "reader")
	v := &bodyView{NativeWebView: shell, shell: shell, id: id, roles: p.roles}
	p.reader.Bind(id, message, initial)
	if p.bodies == nil {
		p.bodies = map[int]*bodyView{}
	}
	p.bodies[id] = v
	release := func(reuse bool) { p.releaseBody(id, reuse) }
	if cold {
		if err := shell.load(); err != nil {
			release(false)
			return nil, nil, err
		}
	}
	if p.bodies[id] != v { // shut down during the load
		return nil, nil, webui.ErrWrongPage
	}
	if err := shell.signal(webui.ReaderBinding{ID: id, Active: true}); err != nil {
		release(false)
		return nil, nil, err
	}
	return v, release, nil
}

func (p *platform) releaseBody(id int, reuse bool) {
	v := p.bodies[id]
	if v == nil {
		return // a repeated release cannot touch a later assignment
	}
	delete(p.bodies, id)
	// A retained native node must no longer place this shell. MyGo’s nil view places nothing.
	v.NativeWebView = (*mygo.WebView)(nil)
	keep := reuse && !p.bodiesClosed && p.idleBody == nil
	p.reader.Unbind(id, keep)
	p.roles.Unregister(v.shell.Page())
	if err := v.shell.signal(webui.ReaderBinding{ID: id}); err != nil {
		log.Printf("releasing reader page %d: %v", id, err)
		p.reader.Unbind(id, false)
		v.shell.Destroy()
	} else if keep {
		p.idleBody = v
	} else if reuse && !p.bodiesClosed && p.win != nil {
		// A swap can release several visible pages while building the frame.
		// Destroy surplus after that turn, not halfway through native placement.
		p.win.Update(v.shell.Destroy)
	} else {
		v.shell.Destroy()
	}
}

func (p *platform) closeBodyViews() {
	p.bodiesClosed = true
	for id := range p.bodies {
		p.releaseBody(id, false)
	}
	if v := p.idleBody; v != nil {
		p.idleBody = nil
		p.reader.Unbind(v.id, false)
		v.shell.Destroy()
	}
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
	if err := p.OpenURL("ms-windows-store://pdp/?ProductId=" + core.StoreProduct); err != nil {
		log.Printf("open the Store: %v", err)
	}
}
func (p *platform) LatestVersion(ctx context.Context) (string, error) {
	if packaged() {
		v, err := p.svc.StoreLatestVersion(ctx) // the backend's, so it goes through the proxy set in Godwit
		if err != nil || plugins.CompareVersions(v, app.Version) <= 0 {
			return "", err
		}
		return v, nil
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

func (p *platform) ReloadImages(message int64, manual bool) { p.reader.ReloadImages(message, manual) }

func (p *platform) BodyGlass(view ui.NativeWebView, top, bottom app.BodyGlassRect) {
	if v, ok := view.(*bodyView); ok && p.bodies[v.id] == v {
		g := webui.ReaderGlass{
			Binding: v.id,
			Top:     webui.ReaderGlassRect{X: top.X, Y: top.Y, W: top.W, H: top.H, R: top.R},
			Bottom:  webui.ReaderGlassRect{X: bottom.X, Y: bottom.Y, W: bottom.W, H: bottom.H, R: bottom.R},
		}
		if err := webui.ReaderGlassed.EmitPage(v.shell.Page(), g); err != nil {
			log.Printf("the glass to a reader page: %v", err)
		}
	}
}

func (p *platform) TranslateBody(message int64, texts map[string]string) {
	p.reader.Translate(message, texts)
}

func (p *platform) Print(message int64) {
	for id, v := range p.bodies {
		if m, ok := p.reader.Bound(id); ok && m == message {
			v.shell.Page().Print()
			return
		}
	}
}

func (p *platform) SettingsChanged(st core.Settings) {
	if err := webui.ThemeChanged.Broadcast(webui.ThemeOf(st)); err != nil {
		log.Printf("theme to the pages: %v", err)
	}
	if p.reader != nil {
		p.reader.SettingsChanged(st)
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

// hiddenExitAfter is how long the main window stays hidden after its close
// before the interface exits, the backend and its tray staying: reopening
// sooner is instant, later a cold start, but the interface's memory is freed
// (ADR 0038, #738).
const hiddenExitAfter = 5 * time.Minute

// closeAction is what closing the main window does (mainCloseAction).
type closeAction int

const (
	closeQuit     closeAction = iota // the quit, which stops a backend the interface started
	closeExit                        // the interface quits; the backend stays
	closeHide                        // the window hides
	closeMinimize                    // the window minimizes
)

// mainCloseAction is what closing the main window does: with keep
// (App.KeepInBackground) off, the quit; on, the window hides or minimizes
// (closeMinimizes) until it stayed so for hiddenExitAfter (expired), then the
// interface exits once none of its compose windows is open.
func mainCloseAction(keep, expired bool, compose int, goos string, hasTray bool) closeAction {
	switch {
	case !keep:
		return closeQuit
	case expired && compose == 0:
		return closeExit
	case expired:
		return closeHide // already hidden; decided again once the last compose window closes
	case closeMinimizes(goos, hasTray):
		return closeMinimize
	}
	return closeHide
}

// mainClose is closing the main window over time: close hides it and starts
// the hiddenExitAfter timer, whose firing closes again, expired, if the
// window is still hidden; shown cancels it. Used on the UI thread: after's f
// must run there.
type mainClose struct {
	action  func(expired bool) closeAction // mainCloseAction now
	do      func(closeAction)
	hidden  func() bool                                        // the main window is hidden or minimized
	busy    func() bool                                        // work runs that the exit would end (App.Busy); nil: none
	after   func(d time.Duration, f func()) (stop func() bool) // app.Clock.AfterFunc
	closed  bool                                               // the window closed and has not shown since
	expired bool
	stop    func() bool
	gen     int // voids a timer that fired after shown stopped it
}

// close is the window's close, or deciding it again while closed (a compose
// window went, the timer fired).
func (m *mainClose) close() {
	m.closed = true
	act := m.action(m.expired)
	if (act == closeHide || act == closeMinimize) && !m.expired && m.stop == nil {
		gen := m.gen
		m.stop = m.after(hiddenExitAfter, func() {
			if gen != m.gen {
				return
			}
			m.stop = nil
			if !m.hidden() { // shown without telling us
				m.shown()
				return
			}
			if m.busy != nil && m.busy() {
				m.close() // a download or import would end with the interface: hidden another round
				return
			}
			m.expired = true
			m.close()
		})
	}
	m.do(act)
}

// shown is the window showing again: the interface stays.
func (m *mainClose) shown() {
	if m.stop != nil {
		m.stop()
		m.stop = nil
	}
	m.closed, m.expired = false, false
	m.gen++
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
// frontend, the dev server's under `go tool mygo dev`, else web/dist's. It
// matches the main window's custom title bar chrome on every platform so
// Windows and Linux do not stack a native title bar and menu strip above
// ComposeView's own header bar; Title still names it in the Window menu.
func composeWindowOptions(goos string) mygo.WindowOptions {
	o := mygo.WindowOptions{URL: "/compose.html", Width: 860, Height: 720, MinWidth: 640, MinHeight: 480,
		StateKey: "compose", Title: i18n.T("compose.new"), TitleBarStyle: mygo.TitleBarHiddenInset}
	switch goos {
	case "darwin":
		// The main window's material; ?material tells the page to tint it rather than cover it.
		o.URL, o.Vibrancy = "/compose.html?material", mygo.VibrancySidebar
		o.Transparent = true
		o.TitleBarStyle = mygo.TitleBarHidden
		o.TrafficLightPosition = &mygo.Point{X: trafficLightInset, Y: (theme.TopBarHeight - trafficLightSize) / 2}
	case "windows":
		o.URL, o.Vibrancy, o.TitleBarHeight = "/compose.html?material", mygo.VibrancyMica, 40
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

// composeFrames are the open compose windows' native layers, by window ID.
type composeFrames struct {
	mu sync.Mutex
	m  map[int]*app.ComposeFrame
}

func (fs *composeFrames) get(win int) *app.ComposeFrame {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.m[win]
}

func (fs *composeFrames) set(win int, f *app.ComposeFrame) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.m == nil {
		fs.m = map[int]*app.ComposeFrame{}
	}
	if f == nil {
		delete(fs.m, win)
	} else {
		fs.m[win] = f
	}
}

// newComposeWindow opens a compose window, placed by cas, whose closing goes through ws:
// closing is always prevented and asked of ws, and the page never leaves
// its entry nor opens windows. Native draws the window: its title bar, the
// page's web view under it and the toasts over it (app.ComposeFrame, by view);
// frames keeps the frame while the window is open.
func newComposeWindow(ws *webui.Windows, cas *cascade, frames *composeFrames, view func(*app.ComposeFrame) func(*ui.Context)) webui.Window {
	opts := composeWindowOptions(runtime.GOOS)
	entry := opts.URL
	// Set after NewWindow, which called off the main thread returns while the
	// main thread draws the window already: an atomic, not a plain variable.
	var draw atomic.Pointer[func(*ui.Context)]
	opts.URL, opts.Content = "", ui.View(func(c *ui.Context) {
		if d := draw.Load(); d != nil {
			(*d)(c)
		}
	})
	w := mygo.NewWindow(opts)
	cas.place(w)
	if v, err := w.NewWebView(mygo.WebViewOptions{Transparent: true}); err != nil {
		log.Printf("the compose page: %v", err) // the window stays blank
	} else {
		f := app.NewComposeFrame(v, w.Invalidate)
		frames.set(w.ID(), f)
		d := view(f)
		draw.Store(&d)
		lockPage(v.Page())
		if err := v.Page().LoadURL(entry); err != nil {
			log.Printf("the compose page: %v", err)
		}
		v.Focus()
		w.Invalidate()
	}
	w.OnClose(func(e *mygo.CloseEvent) {
		e.PreventDefault()
		ws.RequestClose(w.ID())
	})
	w.OnClosed(func() {
		if f := frames.get(w.ID()); f != nil {
			f.Close() // a send later still open answers the page 0
		}
		frames.set(w.ID(), nil)
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

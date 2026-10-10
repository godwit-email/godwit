package main

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/assets"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/godwit-email/godwit/webui"
)

// platform is app.Platform on mygo. win and tray are set once they exist;
// tray stays nil where the system has none. The Local API serves svc on
// dataDir, whose owner lock the GUI holds.
type platform struct {
	win     *mygo.Window
	tray    *mygo.Tray
	windows *webui.Windows // the compose windows

	dataDir string
	svc     *core.Service
	apiMu   sync.Mutex
	api     *rpc.Server // nil while the Local API is off
	apiGen  uint64      // the newest request that asked for api

	onAPIStop func(gen uint64, err error)              // told when the server ends by itself
	serve     func(context.Context, *rpc.Server) error // Server.Serve; tests swap it
	closeAPI  func(*rpc.Server) error                  // Server.Close; tests swap it
}

func (p *platform) DataDir() (string, bool) { return p.dataDir, !singleInstance(p.dataDir) }

func (p *platform) SetLocalAPI(gen uint64, on bool) (string, error) {
	p.apiMu.Lock()
	defer p.apiMu.Unlock()
	if !on {
		if p.api == nil {
			return "", nil
		}
		err := p.close(p.api)
		p.api = nil
		return "", err
	}
	if p.api == nil {
		srv, err := rpcbind.Listen(p.dataDir, p.svc, app.Version)
		if err != nil {
			return "", err
		}
		p.api = srv
		serve := p.serve
		if serve == nil {
			serve = func(ctx context.Context, s *rpc.Server) error { return s.Serve(ctx) }
		}
		go p.serveAPI(srv, serve)
	}
	p.apiGen = gen
	return p.api.Path(), nil
}

func (p *platform) close(srv *rpc.Server) error {
	if p.closeAPI != nil {
		return p.closeAPI(srv)
	}
	return srv.Close()
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
func (p *platform) OpenCompose(draftID int64) { p.windows.Open(draftID) }
func (*platform) SettingsChanged(st core.Settings) {
	if err := webui.ThemeChanged.Broadcast(webui.ThemeOf(st)); err != nil {
		log.Printf("theme to the pages: %v", err)
	}
}
func (*platform) TempDir() string {
	dir, err := mygo.App.Path(mygo.PathTemp)
	if err != nil {
		return "" // os.CreateTemp then uses the default
	}
	return dir
}

// serveAPI serves srv until it closes. If it ends while still the current
// server, which no SetLocalAPI(false) asked for, it closes srv and forgets
// it under apiMu, so a newer server cannot start before srv's files are
// gone, then tells the App which request the server was for.
func (p *platform) serveAPI(srv *rpc.Server, serve func(context.Context, *rpc.Server) error) {
	err := serve(context.Background(), srv)
	p.apiMu.Lock()
	if p.api != srv {
		p.apiMu.Unlock()
		return // closed on request
	}
	p.api = nil
	err = errors.Join(err, p.close(srv))
	gen := p.apiGen
	p.apiMu.Unlock()
	if err == nil {
		err = errors.New("the Local API server stopped")
	}
	log.Print(p.svc.Redact("local API: " + err.Error()))
	if p.onAPIStop != nil {
		p.onAPIStop(gen, err)
	}
}

// trayBox is the tray icon's picture, w by h pixels at 2x, with the mark
// inkW wide. macOS shows it 18 points tall at its own aspect, so the wide,
// flat bird gets a wide picture, about 23 by 11 points where a square
// showed it 14 by 7; Windows squares the icon, so there it stays square.
type trayBox struct{ w, h, inkW float32 }

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

// composeWindowOptions is a compose window: the compose page of the
// frontend, the dev server's under `go tool mygo dev`, else web/dist's.
func composeWindowOptions() mygo.WindowOptions {
	return mygo.WindowOptions{URL: "/compose.html", Width: 860, Height: 720, MinWidth: 640, MinHeight: 480,
		StateKey: "compose", Title: "新邮件"}
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

// newComposeWindow opens a compose window whose closing goes through ws:
// closing is always prevented and asked of ws, and the page never leaves
// its entry nor opens windows.
func newComposeWindow(ws *webui.Windows) webui.Window {
	w := mygo.NewWindow(composeWindowOptions())
	entry := "" // the first page the window committed: its own entry
	w.Page().OnDidNavigate(func(u string) {
		if entry == "" {
			entry = u
		}
	})
	w.Page().OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !sameDocument(entry, e.URL) {
			e.PreventDefault()
		}
	})
	w.Page().SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	w.OnClose(func(e *mygo.CloseEvent) {
		e.PreventDefault()
		ws.RequestClose(w.ID())
	})
	w.OnClosed(func() { ws.Closed(w.ID()) })
	return composeWindow{w}
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

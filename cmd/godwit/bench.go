package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
)

// benchProbe logs window lifecycle timestamps to GODWIT_BENCH_LOG and listens
// on an authenticated loopback socket for close/open/quit commands without
// polling timers (0 background wakeups). When GODWIT_BENCH_LOG is unset it is nil.
type benchProbe struct {
	logPath string
	token   string

	mu           sync.Mutex
	ln           net.Listener
	shown        bool
	readyLogged  bool
	hiddenLogged bool
}

func newBenchProbe(logPath string) *benchProbe {
	if logPath == "" {
		return nil
	}
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return &benchProbe{logPath: logPath, token: hex.EncodeToString(raw[:])}
}

func (b *benchProbe) logEvent(event string, extra ...string) {
	if b == nil {
		return
	}
	f, err := os.OpenFile(b.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("bench log: %v", err)
		return
	}
	defer f.Close()
	line := fmt.Sprintf("%s %d pid=%d", event, time.Now().UnixNano(), os.Getpid())
	if len(extra) > 0 {
		line += " " + strings.Join(extra, " ")
	}
	_, _ = fmt.Fprintln(f, line)
}

func (b *benchProbe) onShow() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.shown {
		b.shown = true
		b.readyLogged = false
		b.hiddenLogged = false
	}
	b.mu.Unlock()
}

func (b *benchProbe) onFrame(ready bool) {
	if b == nil || !ready {
		return
	}
	b.mu.Lock()
	emit := b.shown && !b.readyLogged
	if emit {
		b.readyLogged = true
	}
	b.mu.Unlock()
	if emit {
		b.logEvent("mail_ready")
	}
}

func (b *benchProbe) onHide() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.shown = false
	b.readyLogged = false
	emit := !b.hiddenLogged
	b.hiddenLogged = true
	b.mu.Unlock()
	if emit {
		b.logEvent("window_hidden")
	}
}

func (b *benchProbe) listen(onCmd func(cmd string)) error {
	if b == nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.ln = ln
	b.mu.Unlock()
	b.logEvent("bench_ctl", "addr="+ln.Addr().String(), "token="+b.token)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(conn, 256)).ReadString('\n') // a command is short; the token is checked after
			_ = conn.Close()
			if err != nil && line == "" {
				continue
			}
			tok, cmd, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(b.token)) != 1 {
				continue
			}
			onCmd(strings.TrimSpace(cmd))
		}
	}()
	return nil
}

func (b *benchProbe) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	ln := b.ln
	b.ln = nil
	b.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (b *benchProbe) initApp(dataDir string) {
	if b == nil {
		return
	}
	if !singleInstance(dataDir) {
		mygo.App.SetPath(mygo.PathUserData, dataDir)
	}
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
}

func (b *benchProbe) configureWindow(opts *mygo.WindowOptions) {
	if b == nil {
		return
	}
	opts.Hidden = true
	opts.DisableFullScreen = true
	opts.Opacity = 0.01
}

func (b *benchProbe) view(a *app.App) mygo.Content {
	if b == nil {
		return ui.View(a.View)
	}
	return ui.View(func(c *ui.Context) {
		a.View(c)
		b.onFrame(a.MailReady())
	})
}

func (b *benchProbe) bindWindow(win *mygo.Window, a *app.App) {
	if b == nil {
		return
	}
	win.SetVisibleOnAllWorkspaces(true)
	win.OnShow(func() {
		b.onShow()
		a.SetForeground(true)
	})
	win.OnHide(func() {
		b.onHide()
		a.SetForeground(false)
	})
	win.OnClosed(func() { b.onHide() })
	b.onShow()
	win.ShowInactive()
	a.SetForeground(true)
	if err := b.listen(func(cmd string) {
		switch cmd {
		case "close":
			win.Close()
		case "open":
			win.Update(func() {
				if win.IsDestroyed() {
					return
				}
				if win.IsMinimized() {
					win.Restore()
				}
				b.onShow()
				win.ShowInactive()
				a.SetForeground(true)
			})
		case "quit":
			mygo.App.Quit()
		}
	}); err != nil {
		log.Printf("bench control listener: %v", err)
	}
}

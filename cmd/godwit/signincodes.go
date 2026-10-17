package main

import (
	"context"
	"encoding/json"
	"log"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

const codeBannerWidth, codeBannerHeight, codeBannerInset = 320, 48, 24

func signInCodePosition(area mygo.Rectangle, displays []mygo.Display, saved *mygo.Point) mygo.Point {
	if saved != nil {
		for _, display := range displays {
			a := display.WorkArea
			if saved.X >= a.X && saved.X < a.X+a.Width && saved.Y >= a.Y && saved.Y < a.Y+a.Height {
				return mygo.Point{
					X: max(a.X, min(saved.X, a.X+a.Width-codeBannerWidth)),
					Y: max(a.Y, min(saved.Y, a.Y+a.Height-codeBannerHeight)),
				}
			}
		}
	}
	return mygo.Point{X: area.X + max(0, (area.Width-codeBannerWidth)/2), Y: area.Y + max(0, area.Height-codeBannerHeight-codeBannerInset)}
}

const codeBannerSnap = 16 // how close, in points, a drag comes before the banner holds still

// snapSignInCode holds a dragged banner on the work area's vertical centre
// line, and on its default spot there, once it comes within codeBannerSnap.
func snapSignInCode(pos mygo.Point, area mygo.Rectangle) mygo.Point {
	home := signInCodePosition(area, nil, nil)
	if abs(pos.X-home.X) <= codeBannerSnap {
		pos.X = home.X
		if abs(pos.Y-home.Y) <= codeBannerSnap {
			pos.Y = home.Y
		}
	}
	return pos
}

func abs(n int) int { return max(n, -n) }

func loadSignInCodePosition(path string) *mygo.Point {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("loading code banner position: %v", err)
		}
		return nil
	}
	var position *mygo.Point
	if err := json.Unmarshal(data, &position); err != nil {
		log.Printf("loading code banner position: %v", err)
		return nil
	}
	return position
}

func saveSignInCodePosition(path string, position mygo.Point) error {
	data, err := json.Marshal(position)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600) // a torn write only loses the position: loading falls back to the default
}

func freshSignInCode(code core.SignInCode, now time.Time) bool {
	return now.Before(code.ReceivedAt.Add(15 * time.Minute))
}

func codeSender(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if a.Name != "" {
			return a.Name
		}
		return a.Address
	}
	return from
}

// groupCode splits a six-digit code in two, as people read it.
func groupCode(code string) string {
	if len(code) == 6 && strings.Trim(code, "0123456789") == "" {
		return code[:3] + " " + code[3:]
	}
	return code
}

// shortcut: plain-text items until MyGo menu items take an icon and a subtitle (#95).
func recentCodeMenu(codes []core.SignInCode, now time.Time, use func(core.SignInCode)) []*mygo.MenuItem {
	var items []*mygo.MenuItem
	for _, code := range codes {
		if !freshSignInCode(code, now) {
			continue
		}
		label := code.Code + " · " + codeSender(code.From)
		if code.Code == "" {
			label = i18n.Tf("codes.link_item", gomail.LinkHost(code.Link), codeSender(code.From))
		}
		items = append(items, &mygo.MenuItem{Label: label, Click: func(*mygo.MenuItem, *mygo.Window) { use(code) }})
	}
	if len(items) == 0 {
		items = append(items, &mygo.MenuItem{Label: i18n.T("codes.empty"), Disabled: true})
	}
	return []*mygo.MenuItem{mygo.Separator(), {Label: i18n.T("codes.recent"), Submenu: items}}
}

func (p *platform) SetSignInCodes(codes []core.SignInCode) {
	p.signInCodes = slices.Clone(codes)
	if p.updateTray != nil {
		p.updateTray()
	}
	if p.codeBanner != nil && !slices.ContainsFunc(codes, func(c core.SignInCode) bool { return c.MessageID == p.codeBanner.code.MessageID }) {
		p.ShowSignInCode(nil)
	}
}

// useSignInCode copies the code, or opens a link-only entry's link.
func (p *platform) useSignInCode(code core.SignInCode) {
	b := p.codeBanner
	if b != nil && b.code.MessageID != code.MessageID {
		b = nil
	}
	if !freshSignInCode(code, time.Now()) {
		p.ShowSignInCode(nil)
		return
	}
	if code.Code == "" {
		if err := p.OpenURL(code.Link); err != nil {
			log.Printf("opening sign-in link: %v", err)
		}
		p.ShowSignInCode(nil)
		return
	}
	if err := mygo.Clipboard.Write(transfer.TextData(code.Code)); err != nil {
		log.Printf("copying sign-in code: %v", err)
		if b != nil {
			b.failed = true
		}
		return
	}
	if b == nil {
		return
	}
	// "Copied" stays a moment so the click is seen, then the banner goes.
	b.failed, b.copied = false, true
	b.timer.Reset(time.Second)
}

type signInBanner struct {
	code                      core.SignInCode
	win                       *mygo.Window
	timer                     *time.Timer
	failed, copied, hovered   bool
	dragging, snapped, atHome bool
	guide                     *mygo.Window   // the centre line shown while a drag is held on it
	dragFrom, dragStart       mygo.Point     // the cursor and the banner where the drag began
	avatar                    ui.ImageSource // the sender's image once loaded; nil keeps the initial
	stopAvatar                context.CancelFunc
	position                  mygo.Point
}

func (p *platform) SignInCodeBannerSupported() bool { return p.tray != nil }

func (p *platform) signInCodeView(c *ui.Context, b *signInBanner) {
	t, th := theme.Of(c), c.Theme()
	c.Root().Background(ui.Transparent)
	ui.Box(c).Fill().Children(func() {
		if !c.Vibrancy() {
			// shortcut: MyGo's glass cannot blur other windows; use translucency where native backdrop blur is unavailable.
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Radius(t.Radius.X2l).
				Material(glass.Glass{Style: glass.Clear, Tint: t.Background}).Opacity(0.28).PassThrough()
		}
		row := ui.Row(c).Fill().Padding(th.Space(2)).Gap(th.Space(3)).AlignItems(ui.Center)
		p.dragSignInCode(c, b, row.Pressed())
		// The countdown waits while the pointer rests on the banner.
		if h := row.Hovered(); h != b.hovered && !b.copied && b.timer != nil {
			b.hovered = h
			if h {
				b.timer.Stop()
			} else {
				b.timer.Reset(12 * time.Second)
			}
		}
		row.Children(func() {
			sender := codeSender(b.code.From)
			kit.AvatarImage(c, sender, b.avatar)
			action, icon := i18n.T("codes.copy_button"), "copy"
			if b.code.Code != "" {
				ui.Text(c, groupCode(b.code.Code)).Font(theme.Mono).FontSize(t.Text.X2l).FontWeight(500).Shrink(0)
				note, noteColor := sender, t.Muted
				switch {
				case b.copied:
					action, icon = i18n.T("codes.copied"), "check"
				case b.failed:
					note, noteColor, action = i18n.T("codes.copy_failed"), t.DangerText, i18n.T("codes.retry")
				}
				kit.Text(c, note, t.Text.Base).TextColor(noteColor).Grow(1).Shrink(1).SingleLine()
			} else {
				ui.Column(c).Grow(1).Shrink(1).Children(func() {
					kit.Text(c, i18n.T("codes.link"), t.Text.Base).FontWeight(500).SingleLine()
					kit.Text(c, sender+" · "+gomail.LinkHost(b.code.Link), t.Text.Sm).TextColor(t.Muted).SingleLine()
				})
				action, icon = i18n.T("codes.open"), "external-link"
			}
			btn := kit.Button(c, action, kit.ButtonOpts{Variant: kit.Ghost, Icon: icon}).Shrink(0)
			switch {
			case b.copied:
				btn.Background(ui.Transparent).TextColor(t.SuccessText)
			case btn.Hovered():
				btn.Background(t.Foreground.Alpha(0.1))
			default:
				btn.Background(t.Foreground.Alpha(0.06))
			}
			btn.OnClick(func() { p.useSignInCode(b.code) })
		})
	})
}

// dragSignInCode moves the banner with the pointer, as DragWindow would,
// but snaps it (snapSignInCode) on the way, which a system drag cannot.
func (p *platform) dragSignInCode(c *ui.Context, b *signInBanner, pressed bool) {
	if !pressed {
		if b.dragging {
			b.dragging, b.snapped, b.atHome = false, false, false
			p.showSnapGuide(b, false, b.position)
		}
		return
	}
	cursor := mygo.Screen.CursorScreenPoint()
	if !b.dragging {
		b.dragging, b.dragFrom, b.dragStart = true, cursor, b.position
	}
	pos := mygo.Point{X: b.dragStart.X + cursor.X - b.dragFrom.X, Y: b.dragStart.Y + cursor.Y - b.dragFrom.Y}
	area := mygo.Screen.DisplayNearestPoint(cursor).WorkArea
	pos = snapSignInCode(pos, area)
	if pos != b.position && b.win != nil {
		b.win.SetPosition(pos.X, pos.Y) // OnMove records it
	}
	home := signInCodePosition(area, nil, nil)
	onLine, atHome := pos.X == home.X, pos == home
	if onLine && !b.snapped || atHome && !b.atHome {
		snapFeedback() // a tick as it takes hold, as Keynote's guides do
	}
	b.snapped, b.atHome = onLine, atHome
	p.showSnapGuide(b, onLine, pos)
	c.Invalidate() // the pointer moves without telling the view; follow it every frame
}

const codeGuideReach = 40 // how far the centre line runs past the banner, above and below

// showSnapGuide shows the centre line through a banner held on it, as
// design tools draw their alignment guides, and hides it otherwise.
func (p *platform) showSnapGuide(b *signInBanner, on bool, pos mygo.Point) {
	if !on || b.win == nil {
		if b.guide != nil {
			b.guide.Hide()
		}
		return
	}
	if b.guide == nil {
		b.guide = mygo.NewFlyout(mygo.FlyoutOptions{Tray: p.tray, Width: 1, Height: codeBannerHeight + 2*codeGuideReach, Hidden: true,
			Content: ui.View(func(c *ui.Context) {
				c.Root().Background(ui.Transparent)
				ui.Box(c).Fill().Background(theme.Of(c).Unread)
			})})
		b.guide.SetIgnoreMouseEvents(true)
	}
	b.guide.SetPosition(pos.X+codeBannerWidth/2, pos.Y-codeGuideReach)
	if !b.guide.IsVisible() {
		b.guide.ShowInactive()
	}
}

// signInAvatar is the sender's image as the reader shows it. Without the
// user's per-message permission, the message's remote-image policy decides.
func signInAvatar(ctx context.Context, svc *core.Service, id int64) ui.ImageSource {
	av, err := svc.SenderAvatar(ctx, id, false)
	if err != nil || len(av.Data) == 0 {
		if err != nil && ctx.Err() == nil {
			log.Printf("sign-in code avatar: %v", err)
		}
		return nil
	}
	var img ui.ImageSource
	if av.Format == "svg" {
		img, err = ui.ParseSVG(av.Data)
	} else {
		img, err = ui.DecodeBitmap(av.Data)
	}
	if err != nil {
		log.Printf("sign-in code avatar: %v", err)
		return nil
	}
	return img
}

// A tray flyout with Focusable=false keeps the target app's keyboard even
// when Copy is clicked. A normal ShowInactive window would activate on click.
func (p *platform) ShowSignInCode(code *core.SignInCode) {
	if b := p.codeBanner; b != nil {
		p.codeBanner = nil
		b.timer.Stop()
		b.win.Close()
	}
	if code == nil || p.tray == nil || !freshSignInCode(*code, time.Now()) {
		return
	}
	b := &signInBanner{code: *code}
	b.win = mygo.NewFlyout(mygo.FlyoutOptions{Tray: p.tray, Placement: mygo.PlacementBottomEnd,
		Width: codeBannerWidth, Height: codeBannerHeight, Hidden: true, Shadow: true, Content: ui.View(func(c *ui.Context) {
			p.signInCodeView(c, b)
		})})
	signInCodeMaterial(b.win)
	path := filepath.Join(p.dataDir, "code-banner-position.json")
	b.position = signInCodePosition(mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint()).WorkArea, mygo.Screen.Displays(), loadSignInCodePosition(path))
	b.win.SetPosition(b.position.X, b.position.Y)
	initialPosition := b.position
	b.timer = time.AfterFunc(12*time.Second, func() {
		p.win.Update(func() {
			if p.codeBanner == b {
				p.ShowSignInCode(nil)
			}
		})
	})
	p.codeBanner = b
	ctx, stop := context.WithCancel(context.Background())
	b.stopAvatar = stop
	if p.svc != nil {
		go func() {
			if img := signInAvatar(ctx, p.svc, code.MessageID); img != nil && ctx.Err() == nil {
				b.win.Update(func() { b.avatar = img })
			}
		}()
	}
	b.win.OnMove(func() {
		b.position.X, b.position.Y = b.win.Position()
		if !b.hovered && !b.copied { // a drag ends under the pointer, where the countdown waits
			b.timer.Reset(12 * time.Second)
		}
	})
	b.win.OnClosed(func() {
		b.timer.Stop()
		b.stopAvatar()
		if b.guide != nil {
			b.guide.Close()
		}
		if b.position != initialPosition {
			if err := saveSignInCodePosition(path, b.position); err != nil {
				log.Printf("saving code banner position: %v", err)
			}
		}
		if p.codeBanner == b {
			p.codeBanner = nil
		}
	})
	b.win.ShowInactive()
}

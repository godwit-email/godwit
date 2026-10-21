package app

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// syncFail is an account's failing sync: the latest error, its kind (core/conn's
// FailureKind: dns, connect, timeout, tls, auth, protocol), and when the failures began.
type syncFail struct {
	err, kind string
	since     time.Time
}

// syncGiveUp is how long a failure that may clear by itself is retried
// before the status turns red and asks the user to look.
const syncGiveUp = 30 * time.Minute

// syncLevel orders accounts in the sync status: the top bar names the worst.
type syncLevel int

const (
	syncOK       syncLevel = iota
	syncBusy               // syncing now
	syncRetrying           // failing, retried on its own (amber)
	syncAction             // needs the user (red)
)

// syncHealth is what the sync status says of one account.
type syncHealth struct {
	acc     core.Account
	level   syncLevel
	status  string // 自动重试中, 需要重新登录…
	short   string // the top bar's word for it; empty: status
	reason  string // what happened, in plain words
	err     string // the raw error, on hover over the reason
	actions []syncButton
}

type syncButton struct {
	label   string
	primary bool
	do      func()
}

func (a *App) syncHealth(acc core.Account) syncHealth {
	h := syncHealth{acc: acc}
	f, failed := a.syncErr[acc.ID]
	h.err = f.err
	id, host := acc.ID, acc.IMAPHost
	retry := syncButton{label: i18n.T("shell.sync.retry"), do: func() {
		a.do(func(ctx context.Context) error { return a.svc.SyncNow(ctx, id) }, a.toastErr)
	}}
	servers := syncButton{label: i18n.T("shell.sync.servers"), do: func() { a.navigate(Route{Page: PageSettings, Settings: accountRoute(id)}) }}
	switch {
	case acc.Status == "reauth_required" || failed && f.kind == "auth":
		h.level, h.actions = syncAction, []syncButton{a.signInButton(acc)}
		secret := secretName(accountProvider(a, acc))
		switch {
		case acc.AuthType == "oauth":
			h.status, h.reason = i18n.T("shell.sync.reauth"), i18n.T("shell.sync.reauth.reason")
		case acc.StatusDetail == "credentials_missing":
			h.status, h.reason = i18n.Tf("shell.sync.missing", secret), i18n.Tf("shell.sync.missing.reason", secret)
		default:
			h.status, h.reason = i18n.Tf("shell.sync.rejected", secret), i18n.Tf("shell.sync.rejected.reason", secret)
		}
	case acc.Status != "ok": // the keychain is unavailable
		h.level, h.status, h.reason, h.actions = syncAction, i18n.T("shell.sync.keychain"), i18n.T("shell.sync.keychain.reason"), []syncButton{retry}
	case failed && f.kind == "tls": // a bad certificate does not fix itself
		h.level, h.status, h.short = syncAction, i18n.T("shell.sync.tls"), i18n.T("shell.sync.failed")
		h.reason, h.actions = i18n.Tf("shell.sync.tls.reason", host), []syncButton{retry, servers}
	case failed && a.clock.Now().Sub(f.since) >= syncGiveUp:
		h.level, h.status = syncAction, i18n.T("shell.sync.failed")
		h.reason, h.actions = i18n.Tf("shell.sync.stuck.reason", a.ago(f.since), failCause(f.kind, host)), []syncButton{retry, servers}
	case failed:
		h.level, h.status, h.short = syncRetrying, i18n.T("shell.sync.retrying"), i18n.T("shell.sync.retrying.short")
		h.reason, h.actions = i18n.Tf("shell.sync.retrying.reason", failCause(f.kind, host)), []syncButton{retry}
	case a.syncing[id]:
		h.level, h.status = syncBusy, i18n.T("shell.sync.syncing")
	default:
		if at, ok := a.syncedAtBy[id]; ok {
			h.status = i18n.Tf("shell.sync.synced", a.ago(at))
		}
	}
	return h
}

// failCause says what a failure of kind was: 连不上 imap.example.com…
func failCause(kind, host string) string {
	switch kind {
	case "dns", "connect", "timeout":
		return i18n.Tf("shell.sync.cause."+kind, host)
	}
	return i18n.Tf("shell.sync.cause.other", host)
}

// signInButton signs in again in the browser (OAuth), or opens the account's
// password dialog.
func (a *App) signInButton(acc core.Account) syncButton {
	p := a.pages[PageSettings].(*settingsPage)
	if acc.AuthType == "oauth" {
		return syncButton{label: i18n.T("onboarding.relogin"), primary: true, do: func() { p.startReauth(acc) }}
	}
	return syncButton{label: i18n.Tf("settings.account.login.update", secretName(accountProvider(a, acc))), primary: true, do: func() {
		a.navigate(Route{Page: PageSettings, Settings: accountRoute(acc.ID)})
		p.openDialog("password", acc) // after navigate, whose refresh closes dialogs
	}}
}

// syncHealths is every account's health, the worst first.
func (a *App) syncHealths() []syncHealth {
	hs := make([]syncHealth, len(a.accounts))
	for i, acc := range a.accounts {
		hs[i] = a.syncHealth(acc)
	}
	slices.SortStableFunc(hs, func(x, y syncHealth) int { return cmp.Compare(y.level, x.level) })
	return hs
}

// syncDot is the dot for a level; ok is false when it has none.
func syncDot(l syncLevel) (s kit.Status, ok bool) {
	switch l {
	case syncAction:
		return kit.Error, true
	case syncRetrying:
		return kit.Attention, true
	case syncOK:
		return kit.OK, true
	}
	return 0, false
}

// syncStatus is the top bar's sync status: the worst account by name when
// one fails, else syncing or how long ago the last sync finished. A click
// lists every account's state.
func (a *App) syncStatus(c *ui.Context) {
	hs := a.syncHealths()
	if len(hs) == 0 {
		return
	}
	t, th := theme.Of(c), c.Theme()
	w := hs[0]
	text, color := "", t.Muted
	switch w.level {
	case syncAction:
		text, color = i18n.Tf("shell.sync.account", accountName(w.acc), cmp.Or(w.short, w.status)), t.DangerText
	case syncRetrying:
		text, color = i18n.Tf("shell.sync.account", accountName(w.acc), w.short), t.WarningText
	case syncBusy:
		text = w.status
	default:
		if a.syncedAt.IsZero() {
			return
		}
		text = a.syncedAgo()
	}
	chip := kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).FontSize(t.Text.Sm).TextColor(color).
		Gap(th.Space(1.5)).Expanded(a.syncOpen).Label(text)
	chip.OnClick(func() { a.syncOpen = !a.syncOpen })
	chip.Children(func() {
		if s, ok := syncDot(w.level); ok {
			kit.StatusDot(c, s)
		}
		ui.Text(c, text).SingleLine().Shrink(1)
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconXs, theme.IconXs)
	})
	kit.Popover(c, chip, &a.syncOpen, func() { a.syncMenu(c, hs) })
}

// syncMenu lists each account: its state, why, and what to do about it.
func (a *App) syncMenu(c *ui.Context, hs []syncHealth) {
	t, th := theme.Of(c), c.Theme()
	small := t.Text.Sm
	indent := theme.StatusDot + th.Space(2) // under the account name, past its dot
	act := func(f func()) func() { return func() { a.syncOpen = false; f() } }
	ui.Column(c).Width(theme.SyncMenuWidth).Gap(th.Space(3)).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			ui.Text(c, i18n.T("shell.sync.title")).FontWeight(600).Grow(1)
			kit.Button(c, i18n.T("shell.sync.all"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Icon: "refresh-cw"}).OnClick(act(a.SyncAll))
		})
		for _, h := range hs {
			kit.Separator(c, false)
			ui.Column(c.Key(h.acc.ID)).Gap(th.Space(1)).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
					if s, ok := syncDot(h.level); ok {
						kit.StatusDot(c, s)
					} else {
						ui.Box(c).Size(theme.StatusDot, theme.StatusDot)
					}
					ui.Text(c, accountName(h.acc)).SingleLine().Grow(1).Shrink(1)
					color := t.Muted
					switch h.level {
					case syncAction:
						color = t.DangerText
					case syncRetrying:
						color = t.WarningText
					}
					kit.Text(c, h.status, small).TextColor(color).SingleLine()
				})
				if h.reason != "" {
					r := kit.Text(c, h.reason, small).TextColor(t.Muted).Padding(0, 0, 0, indent)
					if h.err != "" {
						kit.Tooltip(c, r, h.err)
					}
				}
				if len(h.actions) > 0 {
					ui.Row(c).Gap(th.Space(2)).Padding(th.Space(1), 0, 0, indent).Children(func() {
						for _, b := range h.actions {
							o := kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}
							if b.primary {
								o.Variant = kit.Default
							}
							kit.Button(c.Key(b.label), b.label, o).OnClick(act(b.do))
						}
					})
				}
			})
		}
	})
}

// accountMark is the account's provider mark or custom icon, with a badge on its corner
// while its sync needs the user (red) or is retrying (amber), as the top
// bar's status says.
func (a *App) accountMark(c *ui.Context, acc core.Account) {
	t := theme.Of(c)
	ui.Box(c).Size(theme.MarkSm, theme.MarkSm).Shrink(0).Children(func() {
		a.accountIcon(c, acc, theme.MarkSm)
		color, label := t.Danger, "settings.dot.action"
		switch a.syncHealth(acc).level {
		case syncAction:
		case syncRetrying:
			color, label = t.Attention, "settings.dot.retrying"
		default:
			return
		}
		at := float32(theme.MarkSm - theme.StatusBadge + theme.StatusBadgeRing) // over the corner by the ring
		ui.Box(c).Absolute().Left(at).Top(at).Size(theme.StatusBadge, theme.StatusBadge).Radius(t.Radius.Full).
			Background(color).Border(theme.StatusBadgeRing, t.Background).Label(i18n.T(label))
	})
}

// accountImage is the decoded image of an account whose Icon is image:<n>,
// nil while it loads or when it does not decode. One per account: an upload
// gets a new key, which replaces the last one.
func (a *App) accountImage(acc core.Account) ui.ImageSource {
	if e, ok := a.customIcons[acc.ID]; ok && e.key == acc.Icon {
		return e.img
	}
	if a.customIcons == nil {
		a.customIcons = map[int64]accountImage{}
	}
	a.customIcons[acc.ID] = accountImage{key: acc.Icon}
	var img ui.ImageSource
	a.do(func(ctx context.Context) error {
		icon, err := a.svc.AccountIcon(ctx, acc.ID)
		if err == nil {
			img = decodeIcon(icon.ContentType, icon.Data)
		}
		return err
	}, func(error) {
		if e := a.customIcons[acc.ID]; e.key == acc.Icon {
			a.customIcons[acc.ID] = accountImage{key: acc.Icon, img: img}
		}
	})
	return nil
}

type accountImage struct {
	key string // the account's Icon
	img ui.ImageSource
}

// decodeIcon is an icon image of type ct, nil when it does not decode.
func decodeIcon(ct string, data []byte) ui.ImageSource {
	if ct == "image/svg+xml" {
		if svg, err := ui.ParseSVG(data); err == nil {
			return svg
		}
		return nil
	}
	if bmp, err := ui.DecodeBitmap(data); err == nil {
		return bmp
	}
	return nil
}

// accountIcon is an account's icon: its uploaded image, the label icon or
// provider logo it chose, or else its own provider's mark.
func (a *App) accountIcon(c *ui.Context, acc core.Account, size float32) {
	if strings.HasPrefix(acc.Icon, "image:") {
		if img := a.accountImage(acc); img != nil {
			markTile(c, img, size)
			return
		}
	}
	if id, ok := strings.CutPrefix(acc.Icon, "brand:"); ok {
		if b := icons.Brand(id); b != nil {
			markTile(c, b, size)
			return
		}
	}
	if name, ok := strings.CutPrefix(acc.Icon, "label:"); ok && slices.Contains(core.LabelIcons, name) {
		t := theme.Of(c)
		iconSize := size * 0.8
		if size <= theme.MarkSm {
			iconSize = size
		}
		ui.Box(c).Size(size, size).Shrink(0).Center().Children(func() {
			ui.Icon(c, icons.Get("label-"+name)).Size(iconSize, iconSize).TextColor(t.Foreground)
		})
		return
	}
	mark(c, accountProvider(a, acc), size)
}

// accountName is how the sidebar names an account.
func accountName(acc core.Account) string { return cmp.Or(acc.Alias, acc.Email) }

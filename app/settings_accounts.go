package app

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// accountSaveDelay is how long typing must pause before an account's
// fields save.
const accountSaveDelay = 600 * time.Millisecond

// accountForm is an account's editable fields. While an edit is
// unconfirmed (pending) the fields keep what the user typed; otherwise they
// follow the account.
type accountForm struct {
	name, alias, sig string
	limit, okLimit   int          // the sync range shown, and the account's
	ok               core.Account // the values last confirmed
	gen              uint64       // bumped per text edit; only the newest save's result counts
	pending          bool
	limitBusy        int // sync-range writes in flight
	stop             func()
}

type serverForm struct {
	imapHost, imapPort, smtpHost, smtpPort, username string
	imapSec, smtpSec                                 core.Security
}

func (p *settingsPage) form(acc core.Account) *accountForm {
	f := p.forms[acc.ID]
	if f == nil {
		f = &accountForm{ok: acc, name: acc.Name, alias: acc.Alias, sig: acc.Signature, limit: acc.SyncLimit, okLimit: acc.SyncLimit}
		p.forms[acc.ID] = f
	}
	if !f.pending && (acc.Name != f.ok.Name || acc.Alias != f.ok.Alias || acc.Signature != f.ok.Signature) {
		f.ok = acc
		f.name, f.alias, f.sig = acc.Name, acc.Alias, acc.Signature
	}
	if f.limitBusy == 0 && acc.SyncLimit != f.okLimit {
		f.limit, f.okLimit = acc.SyncLimit, acc.SyncLimit
	}
	return f
}

// edited saves the account's fields once typing pauses.
func (p *settingsPage) edited(id int64, f *accountForm) {
	f.gen++
	f.pending = true
	if f.stop != nil {
		f.stop()
	}
	f.stop = p.a.after(accountSaveDelay, func() {
		f.stop = nil
		p.saveForm(id, f)
	})
}

// saveForm writes the fields as one snapshot; a failure of the newest
// restores the last confirmed values.
func (p *settingsPage) saveForm(id int64, f *accountForm) {
	a := p.a
	name, alias, sig := f.name, f.alias, f.sig
	update := p.update
	var saved core.Account
	a.writes.Submit(accountRoute(id), f.gen, func(ctx context.Context) (err error) {
		saved, err = update(ctx, id, core.AccountPatch{Name: &name, Alias: &alias, Signature: &sig})
		return err
	}, func(gen uint64, err error) {
		if err == nil {
			f.ok.Name, f.ok.Alias, f.ok.Signature = saved.Name, saved.Alias, saved.Signature
		}
		if gen != f.gen {
			return
		}
		f.pending = false
		if err != nil {
			f.name, f.alias, f.sig = f.ok.Name, f.ok.Alias, f.ok.Signature
			a.toastErr(err)
			return
		}
		a.load()
	})
}

// patch writes p for the account after its earlier commands, then reloads.
func (p *settingsPage) patch(id int64, patch core.AccountPatch, done func(err error)) {
	a, update := p.a, p.update
	a.writes.Chain(accountRoute(id), func(ctx context.Context) error {
		_, err := update(ctx, id, patch)
		return err
	}, func(err error) {
		if done != nil {
			done(err)
		}
		a.toastErr(err)
		a.load()
	})
}

func (p *settingsPage) makeDefault(id int64) {
	for i := range p.a.accounts {
		p.a.accounts[i].IsDefault = p.a.accounts[i].ID == id
	}
	on := true
	p.patch(id, core.AccountPatch{IsDefault: &on}, nil)
}

func (p *settingsPage) setLimit(id int64, f *accountForm) {
	lim := f.limit
	f.limitBusy++
	p.patch(id, core.AccountPatch{SyncLimit: &lim}, func(err error) {
		if f.limitBusy--; err != nil && f.limitBusy == 0 {
			f.limit = f.okLimit
		}
	})
}

func (p *settingsPage) readUsage(id int64) {
	a := p.a
	var n int64
	a.query(func(ctx context.Context) (err error) {
		n, err = a.svc.AttachmentCacheUsage(ctx, id)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		p.usage[id] = n
	})
}

func (p *settingsPage) syncNow(id int64) {
	a := p.a
	a.do(func(ctx context.Context) error { return a.svc.SyncNow(ctx, id) }, a.toastErr)
}

// startReauth signs in again in the browser and gives the account the new
// tokens. ponytail: the wait has no cancel button; closing the app cancels it.
func (p *settingsPage) startReauth(acc core.Account) {
	a := p.a
	id, oauth, plat := acc.ID, a.oauth, a.plat
	p.reauth[id] = true
	a.do(func(ctx context.Context) error {
		flow, url, err := oauth.Begin(ctx, acc.Provider, acc.Email)
		if err != nil {
			return err
		}
		if err := plat.OpenURL(url); err != nil {
			oauth.Cancel(flow)
			return err
		}
		if _, err := oauth.Await(ctx, flow); err != nil {
			return err
		}
		return a.svc.Reauthorize(ctx, id, flow)
	}, func(err error) {
		delete(p.reauth, id)
		if err != nil {
			a.toastErr(err)
			return
		}
		a.toast(i18n.T("settings.account.reauth.done"))
		a.load()
	})
}

// accountProvider is the account's provider, or "other IMAP".
func accountProvider(a *App, acc core.Account) auth.Provider {
	for _, pr := range a.svc.ListProviders() {
		if pr.ID == acc.Provider {
			return pr
		}
	}
	return otherProvider
}

// secretName is what the account's password is called: 授权码, 密码…
func secretName(pr auth.Provider) string {
	if s := tr("onboarding.login." + pr.Login); s != "" && pr.Login != "oauth" {
		return s
	}
	return i18n.T("onboarding.login.password")
}

func formatBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	v, unit := float64(n)/1024, 0
	for v >= 1024 && unit < 3 {
		v /= 1024
		unit++
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + string("KMGT"[unit]) + "B"
}

func (p *settingsPage) accountView(c *ui.Context, acc core.Account) {
	a := p.a
	t := theme.Of(c)
	s := t.Scale
	f := p.form(acc)
	prov := accountProvider(a, acc)

	ui.Row(c).Gap(14 * s).Children(func() {
		mark(c, providerMark(prov), 40)
		ui.Column(c).Grow(1).Shrink(1).Gap(4 * s).Children(func() {
			ui.Row(c).Gap(8 * s).Children(func() {
				ui.Text(c, acc.Email).FontSize(t.FontSize + 7).FontWeight(600).SingleLine().Shrink(1)
				if acc.IsDefault {
					kit.Badge(c, i18n.T("settings.account.default"))
				}
			})
			meta := providerName(prov)
			if acc.StatusDetail != "" {
				meta += " · " + cmpTr("settings.account.detail."+acc.StatusDetail, acc.StatusDetail)
			}
			ui.Text(c, meta).TextColor(t.Muted)
		})
		pill := ui.Box(c).Padding(2*s, 8*s).Radius(4).Background(t.WarningBg).TextColor(t.WarningText)
		if acc.Status == "ok" {
			pill.Background(t.Success.Alpha(0.1)).TextColor(t.Success)
		}
		pill.Children(func() {
			ui.Text(c, cmpTr("settings.account.status."+acc.Status, acc.Status)).FontSize(t.FontSize - 1)
		})
	})

	if acc.Status == "reauth_required" {
		kit.Alert(c, kit.Warning, i18n.T("settings.account.reauth"), func() { p.authButton(c, acc, prov, true) })
	}

	ui.Column(c).Children(func() {
		settingsSection(c, i18n.T("settings.account.basic"), "", func() {
			ui.Field(c, i18n.T("settings.account.name"), func() {
				if kit.Input(c, &f.name, kit.InputOpts{}).Changed() {
					p.edited(acc.ID, f)
				}
			})
			ui.Field(c, i18n.T("settings.account.alias"), func() {
				if kit.Input(c, &f.alias, kit.InputOpts{}).Changed() {
					p.edited(acc.ID, f)
				}
			})
			on := acc.IsDefault
			if kit.SwitchRow(c, &on, i18n.T("settings.account.make_default"), "").Disabled(acc.IsDefault).Changed() && !acc.IsDefault {
				p.makeDefault(acc.ID)
			}
		})
		settingsSection(c, i18n.T("settings.account.signature"), i18n.T("settings.account.signature.help"), func() {
			if kit.TextArea(c, &f.sig, kit.InputOpts{}).Height(84 * s).Label(i18n.T("settings.account.signature.label")).Changed() {
				p.edited(acc.ID, f)
			}
		})
		settingsSection(c, i18n.T("settings.account.sync"), "", func() {
			ui.Row(c).Gap(8 * s).Children(func() {
				if st := p.syncState(acc); st != "" {
					ui.Text(c, st)
				}
				if acc.Status == "ok" {
					ui.Text(c, i18n.Tf("mail.unread_count", a.unread[acc.ID])).TextColor(t.Muted)
				}
				ui.Spacer(c)
				kit.Button(c, i18n.T("settings.account.sync_now"), kit.ButtonOpts{Variant: kit.Outline, Small: true, Disabled: a.syncing[acc.ID]}).
					OnClick(func() { p.syncNow(acc.ID) })
			})
			ui.Field(c, i18n.T("settings.account.sync.range"), func() {
				if kit.Select(c, &f.limit, limitOptions(acc.SyncLimit)).Changed() {
					p.setLimit(acc.ID, f)
				}
			})
			ui.Row(c).Gap(8 * s).Children(func() {
				usage := "…"
				if n, ok := p.usage[acc.ID]; ok {
					usage = formatBytes(n)
				}
				ui.Text(c, i18n.Tf("settings.account.usage", usage)).TextColor(t.Muted)
				ui.Spacer(c)
				kit.Button(c, i18n.T("settings.account.clear_cache"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).
					OnClick(func() { p.openDialog("cache", acc) })
			})
		})
		settingsSection(c, i18n.T("settings.account.login"), "", func() {
			ui.Row(c).Gap(8 * s).Children(func() {
				ui.Text(c, p.authText(acc, prov))
				ui.Spacer(c)
				p.authButton(c, acc, prov, false)
			})
			ui.Column(c).Gap(4 * s).Children(func() {
				server := func(proto, host string, port int, sec core.Security) {
					ui.Row(c).Gap(12 * s).Children(func() {
						ui.Text(c, proto).Width(48 * s).TextColor(t.Muted)
						ui.Text(c, host+":"+strconv.Itoa(port)+" · "+cmpTr("onboarding.security."+string(sec), string(sec))).Shrink(1)
					})
				}
				server("IMAP", acc.IMAPHost, acc.IMAPPort, acc.IMAPSecurity)
				server("SMTP", acc.SMTPHost, acc.SMTPPort, acc.SMTPSecurity)
			})
			kit.Button(c, i18n.T("settings.account.edit_servers"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).AlignSelf(ui.Start).
				OnClick(func() { p.openDialog("servers", acc) })
		})
		settingsSection(c, i18n.T("settings.account.remove"), i18n.T("settings.account.remove.help"), func() {
			kit.Button(c, i18n.Tf("settings.account.remove.button", acc.Email), kit.ButtonOpts{Variant: kit.Destructive}).AlignSelf(ui.Start).
				OnClick(func() { p.openDialog("remove", acc) })
		})
	})
}

// cmpTr is key's message, or fallback when the catalog has none.
func cmpTr(key, fallback string) string {
	if s := tr(key); s != "" {
		return s
	}
	return fallback
}

func limitOptions(cur int) []kit.Option[int] {
	var opts []kit.Option[int]
	for _, n := range syncLimits {
		opts = append(opts, kit.Option[int]{Value: n, Label: i18n.T("onboarding.sync." + strconv.Itoa(n))})
	}
	if cur > 0 && !slices.Contains(syncLimits, cur) {
		opts = append(opts, kit.Option[int]{Value: cur, Label: i18n.Tf("settings.account.sync.limit", cur)})
	}
	return opts
}

func (p *settingsPage) syncState(acc core.Account) string {
	a := p.a
	switch {
	case acc.Status != "ok":
		return i18n.T("settings.account.sync.stopped")
	case a.syncing[acc.ID]:
		return i18n.T("shell.sync.syncing")
	case a.syncErr[acc.ID] != "":
		return i18n.T("shell.sync.failed")
	case !a.syncedAt.IsZero():
		return a.syncedAgo()
	}
	return ""
}

func (p *settingsPage) authText(acc core.Account, prov auth.Provider) string {
	switch {
	case acc.AuthType == "oauth" && acc.Status == "reauth_required":
		return i18n.T("settings.account.login.oauth_expired")
	case acc.AuthType == "oauth":
		return i18n.T("settings.account.login.oauth")
	}
	return i18n.Tf("settings.account.login.secret", secretName(prov))
}

// authButton signs in again (OAuth) or updates the password.
func (p *settingsPage) authButton(c *ui.Context, acc core.Account, prov auth.Provider, banner bool) {
	o := kit.ButtonOpts{Variant: kit.Outline, Small: true}
	if acc.AuthType != "oauth" {
		kit.Button(c, i18n.Tf("settings.account.login.update", secretName(prov)), o).OnClick(func() { p.openDialog("password", acc) })
		return
	}
	label := i18n.T("onboarding.relogin")
	if banner {
		label = i18n.T("settings.account.reauth.browser")
	}
	if p.reauth[acc.ID] {
		label, o.Disabled = i18n.T("settings.account.reauth.waiting"), true
	}
	kit.Button(c, label, o).OnClick(func() { p.startReauth(acc) })
}

func (p *settingsPage) openDialog(kind string, acc core.Account) {
	p.dialog, p.open, p.busy, p.dlgErr, p.dlgAcc = kind, true, false, nil, acc.ID
	p.secret = ""
	p.servers = serverForm{imapHost: acc.IMAPHost, imapPort: strconv.Itoa(acc.IMAPPort), smtpHost: acc.SMTPHost,
		smtpPort: strconv.Itoa(acc.SMTPPort), username: acc.Username, imapSec: acc.IMAPSecurity, smtpSec: acc.SMTPSecurity}
}

func (p *settingsPage) closeDialog() {
	p.dialog, p.open, p.busy, p.dlgErr, p.secret = "", false, false, nil, ""
}

// act runs the dialog's action; success closes the dialog and reloads,
// failure shows in it.
func (p *settingsPage) act(fn func(ctx context.Context) error, ok func()) {
	a := p.a
	p.busy, p.dlgErr = true, nil
	kind := p.dialog
	a.do(fn, func(err error) {
		if p.dialog != kind {
			return
		}
		p.busy = false
		if err != nil {
			p.dlgErr = err
			return
		}
		p.closeDialog()
		if ok != nil {
			ok()
		}
		a.load()
	})
}

func (p *settingsPage) dialogView(c *ui.Context) {
	if p.dialog == "" {
		return
	}
	if !p.open { // Escape or a click outside
		p.closeDialog()
		return
	}
	a := p.a
	t := theme.Of(c)
	var acc core.Account
	for _, x := range a.accounts {
		if x.ID == p.dlgAcc {
			acc = x
		}
	}
	if acc.ID == 0 {
		p.closeDialog()
		return
	}
	id := acc.ID
	prov := accountProvider(a, acc)
	var title string
	var body, confirm func()
	label, variant := i18n.T("common.save"), kit.Primary
	switch p.dialog {
	case "password":
		title = i18n.Tf("settings.account.login.update", secretName(prov))
		body = func() {
			ui.Field(c, secretName(prov), func() { kit.Input(c, &p.secret, kit.InputOpts{Password: true}) }).
				Description(i18n.T("onboarding.secret.help"))
		}
		secret := p.secret
		confirm = func() {
			p.act(func(ctx context.Context) error { return a.svc.UpdatePassword(ctx, id, secret) },
				func() { a.toast(i18n.Tf("settings.account.password.updated", secretName(prov))) })
		}
	case "servers":
		title = i18n.T("settings.account.servers")
		body = func() { p.serversForm(c) }
		sf, update := p.servers, p.update
		confirm = func() {
			ip, _ := strconv.Atoi(strings.TrimSpace(sf.imapPort))
			sp, _ := strconv.Atoi(strings.TrimSpace(sf.smtpPort))
			ss := core.ServerSettings{Username: strings.TrimSpace(sf.username), IMAPHost: strings.TrimSpace(sf.imapHost), IMAPPort: ip,
				IMAPSecurity: sf.imapSec, SMTPHost: strings.TrimSpace(sf.smtpHost), SMTPPort: sp, SMTPSecurity: sf.smtpSec}
			p.act(func(ctx context.Context) error {
				_, err := update(ctx, id, core.AccountPatch{Servers: &ss})
				return err
			}, nil)
		}
	case "remove":
		title = i18n.Tf("settings.account.remove.title", acc.Email)
		body = func() { ui.Text(c, i18n.T("settings.account.remove.help")).TextColor(t.Muted) }
		label, variant = i18n.T("common.delete"), kit.Destructive
		confirm = func() {
			p.act(func(ctx context.Context) error { return a.svc.RemoveAccount(ctx, id) }, func() {
				delete(p.forms, id)
				delete(p.usage, id)
				a.navigate(Route{Page: PageSettings})
			})
		}
	case "cache":
		title = i18n.T("settings.account.clear_cache.title")
		body = func() { ui.Text(c, i18n.T("settings.account.clear_cache.body")).TextColor(t.Muted) }
		label = i18n.T("settings.account.clear_cache.confirm")
		confirm = func() {
			p.act(func(ctx context.Context) error { return a.svc.ClearAttachmentCache(ctx, id) }, func() { p.readUsage(id) })
		}
	}
	kit.Dialog(c, &p.open, title, func() {
		body()
		if p.dlgErr != nil {
			kit.Alert(c, kit.Danger, humanize(p.dlgErr), nil)
		}
	}, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(p.closeDialog)
		kit.Button(c, label, kit.ButtonOpts{Variant: variant, Disabled: p.busy || p.dialog == "password" && p.secret == ""}).OnClick(confirm)
	})
}

func (p *settingsPage) serversForm(c *ui.Context) {
	t := theme.Of(c)
	opts := make([]kit.Option[core.Security], len(securities))
	for i, s := range securities {
		opts[i] = kit.Option[core.Security]{Value: s.Value, Label: i18n.T(s.Label)}
	}
	server := func(proto string, host, port *string, sec *core.Security) {
		ui.Row(c).Gap(8 * t.Scale).AlignItems(ui.Start).Children(func() {
			ui.Field(c, i18n.T("onboarding."+proto+".host"), func() { kit.Input(c, host, kit.InputOpts{}) }).Grow(1)
			ui.Field(c, i18n.T("onboarding."+proto+".port"), func() { kit.Input(c, port, kit.InputOpts{}) }).Width(90)
			ui.Field(c, i18n.T("onboarding."+proto+".security"), func() { kit.Select(c, sec, opts) })
		})
	}
	sf := &p.servers
	server("imap", &sf.imapHost, &sf.imapPort, &sf.imapSec)
	server("smtp", &sf.smtpHost, &sf.smtpPort, &sf.smtpSec)
	ui.Field(c, i18n.T("onboarding.username"), func() { kit.Input(c, &sf.username, kit.InputOpts{}) })
}

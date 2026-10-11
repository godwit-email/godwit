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

// accountForm is an account's editable fields. A field with an unconfirmed
// edit keeps what the user typed; otherwise it follows the account, though
// not to a snapshot read before its last confirmed save.
type accountForm struct {
	name, alias, sig string
	limit, okLimit   int          // the sync range shown, and the account's
	ok               core.Account // the values last confirmed
	okAt             uint64       // a.loadGen when ok was confirmed; older loads are stale
	edits            [3]fieldEdit // name, alias, signature
	limitBusy        int          // sync-range writes in flight
}

// fieldEdit is one text field's debounce: typing restarts it, and only the
// newest edit's result counts.
type fieldEdit struct {
	gen     uint64
	pending bool
	stop    func()
}

func (f *accountForm) field(i int) *string { return [...]*string{&f.name, &f.alias, &f.sig}[i] }

func accountField(acc core.Account, i int) string {
	return [...]string{acc.Name, acc.Alias, acc.Signature}[i]
}

func fieldPatch(i int, v string) core.AccountPatch {
	switch i {
	case 0:
		return core.AccountPatch{Name: &v}
	case 1:
		return core.AccountPatch{Alias: &v}
	}
	return core.AccountPatch{Signature: &v}
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
	if p.a.accountsGen > f.okAt {
		for i := range f.edits {
			if v := accountField(acc, i); !f.edits[i].pending && v != accountField(f.ok, i) {
				*f.field(i) = v
			}
		}
		f.ok = acc
	}
	if f.limitBusy == 0 && acc.SyncLimit != f.okLimit {
		f.limit, f.okLimit = acc.SyncLimit, acc.SyncLimit
	}
	return f
}

// edited saves field i once typing in it pauses.
func (p *settingsPage) edited(id int64, f *accountForm, i int) {
	e := &f.edits[i]
	e.gen++
	e.pending = true
	if e.stop != nil {
		e.stop()
	}
	e.stop = p.a.after(accountSaveDelay, func() {
		e.stop = nil
		p.saveField(id, f, i)
	})
}

// cancelEdits drops the account's edits not yet saved.
func (f *accountForm) cancelEdits() {
	for i := range f.edits {
		if e := &f.edits[i]; e.stop != nil {
			e.stop()
			e.stop, e.pending = nil, false
		}
	}
}

// commitEdits saves at once every field still waiting for typing to pause.
func (p *settingsPage) commitEdits() {
	for id, f := range p.forms {
		for i := range f.edits {
			if e := &f.edits[i]; e.stop != nil {
				e.stop()
				e.stop = nil
				p.saveField(id, f, i)
			}
		}
	}
}

// saveField writes field i as it is now, after the account's earlier
// writes; a failure of its newest edit restores the last confirmed value.
func (p *settingsPage) saveField(id int64, f *accountForm, i int) {
	a, e := p.a, &f.edits[i]
	gen := e.gen
	p.write(id, fieldPatch(i, *f.field(i)), func(saved core.Account, err error) {
		if err == nil {
			f.ok, f.okAt = saved, a.loadGen
		}
		if gen != e.gen {
			return
		}
		e.pending = false
		if err != nil {
			*f.field(i) = accountField(f.ok, i)
			a.toastErr(err)
		}
	})
}

// write runs UpdateAccount after the account's earlier writes. On success
// the returned account replaces the shown one at once; either way the
// accounts reload.
func (p *settingsPage) write(id int64, patch core.AccountPatch, done func(saved core.Account, err error)) {
	a, update := p.a, p.update
	var saved core.Account
	a.writes.Chain(accountRoute(id), func(ctx context.Context) (err error) {
		saved, err = update(ctx, id, patch)
		return err
	}, func(err error) {
		if err == nil {
			a.mergeAccount(saved)
		}
		if done != nil {
			done(saved, err)
		}
		a.load()
	})
}

// mergeAccount shows acc in place of the account's older copy.
func (a *App) mergeAccount(acc core.Account) {
	for i := range a.accounts {
		if a.accounts[i].ID == acc.ID {
			a.accounts[i] = acc
		} else if acc.IsDefault {
			a.accounts[i].IsDefault = false
		}
	}
}

// patch writes a command (default, sync range); a failure shows as a toast.
func (p *settingsPage) patch(id int64, patch core.AccountPatch, done func(err error)) {
	p.write(id, patch, func(_ core.Account, err error) {
		if done != nil {
			done(err)
		}
		p.a.toastErr(err)
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

// startReauth signs in again in the browser, then gives the account the
// new tokens after its earlier writes. ponytail: the wait has no cancel
// button; closing the app cancels it.
func (p *settingsPage) startReauth(acc core.Account) {
	a := p.a
	id, oauth, plat := acc.ID, a.oauth, a.plat
	p.reauth[id] = true
	var flow string
	a.do(func(ctx context.Context) error {
		var url string
		var err error
		if flow, url, err = oauth.Begin(ctx, acc.Provider, acc.Email); err != nil {
			return err
		}
		if err := plat.OpenURL(url); err != nil {
			oauth.Cancel(flow)
			return err
		}
		_, err = oauth.Await(ctx, flow)
		return err
	}, func(err error) {
		if err != nil {
			delete(p.reauth, id)
			a.toastErr(err)
			return
		}
		a.writes.Chain(accountRoute(id), func(ctx context.Context) error { return a.svc.Reauthorize(ctx, id, flow) }, func(err error) {
			delete(p.reauth, id)
			if err != nil {
				a.toastErr(err)
			} else {
				a.toastAs(kit.ToastSuccess, i18n.T("settings.account.reauth.done"))
			}
			a.load()
		})
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
	t, th := theme.Of(c), c.Theme()
	f := p.form(acc)
	prov := accountProvider(a, acc)

	ui.Row(c).Gap(th.Space(3.5)).Children(func() {
		mark(c, prov, theme.MarkLg)
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				ui.Text(c, acc.Email).FontSize(t.Text.X2l).FontWeight(600).SingleLine().Shrink(1)
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
		pill := ui.Box(c).Padding(th.Space(0.5), th.Space(2)).Radius(t.Radius.Sm).Background(t.WarningBg).TextColor(t.WarningText)
		if acc.Status == "ok" {
			pill.Background(t.Success.Alpha(0.1)).TextColor(t.Success)
		}
		pill.Children(func() {
			ui.Text(c, cmpTr("settings.account.status."+acc.Status, acc.Status)).FontSize(t.Text.Sm)
		})
	})

	if acc.Status == "reauth_required" {
		kit.Alert(c, kit.Warning, i18n.T("settings.account.reauth"), func() { p.authButton(c, acc, prov, true) })
	}

	row := func(label, desc string, control func()) func() {
		return func() { settingRow(c, label, desc, control) }
	}
	input := func(label string, v *string, i int) func() {
		return row(label, "", func() {
			if kit.Input(c, v, kit.InputOpts{}).Width(theme.SettingsInputWidth).Radius(t.Radius.Lg).Label(label).Changed() {
				p.edited(acc.ID, f, i)
			}
		})
	}
	settingsSection(c, i18n.T("settings.account.basic"), "",
		input(i18n.T("settings.account.name"), &f.name, 0),
		input(i18n.T("settings.account.alias"), &f.alias, 1),
		func() {
			on := acc.IsDefault
			if kit.SwitchRow(c, &on, i18n.T("settings.account.make_default"), "").Disabled(acc.IsDefault).Changed() && !acc.IsDefault {
				p.makeDefault(acc.ID)
			}
		})
	settingsSection(c, i18n.T("settings.account.signature"), i18n.T("settings.account.signature.help"), func() {
		settingBlock(c, func() {
			if kit.TextArea(c, &f.sig, kit.InputOpts{}).Height(th.Space(21)).Label(i18n.T("settings.account.signature.label")).Changed() {
				p.edited(acc.ID, f, 2)
			}
		})
	})
	unread := ""
	if acc.Status == "ok" {
		unread = i18n.Tf("mail.unread_count", a.unread[acc.ID])
	}
	usage := "…"
	if n, ok := p.usage[acc.ID]; ok {
		usage = formatBytes(n)
	}
	settingsSection(c, i18n.T("settings.account.sync"), "",
		row(p.syncState(acc), unread, func() {
			settingButton(c, i18n.T("settings.account.sync_now"), kit.Outline, a.syncing[acc.ID]).OnClick(func() { p.syncNow(acc.ID) })
		}),
		row(i18n.T("settings.account.sync.range"), "", func() {
			if settingSelect(c, i18n.T("settings.account.sync.range"), &f.limit, limitOptions(acc.SyncLimit)).Changed() {
				p.setLimit(acc.ID, f)
			}
		}),
		row(i18n.Tf("settings.account.usage", usage), "", func() {
			settingButton(c, i18n.T("settings.account.clear_cache"), kit.Outline, false).OnClick(func() { p.openDialog("cache", acc) })
		}))
	server := func(proto, host string, port int, sec core.Security) {
		ui.Row(c).Gap(th.Space(3)).Children(func() {
			ui.Text(c, proto).Width(th.Space(12)).TextColor(t.Muted)
			ui.Text(c, host+":"+strconv.Itoa(port)+" · "+cmpTr("onboarding.security."+string(sec), string(sec))).Shrink(1)
		})
	}
	settingsSection(c, i18n.T("settings.account.login"), "",
		row(p.authText(acc, prov), "", func() { p.authButton(c, acc, prov, false) }),
		func() {
			settingBlock(c, func() {
				ui.Column(c).Gap(th.Space(1)).Children(func() {
					server("IMAP", acc.IMAPHost, acc.IMAPPort, acc.IMAPSecurity)
					server("SMTP", acc.SMTPHost, acc.SMTPPort, acc.SMTPSecurity)
				})
				settingButton(c, i18n.T("settings.account.edit_servers"), kit.Outline, false).AlignSelf(ui.Start).OnClick(func() { p.openDialog("servers", acc) })
			})
		})
	settingsSection(c, i18n.T("settings.account.remove"), i18n.T("settings.account.remove.help"), func() {
		settingBlock(c, func() {
			settingButton(c, i18n.Tf("settings.account.remove.button", acc.Email), kit.DestructiveOutline, false).AlignSelf(ui.Start).OnClick(func() { p.openDialog("remove", acc) })
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
	o := kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}
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
	p.dlgGen++
	p.secret = ""
	p.servers = serverForm{imapHost: acc.IMAPHost, imapPort: strconv.Itoa(acc.IMAPPort), smtpHost: acc.SMTPHost,
		smtpPort: strconv.Itoa(acc.SMTPPort), username: acc.Username, imapSec: acc.IMAPSecurity, smtpSec: acc.SMTPSecurity}
}

func (p *settingsPage) closeDialog() {
	p.dialog, p.open, p.busy, p.dlgErr, p.secret = "", false, false, nil, ""
}

// act runs the dialog's action after the account's earlier writes. Success
// closes the dialog and runs ok, failure shows in it, and the accounts
// reload either way; a dialog opened since (current false) is left alone.
func (p *settingsPage) act(id int64, fn func(ctx context.Context) error, ok func(current bool)) {
	a := p.a
	p.busy, p.dlgErr = true, nil
	gen := p.dlgGen
	a.writes.Chain(accountRoute(id), fn, func(err error) {
		current := gen == p.dlgGen && p.dialog != ""
		if current {
			p.busy, p.dlgErr = false, err
		}
		if err == nil {
			if current {
				p.closeDialog()
			}
			if ok != nil {
				ok(current)
			}
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
	label, variant := i18n.T("common.save"), kit.Default
	switch p.dialog {
	case "password":
		title = i18n.Tf("settings.account.login.update", secretName(prov))
		body = func() {
			ui.Field(c, secretName(prov), func() { kit.Input(c, &p.secret, kit.InputOpts{Password: true}) }).
				Description(i18n.T("onboarding.secret.help"))
		}
		secret := p.secret
		confirm = func() {
			p.act(id, func(ctx context.Context) error { return a.svc.UpdatePassword(ctx, id, secret) },
				func(bool) {
					a.toastAs(kit.ToastSuccess, i18n.Tf("settings.account.password.updated", secretName(prov)))
				})
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
			var saved core.Account
			p.act(id, func(ctx context.Context) (err error) {
				saved, err = update(ctx, id, core.AccountPatch{Servers: &ss})
				return err
			}, func(bool) { a.mergeAccount(saved) })
		}
	case "remove":
		title = i18n.Tf("settings.account.remove.title", acc.Email)
		body = func() { ui.Text(c, i18n.T("settings.account.remove.help")).TextColor(t.Muted) }
		label, variant = i18n.T("common.delete"), kit.Destructive
		confirm = func() {
			if f := p.forms[id]; f != nil {
				f.cancelEdits()
			}
			p.act(id, func(ctx context.Context) error { return a.svc.RemoveAccount(ctx, id) }, func(current bool) {
				delete(p.forms, id)
				delete(p.usage, id)
				if current {
					a.navigate(Route{Page: PageSettings})
				}
			})
		}
	case "cache":
		title = i18n.T("settings.account.clear_cache.title")
		body = func() { ui.Text(c, i18n.T("settings.account.clear_cache.body")).TextColor(t.Muted) }
		label = i18n.T("settings.account.clear_cache.confirm")
		confirm = func() {
			p.act(id, func(ctx context.Context) error { return a.svc.ClearAttachmentCache(ctx, id) }, func(bool) { p.readUsage(id) })
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
	th := c.Theme()
	opts := make([]kit.Option[core.Security], len(securities))
	for i, s := range securities {
		opts[i] = kit.Option[core.Security]{Value: s.Value, Label: i18n.T(s.Label)}
	}
	server := func(proto string, host, port *string, sec *core.Security) {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Start).Children(func() {
			ui.Field(c, i18n.T("onboarding."+proto+".host"), func() { kit.Input(c, host, kit.InputOpts{}) }).Grow(1)
			ui.Field(c, i18n.T("onboarding."+proto+".port"), func() { kit.Input(c, port, kit.InputOpts{}) }).Width(theme.PortWidth)
			ui.Field(c, i18n.T("onboarding."+proto+".security"), func() { kit.Select(c, sec, opts) })
		})
	}
	sf := &p.servers
	server("imap", &sf.imapHost, &sf.imapPort, &sf.imapSec)
	server("smtp", &sf.smtpHost, &sf.smtpPort, &sf.smtpSec)
	ui.Field(c, i18n.T("onboarding.username"), func() { kit.Input(c, &sf.username, kit.InputOpts{}) })
}

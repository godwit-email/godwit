package app

import (
	"context"
	"errors"
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
	name, alias  string
	sync, okSync core.SyncRange // the sync range shown, and the account's
	ok           core.Account   // the values last confirmed
	okAt         uint64         // a.loadGen when ok was confirmed; older loads are stale
	edits        [2]fieldEdit   // name, alias
	syncBusy     int            // sync-range writes in flight
}

// fieldEdit is one text field's debounce: typing restarts it, and only the
// newest edit's result counts.
type fieldEdit struct {
	gen     uint64
	pending bool
	stop    func()
}

func (f *accountForm) field(i int) *string { return [...]*string{&f.name, &f.alias}[i] }

func accountField(acc core.Account, i int) string { return [...]string{acc.Name, acc.Alias}[i] }

func fieldPatch(i int, v string) core.AccountPatch {
	if i == 0 {
		return core.AccountPatch{Name: &v}
	}
	return core.AccountPatch{Alias: &v}
}

type serverForm struct {
	imapHost, imapPort, smtpHost, smtpPort, username string
	imapSec, smtpSec                                 core.Security
}

func (p *settingsPage) form(acc core.Account) *accountForm {
	f := p.forms[acc.ID]
	if f == nil {
		f = &accountForm{ok: acc, name: acc.Name, alias: acc.Alias, sync: acc.Sync, okSync: acc.Sync}
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
	if f.syncBusy == 0 && acc.Sync != f.okSync {
		f.sync, f.okSync = acc.Sync, acc.Sync
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

// signatureFlushWait is how long closing waits for the signature page's save.
const signatureFlushWait = 2 * time.Second

// errSignatureUnsaved is why closing asks when the signature page did not save.
var errSignatureUnsaved = errors.New("the signature was not saved")

// signature has the signature page show account id: made at the first
// account shown, switched outside the frame (making a web view may run
// the event loop).
func (p *settingsPage) signature(id int64) {
	if p.sigAcc == id || p.sigPosted || p.sigErr != nil {
		return
	}
	p.sigPosted = true
	p.a.onUI(func() {
		p.sigPosted = false
		if p.sigView == nil {
			if p.sigView, p.sigShow, p.sigFlush, p.sigClose, p.sigErr = p.a.plat.NewSignatureView(); p.sigErr != nil {
				p.a.logf("the signature page: %v", p.sigErr)
				return
			}
		}
		p.sigShow(id)
		p.sigAcc = id
	})
}

// flushSignature has the signature page save its edit, then calls then
// with errSignatureUnsaved if it failed or did not answer within
// signatureFlushWait. A save that succeeds after that calls then(nil)
// again: a close that asked only for it goes on.
func (p *settingsPage) flushSignature(then func(error)) {
	if p.sigFlush == nil {
		then(nil)
		return
	}
	timedOut, answered := false, false
	stop := p.a.after(signatureFlushWait, func() {
		timedOut = true
		then(errSignatureUnsaved)
	})
	p.sigFlush(func(ok bool) {
		if answered {
			return
		}
		answered = true
		switch {
		case !timedOut:
			stop()
			if !ok {
				then(errSignatureUnsaved)
			} else {
				then(nil)
			}
		case ok:
			then(nil)
		}
	})
}

// closeSignature closes the signature page, if made (the App closing).
func (p *settingsPage) closeSignature() {
	if p.sigClose != nil {
		p.sigClose()
	}
	p.sigView, p.sigShow, p.sigFlush, p.sigClose = nil, nil, nil, nil
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

func (p *settingsPage) setSync(id int64, f *accountForm) {
	r := f.sync
	f.syncBusy++
	p.patch(id, core.AccountPatch{Sync: &r}, func(err error) {
		if f.syncBusy--; err != nil && f.syncBusy == 0 {
			f.sync = f.okSync
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

	ui.Row(c).Gap(th.Space(4)).Children(func() {
		mark(c, prov, theme.MarkXl)
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
			kit.Text(c, acc.Email, t.Text.Display).FontWeight(600).SingleLine()
			ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				meta := providerName(prov)
				if acc.Folders > 0 { // 0 until the first sync
					meta += " · " + i18n.Tf("settings.account.folders", acc.Folders)
				}
				if acc.StatusDetail != "" {
					meta += " · " + cmpTr("settings.account.detail."+acc.StatusDetail, acc.StatusDetail)
				}
				ui.Text(c, meta).TextColor(t.Muted)
				if acc.IsDefault {
					kit.Badge(c, i18n.T("settings.account.default"))
				}
				text, bg, fg := cmpTr("settings.account.status."+acc.Status, acc.Status), t.WarningBg, t.WarningText
				if acc.Status == "ok" {
					bg, fg = t.SuccessBg, t.SuccessText
					if at, ok := a.syncedAtBy[acc.ID]; ok {
						text = i18n.Tf("shell.sync.synced", a.ago(at))
					}
				}
				ui.Box(c).Padding(th.Space(0.5), th.Space(2)).Radius(t.Radius.Sm).Background(bg).TextColor(fg).Children(func() {
					kit.Text(c, text, t.Text.Sm)
				})
			})
		})
	})

	if acc.Status == "reauth_required" {
		msg := i18n.T("settings.account.reauth.unknown")
		if at, ok := a.syncedAtBy[acc.ID]; ok {
			msg = i18n.Tf("settings.account.reauth", a.ago(at))
		}
		kit.Alert(c, kit.Warning, msg, func() { p.authButton(c, acc, prov, true) })
	}

	row := func(label, desc string, control func()) func() {
		return func() { settingRow(c, label, desc, control) }
	}
	input := func(label, desc string, v *string, i int) func() {
		return row(label, desc, func() {
			if kit.Input(c, v, kit.InputOpts{}).Width(theme.SettingsInputWidth).Radius(t.Radius.Lg).Label(label).Changed() {
				p.edited(acc.ID, f, i)
			}
		})
	}
	settingsSection(c, i18n.T("settings.account.basic"), "",
		input(i18n.T("settings.account.name"), i18n.T("settings.account.name.help"), &f.name, 0),
		input(i18n.T("settings.account.alias"), "", &f.alias, 1),
		func() {
			on := acc.IsDefault
			if kit.SwitchRow(c, &on, i18n.T("settings.account.make_default"), i18n.T("settings.account.make_default.help")).Disabled(acc.IsDefault).Changed() && !acc.IsDefault {
				p.makeDefault(acc.ID)
			}
		})
	settingsSection(c, i18n.T("settings.account.signature"), i18n.T("settings.account.signature.help"), func() {
		p.signature(acc.ID)
		switch {
		case p.sigErr != nil:
			settingBlock(c, func() { ui.Text(c, humanize(p.sigErr)).TextColor(t.Muted) })
		case p.sigView != nil:
			ui.WebView(c, p.sigView).FillWidth().Height(theme.SignatureEditorHeight) // the page draws its toolbar and editor
		default:
			ui.Box(c).FillWidth().Height(theme.SignatureEditorHeight)
		}
	})
	desc := p.syncState(acc)
	if acc.Status == "ok" {
		desc = strings.Join(slices.DeleteFunc([]string{desc, i18n.Tf("mail.unread_count", a.unread[acc.ID])}, func(s string) bool { return s == "" }), " · ")
	}
	usage := "…"
	if n, ok := p.usage[acc.ID]; ok {
		usage = formatBytes(n)
	}
	settingsSection(c, i18n.T("settings.account.sync"), "",
		row(i18n.T("settings.account.sync.status"), desc, func() {
			settingButton(c, i18n.T("settings.account.sync_now"), kit.Outline, a.syncing[acc.ID]).OnClick(func() { p.syncNow(acc.ID) })
		}),
		row(i18n.T("settings.account.sync.range"), "", func() {
			if settingSelect(c, i18n.T("settings.account.sync.range"), &f.sync, rangeOptions()).Changed() {
				p.setSync(acc.ID, f)
			}
		}),
		row(i18n.T("settings.account.usage"), i18n.T("settings.account.usage.help"), func() {
			ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, usage).TextColor(t.Muted)
				settingButton(c, i18n.T("settings.account.clear_cache"), kit.Outline, false).OnClick(func() { p.openDialog("cache", acc) })
			})
		}))
	p.mailboxSection(c, acc.ID)
	server := func(proto, host string, port int, sec core.Security) func() {
		return row(proto, "", func() {
			kit.Text(c, host+":"+strconv.Itoa(port)+" · "+cmpTr("onboarding.security."+string(sec), string(sec)), t.Text.Sm).Font(theme.Mono).TextColor(t.Muted)
		})
	}
	settingsSection(c, i18n.T("settings.account.login"), "",
		row(i18n.T("settings.account.login.method"), p.authText(acc, prov), func() { p.authButton(c, acc, prov, false) }),
		server("IMAP", acc.IMAPHost, acc.IMAPPort, acc.IMAPSecurity),
		server("SMTP", acc.SMTPHost, acc.SMTPPort, acc.SMTPSecurity),
		row(i18n.T("settings.account.servers"), i18n.T("settings.account.servers.help"), func() {
			settingButton(c, i18n.T("settings.account.edit_servers"), kit.Outline, false).OnClick(func() { p.openDialog("servers", acc) })
		}))
	settingsSection(c, i18n.T("settings.account.remove"), "",
		row(i18n.T("settings.account.remove.row"), strings.TrimRight(i18n.T("settings.account.remove.help"), "。."), func() {
			settingButton(c, i18n.T("settings.account.remove.button"), kit.DestructiveOutline, false).Label(i18n.Tf("settings.account.remove.aria", acc.Email)).OnClick(func() { p.openDialog("remove", acc) })
		}))
}

// cmpTr is key's message, or fallback when the catalog has none.
func cmpTr(key, fallback string) string {
	if s := tr(key); s != "" {
		return s
	}
	return fallback
}

// syncRanges are the ranges offered: 2000 per folder, 3 months and all.
var syncRanges = []core.SyncRange{{Limit: 2000}, {Months: 3}, {}}

// rangeLabel names r as settings lists it (2000 without （推荐）).
func rangeLabel(r core.SyncRange) string {
	switch {
	case r.Limit > 0:
		return i18n.T("settings.account.sync.2000")
	case r.Months > 0:
		return i18n.Tf("settings.account.sync.months", r.Months)
	}
	return i18n.T("settings.account.sync.all")
}

// rangeOptions are syncRanges, labelled as settings lists them.
func rangeOptions() []kit.Option[core.SyncRange] {
	var opts []kit.Option[core.SyncRange]
	for _, r := range syncRanges {
		opts = append(opts, kit.Option[core.SyncRange]{Value: r, Label: rangeLabel(r)})
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
	}
	if at, ok := a.syncedAtBy[acc.ID]; ok {
		return i18n.Tf("shell.sync.synced", a.ago(at))
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
		text, open := i18n.Tf("settings.account.login.update", secretName(prov)), func() { p.openDialog("password", acc) }
		if banner {
			kit.AlertAction(c, kit.Warning, text, open)
		} else {
			kit.Button(c, text, o).OnClick(open)
		}
		return
	}
	label := i18n.T("onboarding.relogin")
	if banner {
		label = i18n.T("settings.account.reauth.browser")
	}
	if p.reauth[acc.ID] {
		label, o.Disabled = i18n.T("settings.account.reauth.waiting"), true
	}
	if banner && !o.Disabled {
		kit.AlertAction(c, kit.Warning, label, func() { p.startReauth(acc) })
		return
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
	acc, ok := a.accountByID(p.dlgAcc)
	if !ok {
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

// mailbox is an account's 邮箱行为 as read: its folders (roles resolved),
// and the selects' values, which follow them unless a write is in flight.
type mailbox struct {
	folders []core.Folder
	roles   [5]int64 // per mailboxRoles: the folder holding it, 0 none
	purge   [2]int   // per mailsync.PurgeRoles: days
	blocked [2]bool
	busy    int // writes in flight
}

// mailboxRoles are the role rows in the design's order.
var mailboxRoles = []string{"drafts", "sent", "junk", "trash", "archive"}

func (p *settingsPage) readMailbox(id int64) {
	a := p.a
	var fs []core.Folder
	var ps map[string]core.Purge
	a.query(func(ctx context.Context) (err error) {
		if fs, err = a.svc.ListFolders(ctx, id); err != nil {
			return err
		}
		ps, err = a.svc.Purges(ctx, id)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		b := p.boxes[id]
		if b == nil {
			b = &mailbox{}
			p.boxes[id] = b
		}
		if b.busy > 0 {
			return // the write's own read follows
		}
		b.folders = fs
		for i, r := range mailboxRoles {
			b.roles[i] = 0
			if j := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Role == r }); j >= 0 {
				b.roles[i] = fs[j].ID
			}
		}
		for i, r := range []string{"junk", "trash"} {
			b.purge[i], b.blocked[i] = ps[r].Days, ps[r].Blocked
		}
	})
}

// writeMailbox runs a 邮箱行为 change, then reads the section again; a
// failure shows as a toast and the selects return to what core has.
func (p *settingsPage) writeMailbox(id int64, b *mailbox, fn func(ctx context.Context) error) {
	a := p.a
	b.busy++
	a.do(fn, func(err error) {
		b.busy--
		a.toastErr(err)
		p.readMailbox(id)
	})
}

// roleOptions are the role's choices as the design lists them: the folder
// holding it first (or 不使用 when none does), then the other folders but
// INBOX; archive also offers 不使用 once it has a folder. Junk and trash
// leave out the folders core refuses for them (sent, drafts, archive).
func roleOptions(b *mailbox, i int) []kit.Option[int64] {
	cur, role := b.roles[i], mailboxRoles[i]
	none := kit.Option[int64]{Value: 0, Label: i18n.T("settings.account.mailbox.none")}
	var opts []kit.Option[int64]
	if cur == 0 {
		opts = append(opts, none)
	}
	for _, f := range b.folders {
		if f.ID == cur {
			opts = slices.Insert(opts, 0, kit.Option[int64]{Value: f.ID, Label: f.Name})
			continue
		}
		if f.Role == "inbox" || (slices.Contains([]string{"junk", "trash"}, role) && slices.Contains([]string{"sent", "drafts", "archive"}, f.Role)) {
			continue
		}
		opts = append(opts, kit.Option[int64]{Value: f.ID, Label: f.Name})
	}
	if cur != 0 && role == "archive" {
		opts = append(opts, none)
	}
	return opts
}

func purgeOptions() []kit.Option[int] {
	var opts []kit.Option[int]
	for _, d := range core.PurgeDays {
		opts = append(opts, kit.Option[int]{Value: d, Label: i18n.T("settings.account.purge." + strconv.Itoa(d))})
	}
	return opts
}

// mailboxSection is 邮箱行为: a select per role, and the junk and trash purges
// under their roles.
func (p *settingsPage) mailboxSection(c *ui.Context, id int64) {
	b := p.boxes[id]
	if b == nil {
		b = &mailbox{}
	}
	role := func(i int) func() {
		r := mailboxRoles[i]
		label := i18n.T("mail.folder." + r)
		return func() {
			settingRow(c, label, cmpTr("settings.account.mailbox."+r+".help", ""), func() {
				if settingSelect(c, label, &b.roles[i], roleOptions(b, i)).Changed() {
					folder := b.roles[i]
					p.writeMailbox(id, b, func(ctx context.Context) error { return p.a.svc.SetRoleFolder(ctx, id, r, folder) })
				}
			})
		}
	}
	purge := func(i int) func() {
		r := []string{"junk", "trash"}[i]
		label := i18n.T("settings.account.purge." + r)
		desc := i18n.T("settings.account.purge." + r + ".help")
		if b.blocked[i] && b.purge[i] > 0 {
			desc = i18n.T("settings.account.purge.blocked")
		}
		return func() {
			settingRow(c, label, desc, func() {
				if settingSelect(c, label, &b.purge[i], purgeOptions()).Changed() {
					days := b.purge[i]
					p.writeMailbox(id, b, func(ctx context.Context) error { return p.a.svc.SetPurge(ctx, id, r, days) })
				}
			})
		}
	}
	settingsSection(c, i18n.T("settings.account.mailbox"), i18n.T("settings.account.mailbox.help"),
		role(0), role(1), role(2), purge(0), role(3), purge(1), role(4))
}

package app

import (
	"context"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// debounce is how long typing pauses before the provider is detected.
const debounce = 400 * time.Millisecond

const defaultSyncLimit = 2000

type obStep int

const (
	stepPick obStep = iota
	stepPassword
	stepOAuth
	stepChecking
	stepDone
)

// testSecurity, set only by tests, replaces both servers' security: the fake
// servers on loopback speak plaintext, which the form never offers.
var testSecurity core.Security

var otherProvider = auth.Provider{ID: "other", Login: "password"}

// onboarding adds an account: the first-run page (window) or the settings
// sheet. Steps: pick a provider, then a password or a browser sign-in, then
// the connection check, then the sender name and sync range.
type onboarding struct {
	a         *App
	sheet     bool // the settings sheet; else the first-run window
	open      bool // the sheet shows; Escape or a click outside clears it
	providers []auth.Provider

	step obStep
	gen  int // bumped as the step changes: async results of an older gen are dropped

	email, emailErr string
	detected        auth.Provider
	detectedFor     string // the address detected is for
	detectStop      func()

	prov                 auth.Provider
	askEmail             bool // the provider was picked before an address was typed
	secret, username     string
	manual               bool
	imapHost, imapPort   string
	smtpHost, smtpPort   string
	imapSec, smtpSec     core.Security
	flowID, authURL      string
	oauthErr             error
	report               core.CheckReport
	checking, adding     bool
	invalid, invalidNext string // the form's own settings-step failure, in words
	name                 string
	syncLimit            int
	again                bool  // finish starts over for another account
	addedID              int64 // the account added; a failed sync-range update retries only that
	saveErr              error

	// add and update save the account: the Service's, swapped by tests.
	add    func(context.Context, core.AccountInput) (core.Account, error)
	update func(context.Context, int64, core.AccountPatch) (core.Account, error)
}

func newOnboarding(a *App, sheet bool) *onboarding {
	return &onboarding{a: a, sheet: sheet, open: sheet, providers: append(a.svc.ListProviders(), otherProvider), syncLimit: defaultSyncLimit,
		add: a.svc.AddAccount, update: a.svc.UpdateAccount}
}

// openAddAccount shows the add-account sheet over the current page.
func (a *App) openAddAccount() { a.addAccount = newOnboarding(a, true) }

// closeAddAccount closes the sheet, cancelling its sign-in.
func (a *App) closeAddAccount() {
	if o := a.addAccount; o != nil {
		o.leave()
		a.addAccount = nil
	}
}

func (o *onboarding) Sidebar(*ui.Context)     {}
func (o *onboarding) Title() (string, string) { return "", "" }
func (o *onboarding) refresh([]events.Event)  {}

// leave drops the step's work in flight and cancels its sign-in.
func (o *onboarding) leave() {
	o.gen++
	if o.detectStop != nil {
		o.detectStop()
		o.detectStop = nil
	}
	if o.flowID != "" {
		o.cancelFlow(o.flowID)
		o.flowID, o.authURL = "", ""
	}
}

func (o *onboarding) cancelFlow(id string) {
	oauth := o.a.oauth
	o.a.do(func(context.Context) error { oauth.Cancel(id); return nil }, nil)
}

func (o *onboarding) goTo(s obStep) {
	o.gen++
	o.step = s
}

func (o *onboarding) toPick() {
	o.leave()
	o.step = stepPick
}

// reset starts over at the pick step, dropping what is in flight.
func (o *onboarding) reset() {
	o.leave()
	*o = onboarding{a: o.a, sheet: o.sheet, open: o.sheet, providers: o.providers, gen: o.gen, syncLimit: defaultSyncLimit,
		add: o.add, update: o.update}
}

func validEmail(s string) bool {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	return err == nil && a.Name == ""
}

func providerName(p auth.Provider) string {
	if s := i18n.T("provider." + p.ID); s != "provider."+p.ID {
		return s
	}
	return p.Name
}

func providerMark(p auth.Provider) string {
	if s := i18n.T("provider." + p.ID + ".mark"); s != "provider."+p.ID+".mark" {
		return s
	}
	return "@"
}

// secretName is what the provider's password is called: 授权码, 应用专用密码…
func (o *onboarding) secretName() string { return i18n.T("onboarding.login." + o.prov.Login) }

// scheduleDetect detects the provider of the address once typing pauses.
func (o *onboarding) scheduleDetect() {
	if o.detectStop != nil {
		o.detectStop()
	}
	email := o.email
	o.detectStop = o.a.after(debounce, func() {
		o.detectStop = nil
		o.detect(email, nil)
	})
}

// detect finds email's provider; then, if given, runs with it unless the
// step changed meanwhile.
func (o *onboarding) detect(email string, then func(auth.Provider)) {
	if !validEmail(email) {
		return
	}
	svc, gen := o.a.svc, o.gen
	var p auth.Provider
	o.a.do(func(ctx context.Context) error {
		var err error
		p, _, err = svc.DetectProvider(ctx, strings.TrimSpace(email))
		return err
	}, func(err error) {
		if gen != o.gen || email != o.email {
			return
		}
		if err != nil {
			o.emailErr = humanize(err)
			return
		}
		o.detected, o.detectedFor = p, email
		if then != nil {
			then(p)
		}
	})
}

// next goes on from the address to its provider's sign-in.
func (o *onboarding) next() {
	if !validEmail(o.email) {
		o.emailErr = i18n.T("onboarding.email.invalid")
		return
	}
	o.emailErr = ""
	if o.detectedFor == o.email {
		o.choose(o.detected)
		return
	}
	o.detect(o.email, o.choose)
}

// choose takes p's servers and goes to its sign-in.
func (o *onboarding) choose(p auth.Provider) {
	o.prov = p
	o.imapHost, o.imapPort, o.imapSec = p.IMAP.Host, strconv.Itoa(p.IMAP.Port), p.IMAP.Security
	o.smtpHost, o.smtpPort, o.smtpSec = p.SMTP.Host, strconv.Itoa(p.SMTP.Port), p.SMTP.Security
	if p.IMAP.Host == "" {
		o.imapPort, o.imapSec, o.smtpPort, o.smtpSec = "993", core.SecurityTLS, "465", core.SecurityTLS
	}
	o.manual = p.IMAP.Host == ""
	o.askEmail = !validEmail(o.email)
	if p.Login == "oauth" {
		o.startOAuth()
		return
	}
	o.goTo(stepPassword)
}

// startOAuth begins a browser sign-in, opens it and waits for it; a finished
// sign-in goes on to the check.
func (o *onboarding) startOAuth() {
	o.leave()
	o.step, o.oauthErr = stepOAuth, nil
	gen, a := o.gen, o.a
	oauth, plat, prov, hint := a.oauth, a.plat, o.prov.ID, strings.TrimSpace(o.email)
	var id, url string
	var openErr error
	a.do(func(ctx context.Context) error {
		var err error
		if id, url, err = oauth.Begin(ctx, prov, hint); err != nil {
			return err
		}
		openErr = plat.OpenURL(url)
		return nil
	}, func(err error) {
		if gen != o.gen {
			if err == nil {
				o.cancelFlow(id)
			}
			return
		}
		if err != nil {
			o.oauthErr = err
			return
		}
		a.toastErr(openErr)
		o.flowID, o.authURL = id, url
		var email string
		a.do(func(ctx context.Context) error {
			var err error
			email, err = oauth.Await(ctx, id)
			return err
		}, func(err error) {
			if gen != o.gen {
				return
			}
			if err != nil {
				o.oauthErr = err
				return
			}
			o.email = email
			o.connect()
		})
	})
}

func (o *onboarding) openURL(url string) {
	plat, a := o.a.plat, o.a
	a.do(func(context.Context) error { return plat.OpenURL(url) }, a.toastErr)
}

func (o *onboarding) input() core.AccountInput {
	ip, _ := strconv.Atoi(strings.TrimSpace(o.imapPort))
	sp, _ := strconv.Atoi(strings.TrimSpace(o.smtpPort))
	in := core.AccountInput{Email: strings.TrimSpace(o.email), Name: strings.TrimSpace(o.name), Username: strings.TrimSpace(o.username),
		IMAPHost: strings.TrimSpace(o.imapHost), IMAPPort: ip, IMAPSecurity: o.imapSec,
		SMTPHost: strings.TrimSpace(o.smtpHost), SMTPPort: sp, SMTPSecurity: o.smtpSec, Provider: o.prov.ID}
	if o.prov.Login == "oauth" {
		in.OAuthFlowID = o.flowID
	} else {
		in.Password = o.secret
	}
	if testSecurity != "" {
		in.IMAPSecurity, in.SMTPSecurity = testSecurity, testSecurity
	}
	return in
}

// connect checks the account; a pass goes on to the done step, a failure
// stays to show which step failed.
func (o *onboarding) connect() {
	from := o.step
	o.goTo(stepChecking)
	o.checking, o.report = true, core.CheckReport{}
	if o.invalid, o.invalidNext = o.validate(); o.invalid != "" {
		o.checking = false
		o.report.Steps = []core.CheckStep{{Name: "settings", Kind: core.KindInvalid}, {Name: "imap", Skipped: true}, {Name: "smtp", Skipped: true}}
		return
	}
	gen, svc, in := o.gen, o.a.svc, o.input()
	var r core.CheckReport
	o.a.do(func(ctx context.Context) error {
		var err error
		r, err = svc.CheckAccount(ctx, in)
		return err
	}, func(err error) {
		if gen != o.gen {
			return
		}
		o.checking = false
		if err != nil {
			o.a.toastErr(err)
			o.goTo(from)
			return
		}
		o.report = r
		if r.OK() {
			o.goTo(stepDone)
		}
	})
}

// validate checks the form as core's settings step does, saying what is
// wrong and what to do in the user's words: what core says is English.
func (o *onboarding) validate() (reason, next string) {
	if !validEmail(o.email) {
		return i18n.T("onboarding.invalid.email"), i18n.T("onboarding.next.email")
	}
	if o.prov.Login == "oauth" {
		return "", ""
	}
	for _, sv := range []struct{ proto, host, port string }{{"IMAP", o.imapHost, o.imapPort}, {"SMTP", o.smtpHost, o.smtpPort}} {
		if strings.TrimSpace(sv.host) == "" {
			return i18n.Tf("onboarding.invalid.host", sv.proto), i18n.T("onboarding.next.host")
		}
		if p, err := strconv.Atoi(strings.TrimSpace(sv.port)); err != nil || p < 1 || p > 65535 {
			return i18n.Tf("onboarding.invalid.port", sv.proto, strings.TrimSpace(sv.port)), i18n.T("onboarding.next.port")
		}
	}
	if o.secret == "" {
		return i18n.Tf("onboarding.invalid.secret", o.secretName()), i18n.T("onboarding.next.secret")
	}
	return "", ""
}

// retry goes back to the sign-in after a failed check, keeping what was typed.
func (o *onboarding) retry(manual bool) {
	if o.prov.Login == "oauth" {
		o.startOAuth()
		return
	}
	o.manual = o.manual || manual
	o.goTo(stepPassword)
}

// finish adds the account, with the default sync range, then sets the
// range chosen; after a failed range update it retries only that. again
// starts over for another account; else the window goes to the mail and
// the sheet closes onto the account's settings. The form is disabled while
// it saves, so what is saved is what shows.
func (o *onboarding) finish(again bool) {
	if o.adding {
		return
	}
	o.adding, o.again, o.saveErr = true, again, nil
	a, add, update, in, limit, id := o.a, o.add, o.update, o.input(), o.syncLimit, o.addedID
	gen := o.gen
	a.do(func(ctx context.Context) error {
		if id == 0 {
			acc, err := add(ctx, in)
			if err != nil {
				return err
			}
			id = acc.ID
		}
		if limit != defaultSyncLimit {
			_, err := update(ctx, id, core.AccountPatch{SyncLimit: &limit})
			return err
		}
		return nil
	}, func(err error) {
		o.adding = false
		if id != 0 && o.addedID == 0 {
			o.addedID, o.flowID = id, "" // the account took the sign-in
			a.load()
		}
		if gen != o.gen { // the sheet closed meanwhile
			return
		}
		if err != nil {
			o.saveErr = err
			return
		}
		switch {
		case again:
			o.reset()
		case o.sheet:
			a.addAccount = nil
			a.navigate(Route{Page: PageSettings, Settings: "account:" + strconv.FormatInt(id, 10)})
		default:
			o.reset()
			a.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
			a.history = nil // not back to the onboarding
		}
	})
}

// Content is the first-run window: the privacy promises on the left, the
// steps on the right.
func (o *onboarding) Content(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	panel := ui.Row(c).Grow(1).AlignItems(ui.Stretch).Margin(0, th.Space(2), th.Space(2), th.Space(2)).Radius(t.Radius.Xl).Clip().Background(t.Background).Border(theme.Hairline, t.Outline)
	kit.Elevate(c, panel, theme.ShadowXs).Children(func() {
		ui.Column(c).Width(theme.AsideWidth).Padding(th.Space(12), th.Space(11)).Gap(th.Space(7)).Radius(t.Radius.Xl-theme.Hairline, 0, 0, t.Radius.Xl-theme.Hairline).Background(t.Sidebar).BorderWidth(0, theme.Hairline, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				ui.Icon(c, logo).Size(theme.BrandLogo, theme.BrandLogo)
				kit.Text(c, i18n.T("onboarding.brand"), t.Text.X2l).FontWeight(600)
			})
			ui.Column(c).Gap(th.Space(2.5)).Children(func() {
				kit.Text(c, i18n.T("onboarding.headline"), t.Text.Display).FontWeight(600)
				ui.Text(c, i18n.T("onboarding.lede")).TextColor(t.Muted).LineHeight(theme.LineLede)
			})
			ui.Column(c).Gap(th.Space(4.5)).Children(func() {
				for _, p := range []struct{ icon, key string }{{"shield", "keychain"}, {"server", "local"}, {"sparkles", "ai"}} {
					ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Start).Children(func() {
						ui.Icon(c, icons.Get(p.icon)).Size(theme.IconMd, theme.IconMd).Margin(th.Space(0.5), 0, 0, 0).TextColor(t.Foreground.Mix(t.Background, 0.25))
						ui.Column(c).Gap(th.Space(0.5)).Shrink(1).Children(func() {
							ui.Text(c, i18n.T("onboarding.promise."+p.key)).FontWeight(500)
							kit.Text(c, i18n.T("onboarding.promise."+p.key+".sub"), t.Text.Sm).TextColor(t.Muted).LineHeight(theme.LineProse)
						})
					})
				}
			})
			ui.Spacer(c)
			kit.Text(c, i18n.T("onboarding.license"), t.Text.Xs).TextColor(t.Muted)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Column(c).FillWidth().MaxWidth(theme.FormMaxWidth).Padding(th.Space(14), th.Space(8)).Gap(th.Space(5.5)).Children(func() { o.body(c) })
			})
		})
	})
}

// sheetView is the 560-wide add-account sheet; closing it cancels the sign-in.
func (o *onboarding) sheetView(c *ui.Context) {
	kit.Sheet(c, &o.open, i18n.T("onboarding.sheet.title"), i18n.T("common.close"), func() { o.body(c) })
	if !o.open {
		o.a.closeAddAccount()
	}
}

func (o *onboarding) body(c *ui.Context) {
	switch o.step {
	case stepPick:
		o.pickView(c)
	case stepPassword:
		o.passwordView(c)
	case stepOAuth:
		o.oauthView(c)
	case stepChecking:
		o.checkView(c)
	case stepDone:
		o.doneView(c)
	}
}

func heading(c *ui.Context, title, sub string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		kit.Text(c, title, t.Text.Xl).FontWeight(600)
		if sub != "" {
			ui.Text(c, sub).TextColor(t.Muted)
		}
	})
}

// mark is a provider's mark: its logo (icons.Brand) on a white tile, else
// its letters on a rounded square.
func mark(c *ui.Context, p auth.Provider, size float32) {
	t := theme.Of(c)
	r := min(t.Radius.Lg, size/4) // a quarter of larger marks
	if size <= theme.MarkSm {
		r = t.Radius.Sm
	}
	if size >= theme.MarkXl {
		r = t.Radius.Xl
	}
	if b := icons.Brand(p.ID); b != nil {
		tile := ui.Box(c).Size(size, size).Radius(r).Clip().Background(t.MarkTile).Border(theme.Hairline, t.Outline)
		in := size - 2*theme.Hairline // inside the outline: a full-size logo would sit off centre, clipped
		kit.Elevate(c, tile, theme.ShadowXs).Children(func() { ui.Image(c, b).Size(in, in) })
		return
	}
	text := providerMark(p)
	fs := t.Text.Xs
	if size >= theme.MarkLg {
		fs = t.Text.Base
	}
	ui.Box(c).Size(size, size).Radius(r).Background(t.Hover).Center().Children(func() {
		kit.Text(c, text, fs).FontWeight(600).TextColor(t.Foreground.Mix(t.Background, 0.25))
	})
}

func primary(c *ui.Context, label string, disabled bool) ui.Element {
	return kit.Button(c, label, kit.ButtonOpts{Size: kit.SizeLg, Disabled: disabled}).FillWidth()
}

func (o *onboarding) pickView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	heading(c, i18n.T("onboarding.pick.title"), i18n.T("onboarding.pick.sub"))
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		box := ui.Row(c).Height(th.Space(10)).AlignItems(ui.Center).Gap(th.Space(2)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).Border(theme.Hairline, t.Input).Background(t.Background)
		if box.FocusWithin() { // the row is the box, so it carries the ring a bare input would
			box.Border(theme.Hairline, t.Ring)
		}
		if o.emailErr != "" {
			box.Border(theme.Hairline, t.Danger)
		}
		kit.Elevate(c, box, theme.ShadowXs).Children(func() {
			in := kit.Input(c, &o.email, kit.InputOpts{Placeholder: i18n.T("onboarding.email.placeholder"), Bare: true}).FocusRing(false).Label(i18n.T("onboarding.email")).Grow(1).Height(th.Space(10))
			if in.Changed() {
				o.emailErr = ""
				o.scheduleDetect()
			}
			if in.Submitted() {
				o.next()
			}
			if o.detectedFor == o.email && o.email != "" && o.detected.ID != "other" {
				kit.Badge(c, i18n.Tf("onboarding.detected", providerName(o.detected)))
			}
		})
		if o.emailErr != "" {
			kit.Text(c, o.emailErr, t.Text.Sm).TextColor(t.Danger)
		}
	})
	primary(c, i18n.T("common.continue"), strings.TrimSpace(o.email) == "").OnClick(o.next)
	ui.Row(c).Gap(th.Space(3)).FontSize(t.Text.Xs).TextColor(t.Muted).Children(func() {
		kit.Separator(c, false).Grow(1)
		ui.Text(c, i18n.T("onboarding.or_pick"))
		kit.Separator(c, false).Grow(1)
	})
	ui.Column(c).Gap(th.Space(2)).Children(func() {
		for i := 0; i < len(o.providers); i += 2 {
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Stretch).Children(func() {
				for _, p := range o.providers[i:min(i+2, len(o.providers))] {
					how := i18n.T("onboarding.login." + p.Login)
					if p.ID == "other" {
						how = i18n.T("onboarding.login.manual")
					}
					kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { o.choose(p) }}).Label(providerName(p)).
						Grow(1).Basis(0).Height(th.Space(12)).PaddingX(th.Space(3)).Gap(th.Space(2.5)).Children(func() {
						mark(c, p, theme.MarkMd)
						ui.Column(c).Shrink(1).Children(func() {
							ui.Text(c, providerName(p)).SingleLine()
							kit.Text(c, how, t.Text.Xs).TextColor(t.Muted).SingleLine()
						})
					})
				}
			})
		}
	})
}

func (o *onboarding) backLink(c *ui.Context) {
	kit.Button(c, i18n.T("onboarding.change"), kit.ButtonOpts{Variant: kit.Link}).AlignSelf(ui.Start).OnClick(o.toPick)
}

func (o *onboarding) providerHeader(c *ui.Context, sub string) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(3)).Children(func() {
		mark(c, o.prov, theme.MarkLg)
		ui.Column(c).Children(func() {
			kit.Text(c, providerName(o.prov), t.Text.Xl).FontWeight(600)
			if sub != "" {
				ui.Text(c, sub).TextColor(t.Muted)
			}
		})
	})
}

// tr is key's message, or "" when the catalog has none.
func tr(key string) string {
	if s := i18n.T(key); s != key {
		return s
	}
	return ""
}

func (o *onboarding) passwordView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	o.backLink(c)
	sub := strings.TrimSpace(o.email)
	if o.askEmail {
		sub = ""
	}
	o.providerHeader(c, sub)
	if o.askEmail {
		ui.Field(c, i18n.T("onboarding.email"), func() {
			kit.Input(c, &o.email, kit.InputOpts{Placeholder: i18n.T("onboarding.email.placeholder")}).Height(th.Space(10))
		})
	}
	name, login := providerName(o.prov), o.prov.Login
	hint := tr("onboarding.hint." + o.prov.ID)
	if hint == "" && tr("onboarding.hint."+login) != "" {
		hint = i18n.Tf("onboarding.hint."+login, name)
	}
	if hint != "" {
		ui.Column(c).Gap(th.Space(2.5)).Padding(th.Space(3.5), th.Space(4)).Radius(t.Radius.Xl).Background(t.Sidebar).Border(theme.Hairline, t.Hover).Children(func() {
			ui.Text(c, hint).FontWeight(500)
			steps := tr("onboarding.steps." + o.prov.ID)
			if steps == "" {
				steps = tr("onboarding.steps." + login)
			}
			ui.Column(c).Gap(th.Space(1)).Children(func() {
				for i, line := range strings.Split(steps, "\n") {
					ui.Text(c, strconv.Itoa(i+1)+". "+line).TextColor(t.Foreground.Mix(t.Background, 0.25)).LineHeight(theme.LineProse)
				}
			})
			if u := o.prov.HelpURL; u != "" {
				kit.Button(c, i18n.Tf("onboarding.open_help", name), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeSm}).AlignSelf(ui.Start).
					OnClick(func() { o.openURL(u) })
			}
		})
	}
	ui.Field(c, o.secretName(), func() {
		if kit.Input(c, &o.secret, kit.InputOpts{Password: true}).Height(th.Space(10)).Submitted() && o.secret != "" {
			o.connect()
		}
	}).Description(i18n.T("onboarding.secret.help"))
	primary(c, i18n.T("onboarding.connect"), o.secret == "" || !validEmail(o.email)).OnClick(o.connect)
	o.manualView(c)
}

var securities = []kit.Option[core.Security]{
	{Value: core.SecurityTLS, Label: "onboarding.security.tls"},
	{Value: core.SecurityStartTLS, Label: "onboarding.security.starttls"},
}

// manualView is the collapsible server form: hosts, ports and TLS or STARTTLS.
func (o *onboarding) manualView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	icon := "chevron-right"
	if o.manual {
		icon = "chevron-down"
	}
	kit.Button(c, i18n.T("onboarding.manual"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Icon: icon}).AlignSelf(ui.Start).
		TextColor(t.Muted).OnClick(func() { o.manual = !o.manual })
	if !o.manual {
		return
	}
	opts := make([]kit.Option[core.Security], len(securities))
	for i, s := range securities {
		opts[i] = kit.Option[core.Security]{Value: s.Value, Label: i18n.T(s.Label)}
	}
	server := func(proto string, host, port *string, sec *core.Security) {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Start).Children(func() {
			ui.Field(c, i18n.T("onboarding."+proto+".host"), func() { kit.Input(c, host, kit.InputOpts{}) }).Grow(1).FontSize(t.Text.Sm).TextColor(t.Muted)
			ui.Field(c, i18n.T("onboarding.port"), func() { kit.Input(c, port, kit.InputOpts{}).Label(i18n.T("onboarding." + proto + ".port")) }).Width(theme.PortWidth).FontSize(t.Text.Sm).TextColor(t.Muted)
			ui.Field(c, i18n.T("onboarding."+proto+".security"), func() { kit.Select(c, sec, opts) }).FontSize(t.Text.Sm).TextColor(t.Muted)
		})
	}
	ui.Column(c).Gap(th.Space(3)).Children(func() {
		server("imap", &o.imapHost, &o.imapPort, &o.imapSec)
		server("smtp", &o.smtpHost, &o.smtpPort, &o.smtpSec)
		ui.Field(c, i18n.T("onboarding.username"), func() {
			kit.Input(c, &o.username, kit.InputOpts{Placeholder: i18n.T("onboarding.username.placeholder")})
		})
	})
}

func (o *onboarding) oauthView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	o.backLink(c)
	o.providerHeader(c, i18n.T("onboarding.oauth.sub"))
	ui.Column(c).Gap(th.Space(3)).Padding(th.Space(5)).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).BorderStyle(ui.BorderDashed).AlignItems(ui.Center).Children(func() {
		if o.oauthErr != nil {
			kit.Alert(c, kit.Danger, humanize(o.oauthErr), func() {
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(o.startOAuth)
			})
			return
		}
		ui.Box(c).Size(theme.PendingDot, theme.PendingDot).Radius(t.Radius.Full).Background(t.Ring)
		title := i18n.T("onboarding.oauth.opening")
		if o.authURL != "" {
			title = i18n.T("onboarding.oauth.opened")
		}
		ui.Text(c, title).FontWeight(500)
		kit.Text(c, i18n.T("onboarding.oauth.body"), t.Text.Sm).TextColor(t.Muted).TextAlign(ui.Center).LineHeight(theme.LineProse)
		u := o.authURL
		kit.Button(c, i18n.T("onboarding.oauth.reopen"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: u == ""}).
			OnClick(func() { o.openURL(u) })
	})
}

// checkRow says how one check step went: its text, the reason or next step
// below it, and its status (0 waiting, 1 passed, 2 failed).
func (o *onboarding) checkRow(st core.CheckStep) (text, detail string, status int) {
	k := "onboarding.check." + st.Name
	switch {
	case o.checking:
		return i18n.T(k + ".running"), "", 0
	case st.OK:
		if st.Name == "settings" {
			src := "builtin"
			if o.manual || o.prov.ID == "other" {
				src = "manual"
			}
			detail = i18n.Tf("onboarding.check.settings."+src, o.imapHost, o.smtpHost)
		}
		return i18n.T(k + ".ok"), detail, 1
	case st.Skipped:
		return i18n.T(k + ".waiting"), i18n.T("onboarding.check.waiting"), 0
	}
	reason, next := st.Detail, ""
	if st.Name == "settings" {
		return i18n.Tf(k+".failed", o.settingsReason(st)), o.settingsNext(), 2
	}
	switch kind := string(st.Kind); kind {
	case "auth":
		switch login := o.prov.Login; login {
		case "oauth":
			reason, next = i18n.T("onboarding.reason.oauth"), i18n.T("onboarding.next.oauth")
		case "authorization_code", "app_password":
			reason, next = i18n.Tf("onboarding.reason.auth", o.secretName()), i18n.Tf("onboarding.next.regenerate", providerName(o.prov))
		default:
			reason, next = i18n.Tf("onboarding.reason.auth", o.secretName()), i18n.T("onboarding.next.password")
		}
	case "dns", "connect", "timeout", "tls", "protocol":
		reason, next = i18n.T("onboarding.reason."+kind), i18n.T("onboarding.next.server")
	}
	return i18n.Tf(k+".failed", reason), next, 2
}

// settingsReason words a failed settings step: the form's own check, or one
// of the sign-in errors core finds; never core's English.
func (o *onboarding) settingsReason(st core.CheckStep) string {
	switch st.Detail {
	case "":
		if o.invalid != "" {
			return o.invalid
		}
	case core.ErrUnknownFlow.Error():
		return i18n.T("onboarding.invalid.flow")
	case core.ErrWrongAccount.Error():
		return i18n.T("onboarding.invalid.wrong_account")
	case core.ErrProviderMismatch.Error():
		return i18n.T("onboarding.invalid.provider")
	}
	return i18n.T("onboarding.invalid.other")
}

func (o *onboarding) settingsNext() string {
	switch {
	case o.invalid != "":
		return o.invalidNext
	case o.prov.Login == "oauth":
		return i18n.T("onboarding.next.oauth")
	}
	return i18n.T("onboarding.next.server")
}

func (o *onboarding) checkView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	steps := o.report.Steps
	if o.checking || len(steps) == 0 {
		steps = []core.CheckStep{{Name: "settings"}, {Name: "imap"}, {Name: "smtp"}}
	}
	failed := !o.checking && !o.report.OK()
	title := i18n.T("onboarding.check.connecting")
	if failed {
		title = i18n.T("onboarding.check.failed")
	}
	heading(c, title, strings.TrimSpace(o.email))
	ui.Column(c).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).Clip().Children(func() {
		for i, st := range steps {
			text, detail, status := o.checkRow(st)
			row := ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Start).Padding(th.Space(3), th.Space(3.5))
			if i > 0 {
				row.BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Hover)
			}
			row.Children(func() {
				dot := ui.Box(c).Size(theme.StepDot, theme.StepDot).Radius(t.Radius.Full).Center().FontSize(t.Text.Xs)
				switch status {
				case 1:
					dot.Background(t.SuccessBg).TextColor(t.SuccessText).Children(func() { ui.Icon(c, icons.Get("check")).Size(theme.IconXs, theme.IconXs) })
				case 2:
					dot.Background(t.DangerBg).TextColor(t.DangerText).Children(func() { ui.Icon(c, icons.Get("x")).Size(theme.IconXs, theme.IconXs) })
				default:
					dot.Background(t.Hover).TextColor(t.Muted).Children(func() { ui.Text(c, "…") })
				}
				ui.Column(c).Gap(th.Space(0.5)).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, text)
					if detail != "" {
						kit.Text(c, detail, t.Text.Xs).TextColor(t.Muted)
					}
				})
			})
		}
	})
	if failed {
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			if o.prov.Login == "oauth" {
				kit.Button(c, i18n.T("onboarding.relogin"), kit.ButtonOpts{Size: kit.SizeLg}).OnClick(func() { o.retry(false) })
				return
			}
			kit.Button(c, i18n.Tf("onboarding.retype", o.secretName()), kit.ButtonOpts{Size: kit.SizeLg}).OnClick(func() { o.retry(false) })
			kit.Button(c, i18n.T("onboarding.manual"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeLg}).OnClick(func() { o.retry(true) })
		})
	}
}

var syncLimits = []int{2000, 10000, 100000}

func (o *onboarding) doneView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		ui.Box(c).Size(theme.DoneBadge, theme.DoneBadge).Radius(t.Radius.Full).Background(t.SuccessBg).TextColor(t.SuccessText).Center().Children(func() {
			ui.Icon(c, icons.Get("check")).Size(theme.IconMd, theme.IconMd)
		})
		kit.Text(c, i18n.T("onboarding.done.title"), t.Text.Xl).FontWeight(600).Margin(th.Space(2), 0, 0, 0)
		ui.Text(c, strings.TrimSpace(o.email)).TextColor(t.Muted)
	})
	ui.Field(c, i18n.T("onboarding.name"), func() {
		kit.Input(c, &o.name, kit.InputOpts{Placeholder: i18n.T("onboarding.name.placeholder")}).Height(th.Space(10)).Disabled(o.adding || o.addedID != 0) // the name saves with the account
	}).Description(i18n.T("onboarding.name.help"))
	opts := make([]kit.Option[int], len(syncLimits))
	for i, n := range syncLimits {
		opts[i] = kit.Option[int]{Value: n, Label: i18n.T("onboarding.sync." + strconv.Itoa(n))}
	}
	ui.Field(c, i18n.T("onboarding.sync"), func() { kit.Select(c, &o.syncLimit, opts).Disabled(o.adding) }).Description(i18n.T("onboarding.sync.help"))
	if o.saveErr != nil {
		kit.Alert(c, kit.Danger, humanize(o.saveErr), func() {
			kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() { o.finish(o.again) })
		})
	}
	ui.Column(c).Gap(th.Space(2)).Children(func() {
		label := i18n.T("onboarding.start")
		if o.sheet {
			label = i18n.T("common.done")
		}
		primary(c, label, o.adding).OnClick(func() { o.finish(false) })
		kit.Button(c, i18n.T("onboarding.another"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeLg, Disabled: o.adding}).FillWidth().
			OnClick(func() { o.finish(true) })
	})
}

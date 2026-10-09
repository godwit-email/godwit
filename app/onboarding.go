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

	prov               auth.Provider
	askEmail           bool // the provider was picked before an address was typed
	secret, username   string
	manual             bool
	imapHost, imapPort string
	smtpHost, smtpPort string
	imapSec, smtpSec   core.Security
	flowID, authURL    string
	oauthErr           error
	report             core.CheckReport
	checking, adding   bool
	name               string
	syncLimit          int
}

func newOnboarding(a *App, sheet bool) *onboarding {
	return &onboarding{a: a, sheet: sheet, open: sheet, providers: append(a.svc.ListProviders(), otherProvider), syncLimit: defaultSyncLimit}
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
	*o = onboarding{a: o.a, sheet: o.sheet, open: o.sheet, providers: o.providers, gen: o.gen, syncLimit: defaultSyncLimit}
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
		if err != nil || email != o.email {
			return
		}
		o.detected, o.detectedFor = p, email
		if then != nil && gen == o.gen {
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

// retry goes back to the sign-in after a failed check, keeping what was typed.
func (o *onboarding) retry(manual bool) {
	if o.prov.Login == "oauth" {
		o.startOAuth()
		return
	}
	o.manual = o.manual || manual
	o.goTo(stepPassword)
}

// finish adds the account, with the default sync range and then the one
// chosen. again starts over for another account; else the window goes to
// the mail and the sheet closes onto the account's settings.
func (o *onboarding) finish(again bool) {
	if o.adding {
		return
	}
	o.adding = true
	a, svc, in, limit := o.a, o.a.svc, o.input(), o.syncLimit
	var acc core.Account
	a.do(func(ctx context.Context) error {
		var err error
		if acc, err = svc.AddAccount(ctx, in); err != nil {
			return err
		}
		if limit != acc.SyncLimit {
			_, err = svc.UpdateAccount(ctx, acc.ID, core.AccountPatch{SyncLimit: &limit})
		}
		return err
	}, func(err error) {
		o.adding = false
		a.toastErr(err)
		if acc.ID == 0 {
			return
		}
		o.flowID = "" // the account took the sign-in
		a.load()
		switch {
		case again:
			o.reset()
		case o.sheet:
			if a.addAccount == o {
				a.addAccount = nil
				a.navigate(Route{Page: PageSettings, Settings: "account:" + strconv.FormatInt(acc.ID, 10)})
			}
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
	t := theme.Of(c)
	s := t.Scale
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Margin(0, 8, 8, 8).Radius(12).Clip().Background(t.Background).
		Border(1, t.Border).Children(func() {
		ui.Column(c).Width(440).Padding(48*s, 44*s).Gap(28*s).Background(t.Sidebar).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Gap(10).Children(func() {
				ui.Icon(c, icons.Get("mail")).Size(28, 28)
				ui.Text(c, i18n.T("onboarding.brand")).FontSize(18).FontWeight(600)
			})
			ui.Column(c).Gap(10).Children(func() {
				ui.Text(c, i18n.T("onboarding.headline")).FontSize(30).FontWeight(600)
				ui.Text(c, i18n.T("onboarding.lede")).TextColor(t.Muted).LineHeight(1.7)
			})
			ui.Column(c).Gap(18 * s).Children(func() {
				for _, p := range []struct{ icon, key string }{{"shield", "keychain"}, {"server", "local"}, {"sparkles", "ai"}} {
					ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
						ui.Icon(c, icons.Get(p.icon)).Size(18, 18).Margin(2, 0, 0, 0).TextColor(t.Foreground.Mix(t.Background, 0.25))
						ui.Column(c).Gap(2).Shrink(1).Children(func() {
							ui.Text(c, i18n.T("onboarding.promise."+p.key)).FontWeight(500)
							ui.Text(c, i18n.T("onboarding.promise."+p.key+".sub")).FontSize(t.FontSize - 1).TextColor(t.Muted).LineHeight(1.6)
						})
					})
				}
			})
			ui.Spacer(c)
			ui.Text(c, i18n.T("onboarding.license")).FontSize(t.FontSize - 2).TextColor(t.Muted)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Column(c).FillWidth().MaxWidth(520).Padding(56*s, 32*s).Gap(22 * s).Children(func() { o.body(c) })
			})
		})
	})
}

// sheetView is the 560-wide add-account sheet; closing it cancels the sign-in.
func (o *onboarding) sheetView(c *ui.Context) {
	t := theme.Of(c)
	s := t.Scale
	ink := t.Foreground
	if t.Dark {
		ink = t.Background
	}
	ui.DialogBase(c, &o.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ink.Alpha(0.28)).Justify(ui.Start).Padding(64, 0, 0, 0)
		panel.Width(560).MaxHeight(760).Radius(14).Clip().Background(t.Background).Border(1, t.Border).
			Shadow(0, 24, 64, 0, ink.Alpha(0.18))
		ui.Row(c).Padding(20*s, 24*s, 0, 24*s).Children(func() {
			ui.Text(c, i18n.T("onboarding.sheet.title")).FontSize(16).FontWeight(600)
			ui.Spacer(c)
			kit.IconButton(c, "x", i18n.T("common.close"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).OnClick(func() { o.open = false })
		})
		ui.Scroll(c).Shrink(1).Children(func() {
			ui.Column(c).Padding(16*s, 24*s, 24*s, 24*s).Gap(22 * s).Children(func() { o.body(c) })
		})
	})
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
	t := theme.Of(c)
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, title).FontSize(20).FontWeight(600)
		if sub != "" {
			ui.Text(c, sub).TextColor(t.Muted)
		}
	})
}

func mark(c *ui.Context, text string, size float32) {
	t := theme.Of(c)
	fs := t.FontSize - 2
	if size > 30 {
		fs = t.FontSize
	}
	ui.Box(c).Size(size, size).Radius(min(8, size/4)).Background(t.Hover).Center().Children(func() {
		ui.Text(c, text).FontSize(fs).FontWeight(600).TextColor(t.Foreground.Mix(t.Background, 0.25))
	})
}

func primary(c *ui.Context, label string, disabled bool) ui.Element {
	return kit.Button(c, label, kit.ButtonOpts{Disabled: disabled}).FillWidth()
}

func (o *onboarding) pickView(c *ui.Context) {
	t := theme.Of(c)
	s := t.Scale
	heading(c, i18n.T("onboarding.pick.title"), i18n.T("onboarding.pick.sub"))
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			in := kit.Input(c, &o.email, kit.InputOpts{Placeholder: i18n.T("onboarding.email.placeholder"), Invalid: o.emailErr != ""}).Label(i18n.T("onboarding.email")).Grow(1)
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
			ui.Text(c, o.emailErr).FontSize(t.FontSize - 1).TextColor(t.Danger)
		}
	})
	primary(c, i18n.T("common.continue"), strings.TrimSpace(o.email) == "").OnClick(o.next)
	ui.Row(c).Gap(12).FontSize(t.FontSize - 2).TextColor(t.Muted).Children(func() {
		ui.Box(c).Height(1).Grow(1).Background(t.Border)
		ui.Text(c, i18n.T("onboarding.or_pick"))
		ui.Box(c).Height(1).Grow(1).Background(t.Border)
	})
	ui.Column(c).Gap(8).Children(func() {
		for i := 0; i < len(o.providers); i += 2 {
			ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
				for _, p := range o.providers[i:min(i+2, len(o.providers))] {
					how := i18n.T("onboarding.login." + p.Login)
					if p.ID == "other" {
						how = i18n.T("onboarding.login.manual")
					}
					b := ui.ButtonBase(c).Grow(1).Basis(0).Justify(ui.Start).Height(48*s).PaddingX(12).Gap(10).Radius(8).Border(1, t.Border).Background(t.Background)
					if b.Hovered() {
						b.Background(t.Hover)
					}
					b.Label(providerName(p)).Children(func() {
						mark(c, providerMark(p), 28)
						ui.Column(c).Shrink(1).Children(func() {
							ui.Text(c, providerName(p)).SingleLine()
							ui.Text(c, how).FontSize(t.FontSize - 2).TextColor(t.Muted).SingleLine()
						})
					}).OnClick(func() { o.choose(p) })
				}
			})
		}
	})
}

func (o *onboarding) backLink(c *ui.Context) {
	t := theme.Of(c)
	b := ui.ButtonBase(c).AlignSelf(ui.Start).TextColor(t.Muted)
	if b.Hovered() {
		b.TextColor(t.Foreground)
	}
	b.Children(func() { ui.Text(c, i18n.T("onboarding.change")) }).OnClick(o.toPick)
}

func (o *onboarding) providerHeader(c *ui.Context, sub string) {
	ui.Row(c).Gap(12).Children(func() {
		mark(c, providerMark(o.prov), 40)
		ui.Column(c).Children(func() {
			ui.Text(c, providerName(o.prov)).FontSize(20).FontWeight(600)
			if sub != "" {
				ui.Text(c, sub).TextColor(theme.Of(c).Muted)
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
	t := theme.Of(c)
	o.backLink(c)
	sub := strings.TrimSpace(o.email)
	if o.askEmail {
		sub = ""
	}
	o.providerHeader(c, sub)
	if o.askEmail {
		ui.Field(c, i18n.T("onboarding.email"), func() {
			kit.Input(c, &o.email, kit.InputOpts{Placeholder: i18n.T("onboarding.email.placeholder")})
		})
	}
	name, login := providerName(o.prov), o.prov.Login
	hint := tr("onboarding.hint." + o.prov.ID)
	if hint == "" && tr("onboarding.hint."+login) != "" {
		hint = i18n.Tf("onboarding.hint."+login, name)
	}
	if hint != "" {
		ui.Column(c).Gap(10).Padding(14, 16).Radius(10).Background(t.Sidebar).Border(1, t.Border).Children(func() {
			ui.Text(c, hint).FontWeight(500)
			steps := tr("onboarding.steps." + o.prov.ID)
			if steps == "" {
				steps = tr("onboarding.steps." + login)
			}
			ui.Column(c).Gap(4).Children(func() {
				for i, line := range strings.Split(steps, "\n") {
					ui.Text(c, strconv.Itoa(i+1)+". "+line).TextColor(t.Foreground.Mix(t.Background, 0.25)).LineHeight(1.6)
				}
			})
			if u := o.prov.HelpURL; u != "" {
				kit.Button(c, i18n.Tf("onboarding.open_help", name), kit.ButtonOpts{Variant: kit.Outline, Small: true}).AlignSelf(ui.Start).
					OnClick(func() { o.openURL(u) })
			}
		})
	}
	ui.Field(c, o.secretName(), func() {
		if kit.Input(c, &o.secret, kit.InputOpts{Password: true}).Submitted() && o.secret != "" {
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
	t := theme.Of(c)
	icon := "chevron-right"
	if o.manual {
		icon = "chevron-down"
	}
	kit.Button(c, i18n.T("onboarding.manual"), kit.ButtonOpts{Variant: kit.Ghost, Small: true, Icon: icon}).AlignSelf(ui.Start).
		TextColor(t.Muted).OnClick(func() { o.manual = !o.manual })
	if !o.manual {
		return
	}
	opts := make([]kit.Option[core.Security], len(securities))
	for i, s := range securities {
		opts[i] = kit.Option[core.Security]{Value: s.Value, Label: i18n.T(s.Label)}
	}
	server := func(proto string, host, port *string, sec *core.Security) {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Field(c, i18n.T("onboarding."+proto+".host"), func() { kit.Input(c, host, kit.InputOpts{}) }).Grow(1)
			ui.Field(c, i18n.T("onboarding."+proto+".port"), func() { kit.Input(c, port, kit.InputOpts{}) }).Width(90)
			ui.Field(c, i18n.T("onboarding."+proto+".security"), func() { kit.Select(c, sec, opts) })
		})
	}
	ui.Column(c).Gap(12).Children(func() {
		server("imap", &o.imapHost, &o.imapPort, &o.imapSec)
		server("smtp", &o.smtpHost, &o.smtpPort, &o.smtpSec)
		ui.Field(c, i18n.T("onboarding.username"), func() {
			kit.Input(c, &o.username, kit.InputOpts{Placeholder: i18n.T("onboarding.username.placeholder")})
		})
	})
}

func (o *onboarding) oauthView(c *ui.Context) {
	t := theme.Of(c)
	o.backLink(c)
	o.providerHeader(c, i18n.T("onboarding.oauth.sub"))
	ui.Column(c).Gap(12).Padding(20).Radius(10).Border(1, t.Border).BorderStyle(ui.BorderDashed).AlignItems(ui.Center).Children(func() {
		if o.oauthErr != nil {
			kit.Alert(c, kit.Danger, humanize(o.oauthErr), func() {
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).OnClick(o.startOAuth)
			})
			return
		}
		ui.Box(c).Size(10, 10).Radius(5).Background(t.Muted)
		title := i18n.T("onboarding.oauth.opening")
		if o.authURL != "" {
			title = i18n.T("onboarding.oauth.opened")
		}
		ui.Text(c, title).FontWeight(500)
		ui.Text(c, i18n.T("onboarding.oauth.body")).FontSize(t.FontSize - 1).TextColor(t.Muted).TextAlign(ui.Center).LineHeight(1.6)
		u := o.authURL
		kit.Button(c, i18n.T("onboarding.oauth.reopen"), kit.ButtonOpts{Variant: kit.Outline, Small: true, Disabled: u == ""}).
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

func (o *onboarding) checkView(c *ui.Context) {
	t := theme.Of(c)
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
	ui.Column(c).Radius(10).Border(1, t.Border).Clip().Children(func() {
		for i, st := range steps {
			text, detail, status := o.checkRow(st)
			row := ui.Row(c).Gap(12).AlignItems(ui.Start).Padding(12, 14)
			if i > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(t.Hover)
			}
			row.Children(func() {
				dot := ui.Box(c).Size(20, 20).Radius(10).Center().FontSize(12)
				switch status {
				case 1:
					dot.Background(t.Success.Alpha(0.12)).TextColor(t.Success).Children(func() { ui.Icon(c, icons.Get("check")).Size(12, 12) })
				case 2:
					dot.Background(t.DangerBg).TextColor(t.Danger).Children(func() { ui.Icon(c, icons.Get("x")).Size(12, 12) })
				default:
					dot.Background(t.Hover).TextColor(t.Muted).Children(func() { ui.Text(c, "…") })
				}
				ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, text)
					if detail != "" {
						ui.Text(c, detail).FontSize(t.FontSize - 2).TextColor(t.Muted)
					}
				})
			})
		}
	})
	if failed {
		ui.Row(c).Gap(8).Children(func() {
			if o.prov.Login == "oauth" {
				kit.Button(c, i18n.T("onboarding.relogin"), kit.ButtonOpts{}).OnClick(func() { o.retry(false) })
				return
			}
			kit.Button(c, i18n.Tf("onboarding.retype", o.secretName()), kit.ButtonOpts{}).OnClick(func() { o.retry(false) })
			kit.Button(c, i18n.T("onboarding.manual"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { o.retry(true) })
		})
	}
}

var syncLimits = []int{2000, 10000, 100000}

func (o *onboarding) doneView(c *ui.Context) {
	t := theme.Of(c)
	ui.Column(c).Gap(6).Children(func() {
		ui.Box(c).Size(36, 36).Radius(18).Background(t.Success.Alpha(0.12)).TextColor(t.Success).Center().Children(func() {
			ui.Icon(c, icons.Get("check")).Size(18, 18)
		})
		ui.Text(c, i18n.T("onboarding.done.title")).FontSize(20).FontWeight(600).Margin(8, 0, 0, 0)
		ui.Text(c, strings.TrimSpace(o.email)).TextColor(t.Muted)
	})
	ui.Field(c, i18n.T("onboarding.name"), func() {
		kit.Input(c, &o.name, kit.InputOpts{Placeholder: i18n.T("onboarding.name.placeholder")})
	}).Description(i18n.T("onboarding.name.help"))
	opts := make([]kit.Option[int], len(syncLimits))
	for i, n := range syncLimits {
		opts[i] = kit.Option[int]{Value: n, Label: i18n.T("onboarding.sync." + strconv.Itoa(n))}
	}
	ui.Field(c, i18n.T("onboarding.sync"), func() { kit.Select(c, &o.syncLimit, opts) }).Description(i18n.T("onboarding.sync.help"))
	ui.Column(c).Gap(8).Children(func() {
		label := i18n.T("onboarding.start")
		if o.sheet {
			label = i18n.T("common.done")
		}
		primary(c, label, o.adding).OnClick(func() { o.finish(false) })
		kit.Button(c, i18n.T("onboarding.another"), kit.ButtonOpts{Variant: kit.Outline, Disabled: o.adding}).FillWidth().
			OnClick(func() { o.finish(true) })
	})
}

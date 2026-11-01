package app

import (
	"context"
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

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

var defaultSync = core.SyncRange{Limit: 2000}

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
	focusEmail           bool // place the caret before a newly prefilled domain once
	secret, username     string
	showSecret           bool
	tab                  int        // ownClientOffered's tabs: 0 the secret, 1 the browser with the user's own OAuth client
	client               clientForm // the browser tab's own OAuth client
	manual               bool
	imapHost, imapPort   string
	smtpHost, smtpPort   string
	imapSec, smtpSec     core.Security
	flowID, authURL      string
	oauthErr             error
	report               core.CheckReport
	checking, adding     bool
	confetti             *burst // thrown as the check passes, dropped once it lands
	invalid, invalidNext string // the form's own settings-step failure, in words
	name                 string
	sync                 core.SyncRange
	again                bool  // finish starts over for another account
	addedID              int64 // the account added
	saveErr              error

	// add saves the account: the Service's, swapped by tests.
	add func(context.Context, core.AccountInput) (core.Account, error)
}

func newOnboarding(a *App, sheet bool) *onboarding {
	return &onboarding{a: a, sheet: sheet, open: sheet, providers: append(a.svc.ListProviders(), otherProvider), sync: defaultSync,
		add: a.svc.AddAccount}
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
	o.showSecret = false
}

func (o *onboarding) toPick() {
	o.leave()
	if o.askEmail && len(o.prov.Domains) > 0 && o.email == "@"+o.prov.Domains[0] {
		o.email = ""
	}
	o.step = stepPick
}

// reset starts over at the pick step, dropping what is in flight.
func (o *onboarding) reset() {
	o.leave()
	*o = onboarding{a: o.a, sheet: o.sheet, open: o.sheet, providers: o.providers, gen: o.gen, sync: defaultSync,
		add: o.add}
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

// listed reports whether p has its own tile: Lark (Feishu outside China)
// shares Feishu's, and the address's MX picks between them.
func listed(p auth.Provider) bool { return p.ID != "lark" }

// tileName is p's name on its tile: Feishu's is 飞书/Lark.
func tileName(p auth.Provider) string {
	if p.ID == "feishu" {
		return i18n.T("provider.feishu_lark")
	}
	return providerName(p)
}

// feishuOrLark reports whether p is behind the 飞书/Lark tile.
func feishuOrLark(p auth.Provider) bool { return p.ID == "feishu" || p.ID == "lark" }

// provName is the sign-in's provider name: 飞书/Lark from that tile until
// the address is found to be Feishu's.
func (o *onboarding) provName() string {
	if o.prov.ID == "feishu" && o.askEmail && !(o.detectedFor == o.email && o.detected.ID == "feishu") {
		return tileName(o.prov)
	}
	return providerName(o.prov)
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
		o.found(p, email)
		if then != nil {
			then(p)
		}
	})
}

// found records p as email's provider. From the 飞书/Lark tile, it also says
// which of the two the sign-in is, unless the servers were edited.
func (o *onboarding) found(p auth.Provider, email string) {
	o.detected, o.detectedFor = p, email
	if o.step == stepPassword && feishuOrLark(o.prov) && feishuOrLark(p) && p.ID != o.prov.ID &&
		o.imapHost == o.prov.IMAP.Host && o.smtpHost == o.prov.SMTP.Host {
		o.prov = p
		o.imapHost, o.smtpHost = p.IMAP.Host, p.SMTP.Host
	}
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
	if feishuOrLark(p) && o.detectedFor == o.email && feishuOrLark(o.detected) {
		p = o.detected // the 飞书/Lark tile for an address already found
	}
	if p.ID != o.prov.ID { // what was typed or imported is for the provider it was given to
		o.secret, o.client = "", clientForm{}
	}
	o.prov, o.tab = p, 0
	o.imapHost, o.imapPort, o.imapSec = p.IMAP.Host, strconv.Itoa(p.IMAP.Port), p.IMAP.Security
	o.smtpHost, o.smtpPort, o.smtpSec = p.SMTP.Host, strconv.Itoa(p.SMTP.Port), p.SMTP.Security
	if p.IMAP.Host == "" {
		o.imapPort, o.imapSec, o.smtpPort, o.smtpSec = "993", core.SecurityTLS, "465", core.SecurityTLS
	}
	o.manual = p.IMAP.Host == ""
	o.askEmail = !validEmail(o.email)
	o.focusEmail = false
	if p.Login == "oauth" {
		o.startOAuth()
		return
	}
	if o.email == "" && len(p.Domains) > 0 {
		o.email = "@" + p.Domains[0]
		o.focusEmail = true
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
	var client core.OAuthClient
	if o.ownClient() {
		client = o.client.client(publicClient(o.prov))
	}
	var id, url string
	var openErr error
	a.do(func(ctx context.Context) error {
		var err error
		if id, url, err = oauth.Begin(ctx, prov, hint, client); err != nil {
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
			if !errors.Is(err, context.Canceled) {
				a.plat.ShowWindow() // the user is in the browser; bring them back
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
	if o.oauth() {
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
	if feishuOrLark(o.prov) && o.detectedFor != o.email && validEmail(o.email) {
		o.detect(o.email, func(auth.Provider) { o.connect() }) // Feishu or Lark first
		return
	}
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
			b := newBurst()
			o.confetti = b
			o.a.after(confettiLife, func() {
				if o.confetti == b {
					o.confetti = nil
				}
			})
		}
	})
}

// validate checks the form as core's settings step does, saying what is
// wrong and what to do in the user's words: what core says is English.
func (o *onboarding) validate() (reason, next string) {
	if !validEmail(o.email) {
		return i18n.T("onboarding.invalid.email"), i18n.T("onboarding.next.email")
	}
	if o.oauth() {
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
	if o.oauth() {
		o.startOAuth()
		return
	}
	o.manual = o.manual || manual
	o.goTo(stepPassword)
}

// finish adds the account with the sync range chosen, in one write, so its
// first sync never runs with another range. again
// starts over for another account; else the window goes to the mail and
// the sheet closes onto the account's settings. The form is disabled while
// it saves, so what is saved is what shows.
func (o *onboarding) finish(again bool) {
	if o.adding {
		return
	}
	o.adding, o.again, o.saveErr = true, again, nil
	a, add, in, id := o.a, o.add, o.input(), o.addedID
	sync := o.sync
	in.Sync = &sync
	gen := o.gen
	a.do(func(ctx context.Context) error {
		if id == 0 {
			acc, err := add(ctx, in)
			if err != nil {
				return err
			}
			id = acc.ID
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

// Content is the first-run window: the privacy promises on the window's
// material on the left, the steps on a panel on the right.
func (o *onboarding) Content(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Gap(th.Space(2)).Margin(0, th.Space(2), th.Space(2), th.Space(2)).Children(func() {
		ui.Column(c).Width(theme.AsideWidth).Shrink(0).Padding(th.Space(12), th.Space(11)).Gap(th.Space(7)).Children(func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				ui.Icon(c, logo).Size(theme.BrandLogo, theme.BrandLogo)
				kit.Text(c, i18n.T("onboarding.brand"), t.Text.X2l).FontWeight(600)
			})
			ui.Column(c).Gap(th.Space(2.5)).Children(func() {
				kit.Text(c, i18n.T("onboarding.headline"), t.Text.Display).FontWeight(600)
				ui.Text(c, i18n.T("onboarding.lede")).TextColor(t.Muted).LineHeight(theme.LineLede)
			})
			ui.Column(c).Gap(th.Space(4.5)).Children(func() {
				for _, p := range []struct{ icon, key string }{{"lock", "keychain"}, {"monitor", "local"}, {"sparkles", "ai"}} {
					ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Start).Children(func() {
						ui.Icon(c, icons.Get(p.icon)).Size(theme.IconMd, theme.IconMd).Margin(th.Space(0.5), 0, 0, 0).TextColor(t.Foreground.Mix(t.Background, 0.25))
						ui.Column(c).Gap(th.Space(0.5)).Shrink(1).Children(func() {
							lineText(c, i18n.T("onboarding.promise."+p.key), t.Text.Body).FontWeight(500)
							kit.Text(c, i18n.T("onboarding.promise."+p.key+".sub"), t.Text.Base).TextColor(t.Muted).LineHeight(theme.LineProse)
						})
					})
				}
			})
			ui.Spacer(c)
			lineText(c, i18n.T("onboarding.license"), t.Text.Sm).TextColor(t.Muted)
		})
		kit.Panel(c, panelRadius(c)).Grow(1).Shrink(1).Children(func() { // Windows rounds 8, as the main window
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Column(c).FillWidth().MaxWidth(theme.FormMaxWidth).Padding(th.Space(14), th.Space(8)).Gap(th.Space(5.5)).Children(func() { o.body(c) })
				})
			})
		})
	})
}

// sheetView is the add-account dialog; closing it cancels the sign-in.
func (o *onboarding) sheetView(c *ui.Context) {
	opts := kit.DialogOpts{Close: i18n.T("common.close"), Top: true} // each step is its own height
	if o.step == stepPick {                                          // the dialog's header says what the pick step's heading would
		opts.Desc = i18n.T("onboarding.pick.sub")
	}
	kit.Dialog(c, &o.open, i18n.T("onboarding.sheet.title"), opts, func() {
		ui.Column(c).Gap(c.Theme().Space(5.5)).Children(func() { o.body(c) })
	}, nil)
	if !o.open {
		o.a.closeAddAccount()
	}
}

func (o *onboarding) body(c *ui.Context) {
	th := c.Theme()
	switch o.step {
	case stepPick:
		o.pickView(c)
	case stepPassword:
		ui.Column(c).Gap(th.Space(5)).Children(func() { o.passwordView(c) })
	case stepOAuth:
		ui.Column(c).Gap(th.Space(5)).Children(func() { o.oauthView(c) })
	case stepChecking:
		ui.Column(c).Gap(th.Space(5)).Children(func() { o.checkView(c) })
	case stepDone:
		o.doneView(c)
	}
}

func heading(c *ui.Context, title, sub string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		lineText(c, title, t.Text.X2l).FontWeight(600)
		if sub != "" {
			lineText(c, sub, t.Text.Body).TextColor(t.Muted)
		}
	})
}

// lineText is s at size with the design's normal line: taller with CJK in it.
func lineText(c *ui.Context, s string, size float32) ui.Element {
	return kit.Text(c, s, size).LineHeight(theme.LineFor(s))
}

// mark is a provider's mark: its logo (icons.Brand) on a white tile, else
// its letters on a transparent square.
func mark(c *ui.Context, p auth.Provider, size float32) {
	if b := icons.Brand(p.ID); b != nil {
		markTile(c, b, size)
		return
	}
	t := theme.Of(c)
	text := providerMark(p)
	fs := t.Text.Xs
	if size >= theme.MarkLg {
		fs = t.Text.Base
	}
	ui.Box(c).Size(size, size).Center().Children(func() {
		kit.Text(c, text, fs).FontWeight(600).LineHeight(theme.LineTight).TextColor(t.Foreground.Mix(t.Background, 0.25))
	})
}

// markTile is img on a mark's white tile: a provider's logo or an account's
// uploaded icon, which covers the tile.
func markTile(c *ui.Context, img ui.ImageSource, size float32) {
	t := theme.Of(c)
	r := min(t.Radius.Lg, size/4) // a quarter of larger marks
	if size <= theme.MarkSm {
		r = t.Radius.Sm
	}
	if size >= theme.MarkXl {
		r = t.Radius.Xl
	}
	tile := ui.Box(c).Size(size, size).Shrink(0).Radius(r).Clip().Background(t.MarkTile)
	if size <= theme.MarkSm {
		// The logo fills the tile, at a size its levels halve to, sharp, under a one device pixel outline.
		kit.Elevate(c, tile, theme.ShadowXs).Children(func() {
			ui.Image(c, img).Size(size, size).Fit(ui.Cover)
			ui.Box(c).Absolute().Left(0).Top(0).Size(size, size).Radius(r).Border(theme.Hairline, t.Outline)
		})
		return
	}
	tile.Border(theme.Stroke, t.Outline)
	in := size - 2*theme.Stroke // inside the outline: a full-size logo would sit off centre, clipped
	kit.Elevate(c, tile, theme.ShadowXs).Children(func() { ui.Image(c, img).Size(in, in).Fit(ui.Cover) })
}

func inputHeight(c *ui.Context) float32 { return c.Theme().Space(9) }

func primary(c *ui.Context, label string, disabled bool) ui.Element {
	return kit.Button(c, label, kit.ButtonOpts{Size: kit.SizeLg, Disabled: disabled}).FillWidth()
}

func (o *onboarding) emailSuggestions() []string {
	local, domain, _ := strings.Cut(strings.TrimSpace(o.email), "@")
	if !validEmail(local + "@example.com") {
		return nil
	}
	var suggestions []string
	for _, p := range o.providers {
		for _, d := range p.Domains {
			if strings.HasPrefix(d, strings.ToLower(domain)) {
				suggestions = append(suggestions, local+"@"+d)
			}
		}
	}
	return suggestions
}

func (o *onboarding) pickView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	if !o.sheet {
		heading(c, i18n.T("onboarding.pick.title"), i18n.T("onboarding.pick.sub"))
	}
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		box := kit.InputGroup(c, kit.InputOpts{Invalid: o.emailErr != ""}).Height(inputHeight(c)).Gap(th.Space(2)).PaddingX(th.Space(3))
		box.Children(func() {
			in := ui.Autocomplete(c, &o.email, o.emailSuggestions()).Placeholder(i18n.T("onboarding.email.placeholder")).
				Padding(0).Border(0, t.Input).Background(t.Background).FocusRing(false).
				Label(i18n.T("onboarding.email")).Grow(1).MinWidth(0).Height(inputHeight(c) - theme.Stroke - theme.Stroke)
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
	ui.Row(c).Gap(th.Space(3)).TextColor(t.Muted).Children(func() {
		kit.Separator(c, false).Grow(1)
		lineText(c, i18n.T("onboarding.or_pick"), t.Text.Sm)
		kit.Separator(c, false).Grow(1)
	})
	ui.Column(c).Gap(th.Space(2)).Children(func() {
		var ps []auth.Provider
		for _, p := range o.providers {
			if listed(p) {
				ps = append(ps, p)
			}
		}
		for i := 0; i < len(ps); i += 2 {
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Stretch).Children(func() {
				for _, p := range ps[i:min(i+2, len(ps))] {
					how := i18n.T("onboarding.login." + p.Login)
					if p.ID == "other" {
						how = i18n.T("onboarding.login.manual")
					}
					kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { o.choose(p) }}).Label(tileName(p)).
						Grow(1).Basis(0).Height(th.Space(12)).PaddingX(th.Space(3)).Gap(th.Space(2.5)).Children(func() {
						mark(c, p, theme.MarkMd)
						ui.Column(c).Shrink(1).Children(func() {
							lineText(c, tileName(p), t.Text.Body).SingleLine()
							lineText(c, how, t.Text.Sm).TextColor(t.Muted).SingleLine()
						})
					})
				}
			})
		}
	})
}

func (o *onboarding) backLink(c *ui.Context) {
	kit.Button(c, i18n.T("onboarding.change"), kit.ButtonOpts{Variant: kit.Link}).LineHeight(theme.LineFor(i18n.T("onboarding.change"))).AlignSelf(ui.Start).OnClick(o.toPick)
}

func (o *onboarding) providerHeader(c *ui.Context, sub string) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(3)).Children(func() {
		mark(c, o.prov, theme.MarkLg)
		ui.Column(c).Children(func() {
			lineText(c, o.provName(), t.Text.X2l).FontWeight(600)
			if sub != "" {
				lineText(c, sub, t.Text.Body).TextColor(t.Muted)
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

func secretInput(c *ui.Context, secret *string, shown *bool, label string) ui.Element {
	th := c.Theme()
	height := inputHeight(c)
	var in ui.Element
	row := kit.InputGroup(c, kit.InputOpts{}).Height(height).Padding(0, th.Space(0.5), 0, 0)
	row.Children(func() {
		in = kit.Input(c, secret, kit.InputOpts{Password: !*shown, Bare: true}).Label(label).Height(height - theme.Stroke - theme.Stroke).Grow(1).Shrink(1).PaddingX(th.Space(2.5))
		icon, tip := "eye", i18n.T("onboarding.secret.show")
		if *shown {
			icon, tip = "eye-off", i18n.T("onboarding.secret.hide")
		}
		kit.IconButton(c, icon, tip, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(func() { *shown = !*shown })
	})
	return in
}

func secretWhitespaceHint(c *ui.Context, secret *string) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\u200b' || r == '\ufeff' {
			return -1
		}
		return r
	}, *secret)
	if cleaned == *secret {
		return
	}
	kit.Alert(c, kit.Warning, i18n.T("onboarding.secret.whitespace"), func() {
		kit.AlertAction(c, kit.Warning, i18n.T("onboarding.secret.clean"), func() { *secret = cleaned })
	})
}

func (o *onboarding) passwordView(c *ui.Context) {
	o.backLink(c)
	sub := strings.TrimSpace(o.email)
	if o.askEmail {
		sub = ""
	}
	o.providerHeader(c, sub)
	if o.askEmail {
		ui.Field(c, i18n.T("onboarding.email"), func() {
			placeholder := i18n.T("onboarding.email.placeholder")
			if len(o.prov.Domains) > 0 {
				placeholder = "you@" + o.prov.Domains[0]
			}
			in := kit.Input(c, &o.email, kit.InputOpts{Placeholder: placeholder}).Height(inputHeight(c))
			if in.Changed() && feishuOrLark(o.prov) {
				o.scheduleDetect()
			}
			if o.focusEmail {
				in.Focus().SetTextSelection(0, 0)
				o.focusEmail = false
			}
		})
	}
	if ownClientOffered(o.prov) {
		tab := o.tab
		kit.TabsFill(c, &o.tab, o.secretName(), i18n.T("onboarding.tab.browser"))
		if o.tab != tab {
			o.showSecret = false
		}
	}
	if o.ownClient() {
		o.ownClientView(c)
		return
	}
	name, login := o.provName(), o.prov.Login
	hint := tr("onboarding.hint." + o.prov.ID)
	if hint == "" && tr("onboarding.hint."+login) != "" {
		hint = i18n.Tf("onboarding.hint."+login, name)
	}
	if hint != "" {
		steps := tr("onboarding.steps." + o.prov.ID)
		if steps == "" {
			steps = tr("onboarding.steps." + login)
		}
		o.hintBox(c, hint, steps, tr("onboarding.note."+o.prov.ID), i18n.Tf("onboarding.open_help", name), o.prov.HelpURL)
	}
	ui.Field(c, o.secretName(), func() {
		if secretInput(c, &o.secret, &o.showSecret, o.secretName()).Submitted() && o.secret != "" {
			o.connect()
		}
	}).Description(i18n.T("onboarding.secret.help")).LineHeight(theme.LineFor(o.secretName())) // the label's line; the description's is the same
	secretWhitespaceHint(c, &o.secret)
	primary(c, i18n.T("onboarding.connect"), o.secret == "" || !validEmail(o.email)).OnClick(o.connect)
	o.manualView(c)
}

// ownClientOffered: the provider has no OAuth client of Godwit's but signs in
// with the user's own (Gmail, for anyone who cannot or will not use an app password).
func ownClientOffered(p auth.Provider) bool { return p.OAuth != nil && p.OAuth.ClientID == "" }

// ownClient: the browser tab is chosen, for the user's own OAuth client.
func (o *onboarding) ownClient() bool { return ownClientOffered(o.prov) && o.tab == 1 }

// oauth: the sign-in is in the browser, with the provider's client or the user's own.
func (o *onboarding) oauth() bool { return o.prov.Login == "oauth" || o.ownClient() }

// ownClientView asks for the user's own OAuth client in place of the password.
func (o *onboarding) ownClientView(c *ui.Context) {
	pub := publicClient(o.prov)
	o.client.choice(c, pub)
	if o.client.ownChosen(pub) {
		o.hintBox(c, i18n.T("onboarding.own_client.title"), i18n.T("onboarding.own_client.steps"), i18n.T("onboarding.own_client.note"),
			i18n.T("onboarding.own_client.help"), ownClientHelpURL)
	}
	o.client.view(c, o.a, i18n.T("onboarding.secret.help"), pub)
	cl := o.client.client(pub)
	primary(c, i18n.T("onboarding.own_client.signin"), cl.ID == "" || cl.Secret == "").OnClick(o.startOAuth)
}

// ownClientHelpURL is Google's guide to creating an OAuth client ID.
const ownClientHelpURL = "https://developers.google.com/workspace/guides/create-credentials#oauth-client-id"

// hintBox is the sign-in hint: a headline, numbered steps (one per line), an
// optional note under them and a help link.
func (o *onboarding) hintBox(c *ui.Context, hint, steps, note, help, helpURL string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(2.5)).Padding(th.Space(3.5), th.Space(4)).Radius(t.Radius.Xl).Background(t.Sidebar).Border(theme.Hairline, t.Hover).Children(func() {
		lineText(c, hint, t.Text.Body).FontWeight(500)
		ui.Column(c).Gap(th.Space(1)).Padding(0, 0, 0, th.Space(1.5)).Children(func() {
			for i, line := range strings.Split(steps, "\n") {
				ui.Text(c, strconv.Itoa(i+1)+". "+line).TextColor(t.Foreground.Mix(t.Background, 0.25)).LineHeight(theme.LineProse)
			}
		})
		if note != "" {
			kit.Text(c, note, t.Text.Sm).TextColor(t.Muted).LineHeight(theme.LineProse)
		}
		if helpURL != "" {
			kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { o.openURL(helpURL) }}).Label(help).AlignSelf(ui.Start).Children(func() {
				lineText(c, help, t.Text.Base).Underline()
			})
		}
	})
}

var securities = []kit.Option[core.Security]{
	{Value: core.SecurityTLS, Label: "onboarding.security.tls"},
	{Value: core.SecurityStartTLS, Label: "onboarding.security.starttls"},
}

// manualView is the collapsible server form: hosts, ports and TLS or STARTTLS.
func (o *onboarding) manualView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(2.5)).Children(func() {
		label := i18n.T("onboarding.manual")
		// Pulled left by its padding, so the chevron lines up with the fields.
		kit.Collapsible(c, &o.manual, func() { lineText(c, label, t.Text.Base) }, func() { o.serversForm(c) }).AlignSelf(ui.Start).Margin(0, 0, 0, -th.Space(2))
	})
}

// serversForm is manualView's fields.
func (o *onboarding) serversForm(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
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
	sub := tr("onboarding.oauth.sub." + o.prov.ID)
	if sub == "" {
		sub = i18n.T("onboarding.oauth.sub")
	}
	o.providerHeader(c, sub)
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
		lineText(c, title, t.Text.Body).FontWeight(500)
		kit.Text(c, i18n.T("onboarding.oauth.body"), t.Text.Base).TextColor(t.Muted).TextAlign(ui.Center).LineHeight(theme.LineProse)
		u := o.authURL
		kit.Button(c, i18n.T("onboarding.oauth.reopen"), kit.ButtonOpts{Variant: kit.Outline, Disabled: u == ""}).PaddingX(th.Space(3)).FontWeight(400).
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
		if st.Name == "imap" {
			detail = o.foldersText()
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
		switch login := o.prov.Login; {
		case o.oauth():
			reason, next = i18n.T("onboarding.reason.oauth"), i18n.T("onboarding.next.oauth")
		case login == "authorization_code" || login == "app_password":
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

// foldersText is "N 个文件夹" from the check, or "" when LIST found none.
func (o *onboarding) foldersText() string {
	if o.report.Folders == 0 {
		return ""
	}
	return i18n.Tf("settings.account.folders", o.report.Folders)
}

func (o *onboarding) settingsNext() string {
	switch {
	case o.invalid != "":
		return o.invalidNext
	case o.oauth():
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
				dot := ui.Box(c).Size(theme.StepDot, theme.StepDot).Radius(t.Radius.Full).Center().FontSize(t.Text.Sm)
				// the design's text glyphs, taller than the check and x icons; "…" sits on the
				// baseline, so pending is the ellipsis icon, centered
				switch status {
				case 1:
					dot.Background(t.SuccessBg).TextColor(t.SuccessText).Children(func() { ui.Text(c, "✓") })
				case 2:
					dot.Background(t.DangerBg).TextColor(t.DangerText).Children(func() { ui.Text(c, "✕") })
				default:
					dot.Background(t.Hover).TextColor(t.Muted).Children(func() { ui.Icon(c, icons.Get("ellipsis")).Size(theme.IconXs, theme.IconXs) })
				}
				ui.Column(c).Gap(th.Space(0.5)).Grow(1).Shrink(1).Children(func() {
					lineText(c, text, t.Text.Body)
					if detail != "" {
						lineText(c, detail, t.Text.Sm).TextColor(t.Muted)
					}
				})
			})
		}
	})
	if failed {
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			if o.oauth() {
				kit.Button(c, i18n.T("onboarding.relogin"), kit.ButtonOpts{Size: kit.SizeLg}).PaddingX(th.Space(3.5)).OnClick(func() { o.retry(false) })
				return
			}
			kit.Button(c, i18n.Tf("onboarding.retype", o.secretName()), kit.ButtonOpts{Size: kit.SizeLg}).PaddingX(th.Space(3.5)).OnClick(func() { o.retry(false) })
			kit.Button(c, i18n.T("onboarding.manual"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeLg}).PaddingX(th.Space(3.5)).FontWeight(400).OnClick(func() { o.retry(true) })
		})
	}
}

func (o *onboarding) doneView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		badge := ui.Box(c).Size(theme.DoneBadge, theme.DoneBadge).Radius(t.Radius.Full).Background(t.SuccessBg).TextColor(t.SuccessText).Center().FontSize(t.Text.Xl).Children(func() {
			ui.Text(c, "✓")
		})
		if c.Preferences().ReduceMotion {
			o.confetti = nil
		}
		if b := o.confetti; b != nil && badge.Bounds().W > 0 { // from the badge's centre, once it was laid out
			x, y := badge.LocalToWindow(theme.DoneBadge/2, theme.DoneBadge/2)
			ui.Overlay(c, func() { b.build(c, x, y) })
		}
		lineText(c, i18n.T("onboarding.done.title"), t.Text.X2l).FontWeight(600).Margin(th.Space(2), 0, 0, 0)
		line := strings.TrimSpace(o.email)
		if f := o.foldersText(); f != "" {
			line += " · " + f
		}
		lineText(c, line, t.Text.Body).TextColor(t.Muted)
	})
	ui.Field(c, i18n.T("onboarding.name"), func() {
		// Keep the completion form's original height; the 36px fields are for sign-in.
		kit.Input(c, &o.name, kit.InputOpts{Placeholder: i18n.T("onboarding.name.placeholder")}).Height(th.Space(10) + theme.Stroke + theme.Stroke).Disabled(o.adding || o.addedID != 0) // the name saves with the account
	}).Description(i18n.T("onboarding.name.help")).LineHeight(theme.LineFor(i18n.T("onboarding.name")))
	opts := rangeOptions()
	opts[0].Label = i18n.T("onboarding.sync.2000") // （推荐） here only
	ui.Field(c, i18n.T("onboarding.sync"), func() { kit.Select(c, &o.sync, opts).Height(th.Space(10)).Disabled(o.adding) }).
		Description(i18n.T("onboarding.sync.help")).LineHeight(theme.LineFor(i18n.T("onboarding.sync")))
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

package app

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/assets"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// Version is the app's version; cmd/godwit sets it with -ldflags.
var Version = "dev"

const repoURL = "https://github.com/godwit-email/godwit"

var logo = ui.MustParseSVG(assets.Logo)

// settingsPage is the Settings section: the accounts and the app's pages,
// chosen by Route.Settings ("account:<id>", "general", "models",
// "extensions", "privacy", "about").
type settingsPage struct {
	a *App

	// accounts
	forms   map[int64]*accountForm
	usage   map[int64]int64                                                                // attachment cache bytes per account; absent until read
	dialog  string                                                                         // the open dialog: "password", "servers", "remove", "cache"; "" none
	dlgAcc  int64                                                                          // the account the dialog is about
	dlgGen  uint64                                                                         // bumped per dialog opened; an older dialog's result leaves it alone
	open    bool                                                                           // the dialog shows; Escape or a click outside clears it
	busy    bool                                                                           // the dialog's action runs
	dlgErr  error                                                                          // the dialog action's failure
	secret  string                                                                         // the password dialog's field
	servers serverForm                                                                     // the servers dialog's fields
	reauth  map[int64]bool                                                                 // a browser sign-in runs for the account
	update  func(ctx context.Context, id int64, p core.AccountPatch) (core.Account, error) // tests swap it

	// general
	st, shown core.Settings // what the controls are bound to, and what they showed last frame
	noTrans   bool          // the no-translate language list is open

	keychain    error // the keychain probe's result
	keychainSet bool  // the probe finished

	models *modelsView
	ext    *extView
}

func newSettingsPage(a *App) *settingsPage {
	return &settingsPage{a: a, forms: map[int64]*accountForm{}, usage: map[int64]int64{}, reauth: map[int64]bool{},
		update: a.svc.UpdateAccount, models: &modelsView{a: a, fetchModels: checkModels(a.svc), testService: a.svc.TestModelService}, ext: &extView{a: a}}
}

// section is the page shown: an unknown or missing account falls back to
// the first account, then to General.
func (p *settingsPage) section() (sec string, acc *core.Account) {
	a := p.a
	sec = a.route.Settings
	if rest, ok := strings.CutPrefix(sec, "account:"); ok || sec == "" {
		id, _ := strconv.ParseInt(rest, 10, 64)
		for i := range a.accounts {
			if a.accounts[i].ID == id {
				return sec, &a.accounts[i]
			}
		}
		if len(a.accounts) == 0 {
			return "general", nil
		}
		return accountRoute(a.accounts[0].ID), &a.accounts[0]
	}
	return sec, nil
}

func accountRoute(id int64) string { return "account:" + strconv.FormatInt(id, 10) }

func (p *settingsPage) refresh(hints []events.Event) {
	a := p.a
	if hints == nil {
		p.closeDialog()
	}
	switch sec, acc := p.section(); {
	case acc != nil:
		p.readUsage(acc.ID)
	case sec == "models" && hints == nil:
		p.models.load()
	case sec == "extensions" && (hints == nil || slices.ContainsFunc(hints, extChange)):
		p.ext.load()
	case sec == "privacy" && hints == nil:
		var err error
		a.query(func(ctx context.Context) error { err = a.svc.KeychainStatus(ctx); return nil },
			func(error) { p.keychain, p.keychainSet = err, true })
	}
}

func (p *settingsPage) Title() (string, string) {
	sec, acc := p.section()
	if acc != nil {
		return i18n.T("settings.accounts"), acc.Email
	}
	return i18n.T("settings." + sec), ""
}

var appSections = []string{"general", "models", "extensions", "privacy", "about"}

// Sidebar: the accounts, each with its provider's mark and a status dot,
// then Add account; then the app's pages.
func (p *settingsPage) Sidebar(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	cur, _ := p.section()
	group := func(label string) {
		ui.Text(c, label).FontSize(t.Text.Sm).TextColor(t.Muted).Padding(th.Space(2.5), th.Space(2), th.Space(1), th.Space(2))
	}
	item := func(sec string, children func()) {
		b := ui.ButtonBase(c).Height(th.Space(8)).PaddingX(th.Space(2)).Gap(th.Space(2)).Radius(t.Radius.Md)
		on := sec == cur
		switch {
		case on:
			b.Background(t.SidebarSelected).FontWeight(500)
		case b.Hovered():
			b.Background(t.Hover)
		}
		b.Children(children)
		if !on {
			b.OnClick(func() { a.navigate(Route{Page: PageSettings, Settings: sec}) })
		}
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, i18n.T("settings.heading")).FontSize(t.Text.Xl).FontWeight(600).Padding(th.Space(1), th.Space(1.5), th.Space(1.5), th.Space(1.5))
			group(i18n.T("settings.accounts"))
			for _, acc := range a.accounts {
				item(accountRoute(acc.ID), func() {
					mark(c, providerMark(accountProvider(a, acc)), theme.MarkSm)
					ui.Text(c, acc.Email).SingleLine().Grow(1).Shrink(1)
					if acc.Status == "ok" {
						kit.StatusDot(c, kit.OK).Label(i18n.T("settings.dot.ok"))
					} else {
						kit.StatusDot(c, kit.Attention).Label(i18n.T("settings.dot.attention"))
					}
				})
			}
			ui.ButtonBase(c).Height(th.Space(8)).PaddingX(th.Space(2)).Gap(th.Space(2)).Radius(t.Radius.Md).TextColor(t.Muted).Children(func() {
				ui.Icon(c, icons.Get("plus")).Size(theme.IconSm, theme.IconSm)
				ui.Text(c, i18n.T("settings.add_account"))
			}).OnClick(a.openAddAccount)
			group(i18n.T("settings.app"))
			for _, sec := range appSections {
				item(sec, func() { ui.Text(c, i18n.T("settings."+sec)) })
			}
		})
	})
}

func (p *settingsPage) Content(c *ui.Context) {
	th := c.Theme()
	sec, acc := p.section()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).MaxWidth(theme.ContentMaxWidth).Padding(th.Space(6)).Gap(th.Space(5)).Children(func() {
			switch {
			case acc != nil:
				p.accountView(c, *acc)
			case sec == "general":
				p.generalView(c)
			case sec == "privacy":
				p.privacyView(c)
			case sec == "models":
				p.models.view(c)
			case sec == "extensions":
				p.ext.view(c)
			case sec == "about":
				p.aboutView(c)
			default:
				pageHeading(c, i18n.T("settings."+sec), "")
			}
		})
	})
	p.dialogView(c)
}

func pageHeading(c *ui.Context, title, sub string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1)).Children(func() {
		ui.Text(c, title).FontSize(t.Text.X2l).FontWeight(600)
		if sub != "" {
			ui.Text(c, sub).TextColor(t.Muted)
		}
	})
}

// settingsSection is a titled block: the title (and help) in a 200-wide
// column, body beside it.
func settingsSection(c *ui.Context, title, help string, body func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(6)).Padding(th.Space(5), 0).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Width(th.Space(50)).Shrink(0).Gap(th.Space(1)).Children(func() {
			ui.Text(c, title).FontWeight(600)
			if help != "" {
				ui.Text(c, help).TextColor(t.Muted)
			}
		})
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(3)).Children(body)
	})
}

// settingRow is a labelled control: label and help on the left, the
// control on the right.
func settingRow(c *ui.Context, label, help string, control func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(4)).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, label)
			if help != "" {
				ui.Text(c, help).FontSize(t.Text.Sm).TextColor(t.Muted)
			}
		})
		control()
	})
}

func (p *settingsPage) privacyView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	pageHeading(c, i18n.T("settings.privacy"), i18n.T("settings.privacy.sub"))
	for _, k := range []string{"keychain", "local", "ai"} {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			ui.Text(c, i18n.T("onboarding.promise."+k)).FontWeight(500)
			ui.Text(c, i18n.T("onboarding.promise."+k+".sub")).TextColor(t.Muted)
		})
	}
	settingsSection(c, i18n.T("settings.keychain"), "", func() {
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			switch {
			case !p.keychainSet:
				kit.Spinner(c)
				ui.Text(c, i18n.T("settings.keychain.checking")).TextColor(t.Muted)
			case p.keychain != nil:
				kit.StatusDot(c, kit.Error)
				ui.Text(c, humanize(p.keychain))
			default:
				kit.StatusDot(c, kit.OK)
				ui.Text(c, i18n.T("settings.keychain.ok"))
			}
		})
	})
}

func (p *settingsPage) aboutView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	a := p.a
	ui.Column(c).Gap(th.Space(2.5)).AlignItems(ui.Start).Children(func() {
		ui.Icon(c, logo).Size(theme.Logo, theme.Logo).TextColor(t.Foreground)
		ui.Text(c, "Godwit").FontSize(t.Text.X2l).FontWeight(600)
		ui.Text(c, i18n.Tf("settings.about.version", Version)).TextColor(t.Muted)
		ui.Text(c, i18n.T("settings.about.license")).TextColor(t.Muted)
		label := strings.TrimPrefix(repoURL, "https://")
		kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline, Small: true, Icon: "external-link"}).OnClick(func() {
			a.do(func(context.Context) error { return a.plat.OpenURL(repoURL) }, a.toastErr)
		})
	})
}

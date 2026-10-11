package app

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

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
		update: a.svc.UpdateAccount, models: &modelsView{a: a, checkService: a.svc.CheckModelService, testService: a.svc.TestModelService}, ext: &extView{a: a}}
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

// sectionIcons are the app sections' sidebar icons.
var sectionIcons = map[string]string{"general": "settings", "models": "cpu", "extensions": "puzzle", "privacy": "shield", "about": "info"}

// Sidebar: the accounts, each with its provider's mark and a status dot,
// then Add account; then the app's pages.
func (p *settingsPage) Sidebar(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	cur, _ := p.section()
	group := func(label string, top, bottom float32) {
		ui.Text(c, label).FontSize(t.Text.Sm).TextColor(t.Muted).Padding(top, th.Space(2), bottom, th.Space(2))
	}
	item := func(sec string) ui.Element {
		return kit.SidebarItem(c, kit.SidebarItemOpts{Selected: sec == cur, OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: sec}) }})
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, i18n.T("settings.heading")).FontSize(t.Text.Xl).FontWeight(600).Padding(th.Space(1), th.Space(1.5), th.Space(2.5), th.Space(1.5))
			group(i18n.T("settings.accounts"), th.Space(1), th.Space(1.5))
			for _, acc := range a.accounts {
				item(accountRoute(acc.ID)).Children(func() {
					mark(c, accountProvider(a, acc), theme.MarkSm)
					ui.Text(c, acc.Email).SingleLine().Grow(1).Shrink(1)
					if acc.Status == "ok" {
						kit.StatusDot(c, kit.OK).Label(i18n.T("settings.dot.ok"))
					} else {
						kit.StatusDot(c, kit.Attention).Label(i18n.T("settings.dot.attention"))
					}
				})
			}
			kit.SidebarItem(c, kit.SidebarItemOpts{Muted: true, OnClick: a.openAddAccount}).Children(func() {
				ui.Row(c).Width(theme.MarkSm).Justify(ui.Center).Children(func() { // the accounts' marks' column
					ui.Icon(c, icons.Get("plus")).Size(theme.IconSm, theme.IconSm)
				})
				ui.Text(c, i18n.T("settings.add_account"))
			})
			group(i18n.T("settings.app"), th.Space(3), th.Space(1))
			for _, sec := range appSections {
				b := item(sec)
				b.Children(func() {
					kit.SidebarIcon(c, b, sectionIcons[sec], sec == cur)
					ui.Text(c, i18n.T("settings."+sec))
				})
			}
		})
	})
}

func (p *settingsPage) Content(c *ui.Context) {
	th := c.Theme()
	sec, acc := p.section()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(theme.SettingsMaxWidth).AlignSelf(ui.Center).Padding(th.Space(8)).Gap(th.Space(8)).Children(func() {
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
	t := theme.Of(c)
	ui.Column(c).Gap(theme.HeadingGap).Children(func() {
		ui.Text(c, title).FontSize(t.Text.Display).FontWeight(600)
		if sub != "" {
			ui.Text(c, sub).TextColor(t.Muted)
		}
	})
}

// settingsSection is a titled card of rows with an optional footnote under
// it. Rows are builders (mygo cannot move built elements into a parent);
// the hairlines between them are the section's.
func settingsSection(c *ui.Context, title, footnote string, rows ...func()) {
	t := theme.Of(c)
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		ui.Text(c, title).FontSize(t.Text.Base).FontWeight(600)
		ui.Column(c).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).Background(t.Background).Clip().Children(func() {
			for i, row := range rows {
				if i > 0 {
					ui.Box(c).FillWidth().Height(theme.Hairline).Shrink(0).Background(t.Border)
				}
				row()
			}
		})
		if footnote != "" {
			ui.Text(c, footnote).FontSize(t.Text.Sm).TextColor(t.Muted)
		}
	})
}

// settingRow is a labelled control: label (500) and description on the
// left, the control on the right; at least 48 high.
func settingRow(c *ui.Context, label, desc string, control func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, label).FontWeight(500)
			if desc != "" {
				ui.Text(c, desc).FontSize(t.Text.Sm).TextColor(t.Muted)
			}
		})
		control()
	})
}

// settingBlock is a card row of free content (padded like a settingRow).
func settingBlock(c *ui.Context, body func()) {
	th := c.Theme()
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(2), th.Space(4)).Children(body)
}

// settingButton is a button in a settings row: the default size, 12 padding.
func settingButton(c *ui.Context, label string, v kit.Variant, disabled bool) ui.Element {
	return kit.Button(c, label, kit.ButtonOpts{Variant: v, Disabled: disabled}).PaddingX(c.Theme().Space(3))
}

// settingSelect is a select in a settings row.
func settingSelect[T comparable](c *ui.Context, label string, sel *T, opts []kit.Option[T]) ui.Element {
	return kit.SelectWith(c, kit.SelectOpts{Radius: theme.Of(c).Radius.Lg, MinWidth: theme.SettingsSelectMin, MaxWidth: theme.SettingsSelectMax, Flat: true}, sel, opts).Label(label)
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
		settingBlock(c, func() {
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
	})
	p.syncSettings()
	defer p.syncSettings() // what the switch changed while building
	a := p.a
	rows := []func(){func() {
		kit.SwitchRow(c, &p.st.LocalAPI, i18n.T("settings.local_api.on"), i18n.T("settings.local_api.help"))
	}}
	if err := a.localAPIErr; err != nil {
		rows = append(rows, func() {
			settingBlock(c, func() {
				ui.Row(c).Gap(th.Space(2)).Children(func() {
					kit.StatusDot(c, kit.Error)
					ui.Text(c, humanize(err)).Selectable()
				})
			})
		})
	}
	if a.settings.LocalAPI && a.localAPIPath != "" {
		rows = append(rows, func() {
			settingBlock(c, func() {
				ui.Text(c, i18n.Tf("settings.local_api.socket", a.localAPIPath)).Selectable()
				ui.Text(c, i18n.T("settings.local_api.mcp")).FontSize(t.Text.Sm).TextColor(t.Muted)
				config := a.mcpConfig()
				ui.Text(c, config).Font(theme.Mono).FontSize(t.Text.Sm).Selectable()
				if settingButton(c, i18n.T("settings.local_api.copy"), kit.Outline, false).AlignSelf(ui.Start).Clicked() {
					c.WriteClipboard(config)
				}
			})
		})
	}
	settingsSection(c, i18n.T("settings.local_api"), "", rows...)
}

// godwitExe is this executable's absolute path.
var godwitExe = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return "godwit" // found on PATH, if at all
	}
	return exe
})

// mcpConfig is the MCP client entry that runs godwit mcp on this data
// directory.
func (a *App) mcpConfig() string {
	args := []string{"mcp"}
	if dir, custom := a.plat.DataDir(); custom {
		args = append(args, "--data-dir", dir)
	}
	exe, _ := json.Marshal(godwitExe())
	as, _ := json.Marshal(args)
	return `{"command": ` + string(exe) + `, "args": ` + string(as) + `}`
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
		kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Icon: "external-link"}).OnClick(func() {
			a.do(func(context.Context) error { return a.plat.OpenURL(repoURL) }, a.toastErr)
		})
	})
}

package app

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
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

// Version is the app's version; cmd/godwit sets it from the packaged app.
var Version = "dev"

const (
	siteURL = "https://godwit.email"
	repoURL = "https://github.com/godwit-email/godwit"
)

var logo = ui.MustParseSVG(assets.Logo)

// settingsPage is the Settings section: the accounts and the app's pages,
// chosen by Route.Settings ("account:<id>", "general", "signatures",
// "signature:<id>", "models", "labels", "extensions", "privacy", "about").
type settingsPage struct {
	a *App

	// accounts
	accountOrder ui.ListState // the sidebar's accounts, dragged to reorder them
	forms        map[int64]*accountForm
	usage        map[int64]int64    // attachment cache bytes per account; absent until read
	boxes        map[int64]*mailbox // 邮箱行为 per account; absent until read
	dialog       string             // the open dialog: "password", "servers", "remove", "cache"; "" none
	dlgAcc       int64              // the account the dialog is about
	dlgGen       uint64             // bumped per dialog opened; an older dialog's result leaves it alone
	open         bool               // the dialog shows; Escape or a click outside clears it
	busy         bool               // the dialog's action runs
	dlgErr       error              // the dialog action's failure
	secret       string             // the password dialog's field
	showSecret   bool
	iconSelected string                                                                         // the icon dialog's chosen icon: '' or label:<name>
	iconUpload   []byte                                                                         // the icon dialog's uploaded image, over iconSelected
	iconPreview  ui.ImageSource                                                                 // iconUpload decoded
	servers      serverForm                                                                     // the servers dialog's fields
	reauth       map[int64]bool                                                                 // a browser sign-in runs for the account
	update       func(ctx context.Context, id int64, p core.AccountPatch) (core.Account, error) // tests swap it

	// general
	st, shown   core.Settings         // what the controls are bound to, and what they showed last frame
	noTrans     bool                  // the no-translate language list is open
	translators []core.TranslatorInfo // the 翻译方式 choices, read on opening General
	mask        maskDraft             // 隐私脱敏's text fields
	maskLog     []core.MaskRecord     // 脱敏记录, newest first, as last read

	keychain    error // the keychain probe's result
	keychainSet bool  // the probe finished
	imgSize     int64 // the remote image cache's bytes; -1 until read

	// The signature page, made once (NewSignatureView), and the signature
	// it shows; sigPosted is a switch queued.
	sigView   ui.NativeWebView
	sigShow   func(account int64)
	sigFlush  func(done func(ok bool))
	sigClose  func()
	sigErr    error
	sigShown  int64
	sigPosted bool
	sigCode   *sigCode // the shown signature in the HTML mode: a native editor in the page's place

	sigForm   sigForm          // the shown signature's name
	sigDelete bool             // its delete confirmation is open
	sigDocs   map[int64]sigDoc // the account pages' signature previews
	sigLink   *sigLink         // the signature page's link dialog, while it asks

	// The About page's bird, made as the page shows and closed as it goes
	// (setRoute); birdPosted is its making queued.
	birdView   ui.NativeWebView
	birdClose  func()
	birdErr    error
	birdPosted bool
	latest     string // a newer version the update feed offers, read as About opens; "" none
	gh         bool   // the GitHub CLI is signed in: the star button stars, else it opens the repository
	starred    bool   // the user starred Godwit, by gh
	starring   bool   // StarRepo runs

	query string // the sidebar's search

	models *modelsView
	ext    *extView
	labels *labelsView
}

func newSettingsPage(a *App) *settingsPage {
	return &settingsPage{a: a, forms: map[int64]*accountForm{}, usage: map[int64]int64{}, imgSize: -1, boxes: map[int64]*mailbox{}, reauth: map[int64]bool{}, sigDocs: map[int64]sigDoc{},
		update: a.svc.UpdateAccount, models: &modelsView{a: a, checkService: a.svc.CheckModelService, testService: a.svc.TestCapability, testPlugin: a.svc.TestPluginModel, serviceAvatar: a.svc.ModelServiceAvatar}, ext: &extView{a: a, tab: 1}, labels: newLabelsView(a)}
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
		p.commitSignatureName() // another page now
		p.sigDelete = false
		p.closeCode() // the page shows the signature again when it is back
		if l := p.sigLink; l != nil {
			p.sigLink = nil
			l.answer("", false)
		}
	}
	switch sec, acc := p.section(); {
	case acc != nil:
		p.readUsage(acc.ID)
		p.readMailbox(acc.ID)
	case sec == "models" && (hints == nil || slices.ContainsFunc(hints, func(ev events.Event) bool { return extChange(ev) || ev.Type == events.ModelsChanged })): // a plugin's models come and go with it
		p.models.load()
	case sec == "general" && (hints == nil || slices.ContainsFunc(hints, extChange)):
		var list []core.TranslatorInfo
		var registry core.RegistryView
		a.query(func(ctx context.Context) (err error) {
			registry, _ = a.svc.ListPluginRegistry(ctx) // Missing display names must not hide installed translators.
			list, err = a.svc.ListTranslators(ctx)
			return err
		},
			func(err error) {
				if err == nil {
					p.translators = list
					p.ext.reg = registry
				}
			})
		if hints == nil {
			p.readMaskLog()
		}
	case sec == "extensions" && (hints == nil || slices.ContainsFunc(hints, extChange)):
		p.ext.load()
		if hints == nil { // opened: the directory may have changed since
			p.ext.refreshRegistry()
		}
	case sec == "about" && hints == nil:
		var v string
		a.query(func(ctx context.Context) (err error) { v, err = a.plat.LatestVersion(ctx); return err },
			func(err error) {
				if err == nil {
					p.latest = v
				} // a dev build, offline: Check for Updates says why
			})
		var starred, ok bool
		a.query(func(ctx context.Context) error { starred, ok = a.plat.RepoStarred(ctx); return nil },
			func(error) { p.gh, p.starred = ok, starred })
	case sec == "privacy" && hints == nil:
		var err error
		a.query(func(ctx context.Context) error { err = a.svc.KeychainStatus(ctx); return nil },
			func(error) { p.keychain, p.keychainSet = err, true })
		p.readImageCache()
	}
}

func (p *settingsPage) Title() (string, string) {
	sec, acc := p.section()
	if acc != nil {
		return i18n.T("settings.heading"), i18n.T("settings.accounts") + " · " + acc.Email
	}
	if sig, ok := p.shownSignature(sec); ok {
		return i18n.T("settings.heading"), i18n.T("settings.signatures") + " · " + sig.Name
	}
	if strings.HasPrefix(sec, "signature:") {
		sec = "signatures"
	}
	return i18n.T("settings.heading"), i18n.T("settings." + sec)
}

var appSections = []string{"general", "signatures", "models", "labels", "extensions", "privacy", "about"}

// sectionIcons are the app sections' sidebar icons.
var sectionIcons = map[string]string{"general": "settings", "signatures": "quill-pen", "models": "cpu", "labels": "tag", "extensions": "puzzle", "privacy": "shield", "about": "info"}

// Sidebar: the accounts, each with its provider's mark and a status dot,
// then Add account; then the app's pages.
func (p *settingsPage) Sidebar(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	cur, _ := p.section()
	if strings.HasPrefix(cur, "signature:") {
		cur = "signatures" // a signature is a page of the list
	}
	group := func(label string, top, bottom float32) {
		kit.Text(c, label, t.Text.Sm).TextColor(t.Muted).Padding(top, th.Space(2), bottom, th.Space(2))
	}
	item := func(sec string) ui.Element {
		return kit.SidebarItem(c, kit.SidebarItemOpts{Selected: sec == cur, OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: sec}) }})
	}
	sidebarScroll(c, func() {
		kit.Text(c, i18n.T("settings.heading"), t.Text.Xl).LineHeight(theme.LineFor(i18n.T("settings.heading"))).FontWeight(600).Padding(th.Space(1), th.Space(1.5), th.Space(2.5), th.Space(1.5))
		p.searchField(c)
		hit := p.matching(p.query)
		if hit.accounts {
			group(i18n.T("settings.accounts"), th.Space(1), th.Space(1.5))
		}
		account := func(acc core.Account) {
			item(accountRoute(acc.ID)).Children(func() {
				a.accountMark(c, acc)
				ui.Text(c, accountName(acc)).SingleLine().Grow(1).Shrink(1)
			})
		}
		switch n := len(a.accounts); {
		case !hit.accounts:
		case hit.email == "" && n > 1: // dragged to reorder; not while a search hides some
			p.accountOrder.Key = func(i int) any { return a.accounts[i].ID }
			p.accountOrder.Label = func(i int) string { return accountName(a.accounts[i]) }
			p.accountOrder.Reorder = func(rows []int, to int) { p.moveAccount(rows[0], to) }
			gap := th.Space(0.5) // the sidebar column's
			kit.List(c, &p.accountOrder, n, func(i int) {
				var top float32
				if i > 0 {
					top = gap
				}
				ui.Column(c).FillWidth().Padding(top, 0, 0, 0).Children(func() { account(a.accounts[i]) })
			}).FillWidth().Height(float32(n)*(th.Space(8)+gap) - gap)
		default:
			for _, acc := range a.accounts {
				if hit.email == "" || accountMatches(acc, hit.email) {
					account(acc)
				}
			}
		}
		if hit.accounts {
			kit.SidebarItem(c, kit.SidebarItemOpts{Muted: true, OnClick: a.openAddAccount}).Children(func() {
				ui.Row(c).Width(theme.MarkSm).Justify(ui.Center).Children(func() { // the accounts' marks' column
					ui.Icon(c, icons.Get("plus")).Size(theme.IconSm, theme.IconSm)
				})
				ui.Text(c, i18n.T("settings.add_account"))
			})
		}
		if len(hit.apps) > 0 {
			group(i18n.T("settings.app"), th.Space(3), th.Space(1))
		}
		for _, sec := range hit.apps {
			b := item(sec)
			b.Children(func() {
				kit.SidebarIcon(c, b, sectionIcons[sec], sec == cur)
				ui.Text(c, i18n.T("settings."+sec))
			})
		}
	})
}

func (p *settingsPage) Content(c *ui.Context) {
	th := c.Theme()
	sec, acc := p.section()
	kit.Panel(c, panelRadius(c)).Grow(1).Children(func() {
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).FillWidth().Padding(th.Space(8)).Children(func() {
				ui.Column(c).FillWidth().MaxWidth(theme.SettingsMaxWidth).AlignSelf(ui.Center).Gap(th.Space(8)).Children(func() {
					switch {
					case acc != nil:
						p.accountView(c, *acc)
					case sec == "general":
						p.generalView(c)
					case sec == "signatures" || strings.HasPrefix(sec, "signature:"):
						if sig, ok := p.shownSignature(sec); ok {
							p.signatureView(c, sig)
						} else {
							p.signaturesView(c)
						}
					case sec == "privacy":
						p.privacyView(c)
					case sec == "models":
						p.models.view(c)
					case sec == "labels":
						p.labels.view(c, p)
					case sec == "extensions":
						p.ext.view(c)
					case sec == "about":
						p.aboutView(c)
					default:
						pageHeading(c, i18n.T("settings."+sec), "")
					}
				})
			})
		})
	})
	p.dialogView(c)
}

// pageHeading is a page's title and subtitle; action, if any, is a button
// level with the subtitle's bottom on the right.
func pageHeading(c *ui.Context, title, sub string, action ...func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.End).Gap(th.Space(4)).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(theme.HeadingGap).Children(func() {
			kit.Text(c, title, t.Text.Display).FontWeight(600)
			if sub != "" {
				kit.Text(c, sub, t.Text.Body).LineHeight(theme.LineFor(sub)).TextColor(t.Muted)
			}
		})
		for _, f := range action {
			f()
		}
	})
}

// settingsSection is a titled card of rows with an optional footnote under
// it. Rows are builders (mygo cannot move built elements into a parent);
// the hairlines between them are the section's.
func settingsSection(c *ui.Context, title, footnote string, rows ...func()) {
	t := theme.Of(c)
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		sectionTitle(c, title)
		settingsCard(c, rows...)
		if footnote != "" {
			kit.Text(c, footnote, t.Text.Sm).TextColor(t.Muted).PaddingX(c.Theme().Space(4))
		}
	})
}

// sectionTitle is a settings section's title, inset as its card's rows.
func sectionTitle(c *ui.Context, title string) {
	kit.Text(c, title, theme.Of(c).Text.Body).LineHeight(theme.LineFor(title)).FontWeight(600).PaddingX(c.Theme().Space(4))
}

// settingsCard is a section's card of rows, with hairlines between them: a
// Card fill in a hairline border, so gray chips in its rows are drawn on Background.
func settingsCard(c *ui.Context, rows ...func()) {
	t := theme.Of(c)
	ui.Column(c).Radius(t.Radius.Xl).Background(t.Card).Border(theme.Hairline, t.Border).Clip().Children(func() {
		for i, row := range rows {
			if i > 0 {
				hairline(c)
			}
			row()
		}
	})
}

// hairline divides the rows of a settings card, inset to the rows' text as
// macOS's System Settings does.
func hairline(c *ui.Context) {
	kit.Separator(c, false).Shrink(0).MarginX(c.Theme().Space(4))
}

// settingRow is a labelled control: label (500) and description on the
// left, the control on the right; at least 48 high.
func settingRow(c *ui.Context, label, desc string, control func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, label, t.Text.Body).LineHeight(theme.LineFor(label)).FontWeight(500)
			if desc != "" {
				kit.Text(c, desc, t.Text.Sm).TextColor(t.Muted)
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
	}, func() {
		settingRow(c, i18n.T("settings.local_api.send"), i18n.T("settings.local_api.send.help"), func() {
			level := p.st.MCPSend
			if level == "" {
				level = core.MCPSendConfirm
			}
			settingSelect(c, i18n.T("settings.local_api.send"), &level, []kit.Option[string]{
				{Value: core.MCPSendOff, Label: i18n.T("settings.local_api.send.off")},
				{Value: core.MCPSendConfirm, Label: i18n.T("settings.local_api.send.confirm")},
				{Value: core.MCPSendDirect, Label: i18n.T("settings.local_api.send.direct")},
			})
			p.st.MCPSend = level
		})
		if p.st.MCPSend == core.MCPSendDirect {
			settingBlock(c, func() { kit.Alert(c, kit.Warning, i18n.T("settings.local_api.send.risk"), nil) })
		}
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
				kit.Text(c, i18n.T("settings.local_api.mcp"), t.Text.Sm).TextColor(t.Muted)
				config := a.mcpConfig()
				kit.Text(c, config, t.Text.Sm).Font(theme.Mono).Selectable()
				if settingButton(c, i18n.T("settings.local_api.copy"), kit.Outline, false).AlignSelf(ui.Start).Clicked() {
					c.WriteClipboard(config)
				}
			})
		})
	}
	settingsSection(c, i18n.T("settings.local_api"), "", rows...)
	p.remoteImagesView(c)
	p.trustedView(c)
	p.blockedView(c)
}

// readImageCache reads the remote image cache's size for the privacy page.
func (p *settingsPage) readImageCache() {
	a := p.a
	var n int64
	a.query(func(ctx context.Context) error { n = a.svc.ImageCacheSize(); return nil }, func(error) { p.imgSize = n })
}

// remoteImagesView is when remote images load (RemoteImages) and the cache
// of the ones loaded, which can be cleared.
func (p *settingsPage) remoteImagesView(c *ui.Context) {
	t, th, a := theme.Of(c), c.Theme(), p.a
	size := "…"
	if p.imgSize >= 0 {
		size = formatBytes(p.imgSize)
	}
	settingsSection(c, i18n.T("settings.remote_images"), i18n.T("settings.remote_images.sub")+"\n"+i18n.T("settings.remote_images.avatars"),
		func() {
			settingRow(c, i18n.T("settings.remote_images.mode"), i18n.T("settings.remote_images.mode.help"), func() {
				mode := p.st.RemoteImages
				if mode == "" {
					mode = "auto"
				}
				settingSelect(c, i18n.T("settings.remote_images.mode"), &mode, []kit.Option[string]{
					{Value: "auto", Label: i18n.T("settings.remote_images.auto")},
					{Value: "trusted", Label: i18n.T("settings.remote_images.trusted")},
					{Value: "ask", Label: i18n.T("settings.remote_images.ask")},
				})
				p.st.RemoteImages = mode
			})
		},
		func() {
			settingRow(c, i18n.T("settings.remote_images.cache"), i18n.T("settings.remote_images.cache.help"), func() {
				ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, size).TextColor(t.Muted)
					if settingButton(c, i18n.T("settings.remote_images.clear"), kit.Outline, false).Clicked() {
						a.do(func(context.Context) error { return a.svc.ClearImageCache() }, func(err error) {
							if err != nil {
								a.toastErr(err)
							}
							p.readImageCache()
						})
					}
				})
			})
		})
}

// trustedView lists the senders whose remote images load at once, each
// with a remove (始终信任此发件人 adds them).
func (p *settingsPage) trustedView(c *ui.Context) {
	t, a := theme.Of(c), p.a
	var rows []func()
	for _, addr := range a.settings.TrustedImageSenders {
		rows = append(rows, func() {
			settingRow(c, addr, "", func() {
				if settingButton(c, i18n.T("settings.trusted.remove"), kit.Outline, false).Label(i18n.Tf("settings.trusted.remove.label", addr)).Clicked() {
					a.do(func(ctx context.Context) error { return a.svc.UntrustImageSender(ctx, addr) }, a.toastErr)
				}
			}).Label(addr)
		})
	}
	if len(rows) == 0 {
		rows = append(rows, func() {
			settingBlock(c, func() { ui.Text(c, i18n.T("settings.trusted.none")).TextColor(t.Muted) })
		})
	}
	settingsSection(c, i18n.T("settings.trusted"), i18n.T("settings.trusted.sub"), rows...)
}

// blockedView lists the senders the user blocked, each with an unblock button.
func (p *settingsPage) blockedView(c *ui.Context) {
	t, a := theme.Of(c), p.a
	var rows []func()
	for _, addr := range a.settings.BlockedSenders {
		rows = append(rows, func() {
			settingRow(c, addr, "", func() {
				if settingButton(c, i18n.T("settings.blocked.remove"), kit.Outline, false).Label(i18n.Tf("settings.blocked.remove.label", addr)).Clicked() {
					a.setBlocked(addr, false, nil)
				}
			}).Label(addr)
		})
	}
	if len(rows) == 0 {
		rows = append(rows, func() {
			settingBlock(c, func() { ui.Text(c, i18n.T("settings.blocked.none")).TextColor(t.Muted) })
		})
	}
	settingsSection(c, i18n.T("settings.blocked"), i18n.T("settings.blocked.sub"), rows...)
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
	t, th, a := theme.Of(c), c.Theme(), p.a
	p.bird()
	ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(1)).Children(func() {
		ui.Column(c).FillWidth().Height(theme.AboutBird).Center().Children(func() {
			if p.birdView != nil {
				ui.WebView(c, p.birdView).FillWidth().Height(theme.AboutBird) // on the panel's color (NewBirdView)
			} else { // while it is made, or without web views
				ui.Icon(c, logo).Size(theme.Logo, theme.Logo).TextColor(t.Foreground)
			}
		})
		kit.Text(c, "Godwit", t.Text.X2l).FontWeight(600)
		ui.Text(c, i18n.Tf("settings.about.version", Version)).TextColor(t.Muted)
	})
	// value is a row's muted value on the right, a button with icon after it.
	value := func(text, icon string) bool {
		return kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).Label(text).Gap(th.Space(1)).TextColor(t.Muted).Children(func() {
			ui.Text(c, text).Role(ui.RoleNone) // the button's Label names it
			ui.Icon(c, icons.Get(icon)).Size(theme.IconSm, theme.IconSm)
		}).Clicked()
	}
	open := func(url string) {
		a.do(func(context.Context) error { return a.plat.OpenURL(url) }, a.toastErr)
	}
	rows := []func(){func() {
		settingRow(c, i18n.T("settings.about.build"), "", func() {
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Godwit "+Version+" ("+runtime.GOOS+"/"+runtime.GOARCH+")").TextColor(t.Muted).Selectable()
				if !a.plat.CanCheckForUpdates() {
					return
				}
				// Either opens the updater's window: up to date, or the newer version to install.
				label, v := i18n.T("settings.about.updates.check"), kit.Outline
				if p.latest != "" {
					label, v = i18n.Tf("settings.about.upgrade", p.latest), kit.Default
				}
				if settingButton(c, label, v, false).Clicked() {
					a.plat.CheckForUpdates()
				}
			})
		})
	}, func() {
		settingRow(c, i18n.T("settings.about.license"), "", func() {
			if value("Apache-2.0", "arrow-up-right") {
				open(repoURL + "/blob/main/LICENSE")
			}
		})
	}}
	settingsCard(c, rows...)
	links := []func(){func() {
		settingRow(c, i18n.T("settings.about.source"), "", func() {
			if !p.starred {
				p.starButton(c)
			} else if value("GitHub", "arrow-up-right") {
				open(repoURL)
			}
		})
	}}
	for _, l := range []struct{ key, value, url string }{
		{"settings.about.site", "godwit.email", siteURL},
		{"settings.about.changelog", "GitHub Releases", repoURL + "/releases"},
		{"settings.about.feedback", "GitHub Issues", repoURL + "/issues"},
	} {
		links = append(links, func() {
			settingRow(c, i18n.T(l.key), "", func() {
				if value(l.value, "arrow-up-right") {
					open(l.url)
				}
			})
		})
	}
	settingsSection(c, i18n.T("settings.about.links"), "", links...)
}

// starButton stars Godwit's repository with the GitHub CLI; without gh it
// opens the repository to star there.
func (p *settingsPage) starButton(c *ui.Context) {
	t, th, a := theme.Of(c), c.Theme(), p.a
	label := i18n.T("settings.about.star")
	b := kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Label(label).Gap(th.Space(1.5)).Children(func() {
		ui.Icon(c, icons.Get("star")).Size(theme.IconSm, theme.IconSm).TextColor(t.Foreground)
		ui.Text(c, label).Role(ui.RoleNone) // the button's Label names it
		if !p.gh {
			ui.Icon(c, icons.Get("arrow-up-right")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		}
	})
	if !b.Clicked() || p.starring {
		return
	}
	if !p.gh {
		a.do(func(context.Context) error { return a.plat.OpenURL(repoURL) }, a.toastErr)
		return
	}
	p.starring = true
	a.do(a.plat.StarRepo, func(err error) {
		p.starring = false
		if err != nil {
			a.toastErr(err)
			return
		}
		p.starred = true
	})
}

// bird makes the About page's bird, once, after this frame (making a web
// view may run the event loop).
func (p *settingsPage) bird() {
	if p.birdView != nil || p.birdPosted || p.birdErr != nil {
		return
	}
	p.birdPosted = true
	p.a.onUI(func() {
		p.birdPosted = false
		if p.a.route.Page != PageSettings || p.a.route.Settings != "about" {
			return // gone before it was made
		}
		if p.birdView, p.birdClose, p.birdErr = p.a.plat.NewBirdView(); p.birdErr != nil {
			p.a.logf("the About page's bird: %v", p.birdErr)
		}
	})
}

// closeBird closes the About page's bird, if made.
func (p *settingsPage) closeBird() {
	if p.birdClose != nil {
		p.birdClose()
	}
	p.birdView, p.birdClose = nil, nil
}

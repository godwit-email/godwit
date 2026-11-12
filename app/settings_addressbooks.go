package app

import (
	"cmp"
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// addressBooksView is the read-only CardDAV address books in Settings'
// accounts (ADR 0038): a mail account page's 通讯录 section, and the page of
// an address book with no mail account (addressbook:<id>).
type addressBooksView struct {
	a *App

	syncing map[int64]bool
	reauth  map[int64]bool
	linking map[int64]bool // by mail account: its address book is being added

	renameID int64 // the address book the name field is for
	rename   string

	dialog     string // "add", "password", "remove"; "" none
	open       bool
	busy       bool
	dlgErr     error
	dlgID      int64
	dlgGen     uint64
	preset     string
	name       string
	serverURL  string
	username   string
	password   string
	showSecret bool
	client     clientForm
	flowID     string
	authURL    string
	oauthWait  bool

	add        func(context.Context, core.AddressBookInput) (core.AddressBook, error)
	updateAuth func(ctx context.Context, id int64, password, oauthFlowID string, client core.OAuthClient) (core.AddressBook, error)
}

func newAddressBooksView(a *App) *addressBooksView {
	return &addressBooksView{
		a:          a,
		syncing:    map[int64]bool{},
		reauth:     map[int64]bool{},
		linking:    map[int64]bool{},
		add:        a.svc.AddAddressBook,
		updateAuth: a.svc.UpdateAddressBookAuth,
	}
}

func addressBookRoute(id int64) string { return "addressbook:" + strconv.FormatInt(id, 10) }

// linkedBook is the address book of mail account id, if it has one.
func (a *App) linkedBook(id int64) (core.AddressBook, bool) {
	i := slices.IndexFunc(a.addressBooks, func(ab core.AddressBook) bool { return ab.Account == id })
	if i < 0 {
		return core.AddressBook{}, false
	}
	return a.addressBooks[i], true
}

// standaloneBook is the address book id with no mail account, if there is one.
func (a *App) standaloneBook(id int64) (core.AddressBook, bool) {
	i := slices.IndexFunc(a.addressBooks, func(ab core.AddressBook) bool { return ab.ID == id && ab.Account == 0 })
	if i < 0 {
		return core.AddressBook{}, false
	}
	return a.addressBooks[i], true
}

// abTrouble says whether an address book needs the user (action) or failed
// its last sync and tries again (retrying), for its row's badge.
func abTrouble(ab core.AddressBook) (action, retrying bool) {
	return ab.Status == "reauth_required", ab.Status != "" && ab.Status != "ok" && ab.Status != "reauth_required"
}

// entryRow is a row of Settings' accounts list: the mark with its status
// badge, the name, and what it syncs under it.
func (p *settingsPage) entryRow(c *ui.Context, e accountEntry, selected bool, route string) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	name, kind := "", ""
	var mark func()
	var action, retrying bool
	if acc := e.acc; acc != nil {
		name, kind = accountName(*acc), i18n.T("settings.accounts.kind.mail")
		if isArchive(*acc) {
			kind = i18n.T("mail.archive.kind")
		}
		level := a.syncHealth(*acc).level
		action, retrying = level == syncAction, level == syncRetrying
		if ab, ok := a.linkedBook(acc.ID); ok {
			kind += " · " + i18n.T("settings.addressbooks")
			abAction, abRetrying := abTrouble(ab)
			action, retrying = action || abAction, retrying || abRetrying
		}
		mark = func() { a.accountIcon(c, *acc, theme.MarkSm) }
	} else {
		name, kind = e.ab.Name, abKind(*e.ab)
		action, retrying = abTrouble(*e.ab)
		mark = func() { presetMark(c, e.ab.Preset, theme.MarkSm) }
	}
	kit.SidebarItem(c, kit.SidebarItemOpts{Selected: selected, OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: route}) }}).
		Height(th.Space(11)).Children(func() {
		badgedMark(c, mark, action, retrying)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, name).SingleLine()
			kit.Text(c, kind, t.Text.Xs).TextColor(t.Muted).SingleLine()
		})
	})
}

func (v *addressBooksView) close() {
	v.dlgGen++
	if v.flowID != "" {
		id, oauth := v.flowID, v.a.oauth
		v.flowID, v.authURL = "", ""
		v.a.do(func(context.Context) error { oauth.Cancel(id); return nil }, nil)
	}
	v.dialog, v.open, v.busy, v.oauthWait, v.dlgErr = "", false, false, false, nil
	v.password, v.showSecret = "", false
}

// openAdd opens the add dialog for an address book with no mail account, on
// preset, the username and password filled in.
func (v *addressBooksView) openAdd(preset, username, password string) {
	v.close()
	pub := auth.GoogleCardDAVOAuth().Public
	v.dialog, v.open = "add", true
	v.preset = preset
	v.name, v.serverURL, v.username, v.password = "", "", username, password
	v.client = clientForm{manual: true, public: pub != nil}
}

func (v *addressBooksView) openPassword(ab core.AddressBook) {
	v.close()
	v.dialog, v.open, v.dlgID = "password", true, ab.ID
}

func (v *addressBooksView) openRemove(ab core.AddressBook) {
	v.close()
	v.dialog, v.open, v.dlgID = "remove", true, ab.ID
}

// accountSection is a mail account page's 通讯录: a switch that links the
// address book its credentials reach, then its collections and sync.
func (v *addressBooksView) accountSection(c *ui.Context, acc core.Account) {
	preset := core.LinkedAddressBookPreset(acc)
	if preset == "" || isArchive(acc) {
		return
	}
	a := v.a
	ab, linked := a.linkedBook(acc.ID)
	brand := presetPlaceholderName(preset)
	desc := i18n.Tf("settings.addressbooks.link.help", brand)
	if linked && len(ab.Collections) == 1 {
		desc = i18n.Plural("settings.addressbooks.cards", ab.Collections[0].CardCount) + " · " + desc
	}
	rows := []func(){func() {
		on := linked || v.linking[acc.ID]
		if kit.SwitchRow(c, &on, i18n.Tf("settings.addressbooks.link", brand), desc).Disabled(v.linking[acc.ID]).Changed() {
			if on {
				v.link(acc)
			} else {
				v.unlink(ab)
			}
		}
	}}
	if linked {
		rows = append(rows, v.bookRows(c, ab)...)
	}
	settingsSection(c, i18n.T("settings.addressbooks"), i18n.T("settings.addressbooks.foot"), rows...)
}

// bookRows are an address book's rows: its reauth, its collections when the
// server has more than one, and its sync. With one collection there is no
// list: its count goes on the switch row (a linked one's) or the sync row.
func (v *addressBooksView) bookRows(c *ui.Context, ab core.AddressBook) []func() {
	var rows []func()
	if ab.Status == "reauth_required" {
		rows = append(rows, func() {
			settingRow(c, i18n.T("settings.addressbooks.status.reauth"), abDetailText(ab), func() { v.authAction(c, ab) })
		})
	}
	syncDesc := v.syncText(ab)
	// A sole collection the user turned off keeps its switch: nothing else turns it back on.
	list := len(ab.Collections) > 1 || len(ab.Collections) == 1 && !ab.Collections[0].Enabled
	if ab.Account == 0 && len(ab.Collections) == 1 && !list {
		syncDesc = i18n.Plural("settings.addressbooks.cards", ab.Collections[0].CardCount) + " · " + syncDesc
	}
	if list {
		for _, col := range ab.Collections {
			rows = append(rows, func() {
				on := col.Enabled
				if kit.SwitchRow(c.Key(col.ID), &on, col.Name, i18n.Plural("settings.addressbooks.cards", col.CardCount)).
					Label(i18n.Tf("settings.addressbooks.collection.toggle", col.Name)).Changed() {
					v.toggleCollection(ab.ID, col.ID, on)
				}
			})
		}
	}
	return append(rows, func() {
		settingRow(c, i18n.T("settings.addressbooks.sync"), syncDesc, func() {
			settingButton(c, i18n.T("settings.account.sync_now"), kit.Outline, v.syncing[ab.ID]).
				Label(i18n.Tf("settings.addressbooks.sync.aria", ab.Name)).OnClick(func() { v.syncNow(ab.ID) })
		})
	})
}

// syncText is the sync row's description: the last sync, or what went wrong.
func (v *addressBooksView) syncText(ab core.AddressBook) string {
	switch {
	case v.syncing[ab.ID]:
		return i18n.T("shell.sync.syncing")
	case ab.Status == "error":
		return abDetailText(ab)
	case !ab.LastSyncedAt.IsZero():
		return i18n.Tf("settings.addressbooks.sync.last", v.a.ago(ab.LastSyncedAt))
	}
	return i18n.T("settings.addressbooks.sync.every")
}

// page is an address book with no mail account: its name, collections,
// sign-in and removal, as an account's page.
func (v *addressBooksView) page(c *ui.Context, ab core.AddressBook) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	if v.renameID != ab.ID {
		v.renameID, v.rename = ab.ID, ab.Name
	}
	ui.Row(c).Gap(th.Space(4)).Children(func() {
		presetMark(c, ab.Preset, theme.MarkXl)
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
			kit.Text(c, ab.Name, t.Text.Display).FontWeight(600).SingleLine()
			ui.Text(c, abKind(ab)).TextColor(t.Muted)
		})
	})
	settingsSection(c, i18n.T("settings.account.basic"), "", func() {
		settingRow(c, i18n.T("settings.account.alias"), "", func() {
			if kit.Input(c, &v.rename, kit.InputOpts{}).Width(theme.SettingsInputWidth).Radius(t.Radius.Lg).Label(i18n.T("settings.account.alias")).Changed() {
				if name, id := strings.TrimSpace(v.rename), ab.ID; name != "" {
					a.writes.Chain("addressbooks", func(ctx context.Context) error { return a.svc.RenameAddressBook(ctx, id, name) }, func(err error) {
						if err != nil {
							a.toastErr(err)
						}
						a.load()
					})
				}
			}
		})
	})
	foot := ""
	if len(ab.Collections) > 1 {
		foot = i18n.T("settings.addressbooks.collections.foot")
	}
	settingsSection(c, i18n.T("settings.addressbooks"), foot, v.bookRows(c, ab)...)
	settingsSection(c, i18n.T("settings.account.login"), "",
		func() {
			settingRow(c, i18n.T("settings.account.login.method"), abAuthText(ab), func() { v.authAction(c, ab) })
		},
		func() {
			settingRow(c, "CardDAV", "", func() { kit.Text(c, ab.URL, t.Text.Sm).Font(theme.Mono).TextColor(t.Muted).Shrink(1) })
		})
	settingsSection(c, i18n.T("settings.account.remove"), "", func() {
		settingRow(c, i18n.T("settings.addressbooks.remove.row"), strings.TrimRight(i18n.T("settings.addressbooks.remove.desc"), "。."), func() {
			settingButton(c, i18n.T("settings.account.remove.button"), kit.DestructiveOutline, false).
				Label(i18n.Tf("settings.addressbooks.remove.aria", ab.Name)).OnClick(func() { v.openRemove(ab) })
		})
	})
}

// abHost is the host of the address book's server.
func abHost(ab core.AddressBook) string {
	if u, err := url.Parse(ab.URL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return ab.URL
}

// abKind is an address book's second line in the sidebar and under its page's title.
func abKind(ab core.AddressBook) string {
	return strings.Join(slices.DeleteFunc([]string{i18n.T("settings.addressbooks"), abHost(ab)}, func(s string) bool { return s == "" }), " · ")
}

// abAuthText says how the address book signs in.
func abAuthText(ab core.AddressBook) string {
	if ab.AuthType == "oauth" {
		return i18n.T("settings.account.login.oauth")
	}
	return i18n.Tf("settings.account.login.secret", presetSecretName(ab.Preset))
}

func (v *addressBooksView) authAction(c *ui.Context, ab core.AddressBook) {
	aria := i18n.Tf("settings.addressbooks.auth.aria", ab.Name)
	variant := kit.Outline
	if ab.Status == "reauth_required" {
		variant = kit.Default
	}
	if ab.AuthType == "oauth" {
		label, disabled := i18n.T("onboarding.relogin"), false
		if v.reauth[ab.ID] {
			label, disabled = i18n.T("settings.account.reauth.waiting"), true
		}
		kit.Button(c, label, kit.ButtonOpts{Variant: variant, Size: kit.SizeSm, Disabled: disabled}).
			Label(aria).OnClick(func() { v.reauthorize(ab) })
		return
	}
	label := i18n.Tf("settings.account.login.update", presetSecretName(ab.Preset))
	kit.Button(c, label, kit.ButtonOpts{Variant: variant, Size: kit.SizeSm}).
		Label(aria).OnClick(func() { v.openPassword(ab) })
}

// link adds the address book acc's credentials reach (the switch turned on).
func (v *addressBooksView) link(acc core.Account) {
	a := v.a
	id, preset, email := acc.ID, core.LinkedAddressBookPreset(acc), acc.Email
	v.linking[id] = true
	a.do(func(ctx context.Context) error { return a.linkAddressBook(ctx, id, preset, email) }, func(err error) {
		delete(v.linking, id)
		if preset == core.PresetGoogle && !errors.Is(err, context.Canceled) {
			a.plat.ShowWindow()
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			a.toastErr(err)
		}
		a.load()
	})
}

// linkAddressBook adds mail account id's address book: Google's after a
// sign-in in the browser for its contacts, the others with the account's password.
func (a *App) linkAddressBook(ctx context.Context, id int64, preset, email string) error {
	in := core.AddressBookInput{Account: id, Preset: preset}
	if preset == core.PresetGoogle {
		oauth, plat, pub := a.oauth, a.plat, auth.GoogleCardDAVOAuth().Public
		var authURL string
		var err error
		in.OAuthFlowID, authURL, err = oauth.Begin(ctx, core.ProviderGoogleCardDAV, email, in.Client)
		if errors.Is(err, auth.ErrOAuthNotConfigured) && pub != nil {
			in.Client = core.OAuthClient{ID: pub.ID, Secret: pub.Secret}
			in.OAuthFlowID, authURL, err = oauth.Begin(ctx, core.ProviderGoogleCardDAV, email, in.Client)
		}
		if err != nil {
			return err
		}
		if err := plat.OpenURL(authURL); err != nil {
			oauth.Cancel(in.OAuthFlowID)
			return err
		}
		if _, err := oauth.Await(ctx, in.OAuthFlowID); err != nil {
			return err
		}
	}
	_, err := a.svc.AddAddressBook(ctx, in)
	return err
}

// unlink removes a mail account's address book (the switch turned off): its
// cache goes; turning it on again syncs it anew.
func (v *addressBooksView) unlink(ab core.AddressBook) {
	a := v.a
	a.writes.Chain("addressbooks", func(ctx context.Context) error { return a.svc.RemoveAddressBook(ctx, ab.ID) }, func(err error) {
		if err != nil {
			a.toastErr(err)
		}
		a.load()
	})
}

func (v *addressBooksView) syncNow(id int64) {
	if v.syncing[id] {
		return
	}
	a := v.a
	v.syncing[id] = true
	a.do(func(ctx context.Context) error {
		return a.svc.SyncAddressBook(ctx, id)
	}, func(err error) {
		delete(v.syncing, id)
		if err != nil {
			a.toastErr(err)
		}
		a.load()
	})
}

func (v *addressBooksView) toggleCollection(abID, colID int64, on bool) {
	a := v.a
	// Apply optimistically so the switch responds on the same frame.
	books := slices.Clone(a.addressBooks)
	for i := range books {
		if books[i].ID != abID {
			continue
		}
		cols := slices.Clone(books[i].Collections)
		for j := range cols {
			if cols[j].ID == colID {
				cols[j].Enabled = on
				if !on {
					cols[j].CardCount = 0
				}
			}
		}
		books[i].Collections = cols
	}
	a.addressBooks = books
	a.writes.Chain("addressbooks", func(ctx context.Context) error {
		return a.svc.SetAddressBookCollectionEnabled(ctx, colID, on)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		}
		a.load()
	})
}

func (v *addressBooksView) reauthorize(ab core.AddressBook) {
	if v.reauth[ab.ID] {
		return
	}
	a := v.a
	id, oauth, plat, updateAuth := ab.ID, a.oauth, a.plat, v.updateAuth
	pub := auth.GoogleCardDAVOAuth().Public
	v.reauth[id] = true
	var flow string
	var usedClient core.OAuthClient
	a.do(func(ctx context.Context) error {
		var authURL string
		var err error
		usedClient = core.OAuthClient{AddressBook: id}
		flow, authURL, err = oauth.Begin(ctx, core.ProviderGoogleCardDAV, ab.Username, usedClient)
		if errors.Is(err, auth.ErrOAuthNotConfigured) && pub != nil {
			usedClient = core.OAuthClient{ID: pub.ID, Secret: pub.Secret}
			flow, authURL, err = oauth.Begin(ctx, core.ProviderGoogleCardDAV, ab.Username, usedClient)
		}
		if err != nil {
			return err
		}
		if err := plat.OpenURL(authURL); err != nil {
			oauth.Cancel(flow)
			return err
		}
		_, err = oauth.Await(ctx, flow)
		return err
	}, func(err error) {
		if !errors.Is(err, context.Canceled) {
			a.plat.ShowWindow()
		}
		if err != nil {
			delete(v.reauth, id)
			a.toastErr(err)
			return
		}
		var passClient core.OAuthClient
		if usedClient.AddressBook == 0 {
			passClient = usedClient
		}
		a.writes.Chain("addressbooks", func(ctx context.Context) error {
			_, err := updateAuth(ctx, id, "", flow, passClient)
			return err
		}, func(err error) {
			delete(v.reauth, id)
			if err != nil {
				a.toastErr(err)
			} else {
				a.toastAs(kit.ToastSuccess, i18n.T("settings.account.reauth.done"))
			}
			a.load()
		})
	})
}

func (v *addressBooksView) byID(id int64) (core.AddressBook, bool) {
	for _, ab := range v.a.addressBooks {
		if ab.ID == id {
			return ab, true
		}
	}
	return core.AddressBook{}, false
}

func (v *addressBooksView) dialogView(c *ui.Context) {
	if v.dialog == "" {
		return
	}
	if !v.open {
		v.close()
		return
	}
	a := v.a
	switch v.dialog {
	case "add":
		v.addDialogView(c)
	case "password":
		ab, ok := v.byID(v.dlgID)
		if !ok {
			v.close()
			return
		}
		secretLabel := presetSecretName(ab.Preset)
		title := i18n.Tf("settings.account.login.update", secretLabel)
		body := func() {
			ui.Field(c, secretLabel, func() { secretInput(c, &v.password, &v.showSecret) }).
				Description(i18n.T("onboarding.secret.help"))
			secretWhitespaceHint(c, &v.password)
			if v.dlgErr != nil {
				kit.Alert(c, kit.Danger, humanize(v.dlgErr), nil)
			}
		}
		pw, id, updateAuth, gen := v.password, ab.ID, v.updateAuth, v.dlgGen
		kit.Dialog(c, &v.open, title, kit.DialogOpts{Size: kit.DialogSm, Close: i18n.T("common.close")}, body, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(v.close)
			kit.Button(c, i18n.T("common.save"), kit.ButtonOpts{Disabled: v.busy || pw == ""}).OnClick(func() {
				v.busy, v.dlgErr = true, nil
				a.writes.Chain("addressbooks", func(ctx context.Context) error {
					_, err := updateAuth(ctx, id, pw, "", core.OAuthClient{})
					return err
				}, func(err error) {
					if gen == v.dlgGen && v.dialog == "password" {
						v.busy, v.dlgErr = false, err
						if err == nil {
							v.close()
							a.toastAs(kit.ToastSuccess, i18n.Tf("settings.account.password.updated", secretLabel))
						}
					}
					a.load()
				})
			})
		})
	case "remove":
		ab, ok := v.byID(v.dlgID)
		if !ok {
			v.close()
			return
		}
		id, gen := ab.ID, v.dlgGen
		kit.Dialog(c, &v.open, i18n.Tf("settings.addressbooks.remove.title", ab.Name),
			kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("settings.addressbooks.remove.desc"), Close: i18n.T("common.close")},
			func() {
				if v.dlgErr != nil {
					kit.Alert(c, kit.Danger, humanize(v.dlgErr), nil)
				}
			}, func() {
				kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(v.close)
				kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive, Disabled: v.busy}).OnClick(func() {
					v.busy, v.dlgErr = true, nil
					a.writes.Chain("addressbooks", func(ctx context.Context) error {
						return a.svc.RemoveAddressBook(ctx, id)
					}, func(err error) {
						if gen == v.dlgGen && v.dialog == "remove" {
							v.busy, v.dlgErr = false, err
							if err == nil {
								v.close()
								if a.route.Page == PageSettings && a.route.Settings == addressBookRoute(id) {
									a.navigate(Route{Page: PageSettings}) // the first account's page
								}
							}
						}
						a.load()
					})
				})
			})
	}
}

func (v *addressBooksView) addDialogView(c *ui.Context) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	presets := a.svc.ListAddressBookPresets()
	pub := auth.GoogleCardDAVOAuth().Public
	var curPreset core.AddressBookPreset
	for _, pr := range presets {
		if pr.ID == v.preset {
			curPreset = pr
		}
	}

	choosePreset := func(id string) {
		if v.preset == id {
			return
		}
		if v.flowID != "" {
			fid, oauth := v.flowID, a.oauth
			v.flowID, v.authURL, v.oauthWait = "", "", false
			a.do(func(context.Context) error { oauth.Cancel(fid); return nil }, nil)
		}
		v.preset = id
		v.serverURL = ""
		v.dlgErr = nil
		v.showSecret = false
	}

	body := func() {
		for i := 0; i < len(presets); i += 2 {
			ui.Row(c.Key(i)).Gap(th.Space(2)).AlignItems(ui.Stretch).Children(func() {
				for _, pr := range presets[i:min(i+2, len(presets))] {
					label := presetTitle(pr)
					on := v.preset == pr.ID
					tile := kit.Pressable(c.Key(pr.ID), kit.PressableOpts{
						Selected: on, Bordered: true, OnClick: func() { choosePreset(pr.ID) },
					}).Label(label).Checked(on).Grow(1).Basis(0).Height(th.Space(11)).PaddingX(th.Space(3)).Gap(th.Space(2.5)).AlignItems(ui.Center)
					if on {
						tile.Border(theme.Stroke, t.Foreground)
					}
					tile.Children(func() {
						presetMark(c, pr.ID, theme.MarkSm)
						kit.Text(c, label, t.Text.Base).FontWeight(500).SingleLine().Shrink(1)
					})
				}
			})
		}

		if v.preset == core.PresetGoogle {
			ui.Field(c, i18n.T("settings.addressbooks.name.optional"), func() {
				kit.Input(c, &v.name, kit.InputOpts{Placeholder: "Google"})
			})
			v.client.choice(c, pub)
			v.client.view(c, a, i18n.T("settings.account.client.secret_help"), pub)
			if v.oauthWait {
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					kit.Spinner(c)
					kit.Text(c, i18n.T("onboarding.oauth.opened"), t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
					if v.authURL != "" {
						u := v.authURL
						kit.Button(c, i18n.T("onboarding.oauth.reopen"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeSm}).
							OnClick(func() { a.do(func(context.Context) error { return a.plat.OpenURL(u) }, a.toastErr) })
					}
				})
			}
		} else {
			if hint := presetHint(v.preset); hint != "" {
				ui.Column(c).Gap(th.Space(1)).Padding(th.Space(3)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
					kit.Text(c, hint, t.Text.Sm).TextColor(t.Muted)
					if curPreset.HelpURL != "" {
						u := curPreset.HelpURL
						kit.Button(c, i18n.T("settings.addressbooks.help.link"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeSm}).
							AlignSelf(ui.Start).OnClick(func() { a.do(func(context.Context) error { return a.plat.OpenURL(u) }, a.toastErr) })
					}
				})
			}
			if v.preset == core.PresetCustom {
				ui.Field(c, i18n.T("settings.addressbooks.server"), func() {
					kit.Input(c, &v.serverURL, kit.InputOpts{Placeholder: "dav.example.com", Mono: true})
				}).Description(i18n.T("settings.addressbooks.server.help"))
			}
			userPlaceholder := "alice@example.com"
			switch v.preset {
			case core.PresetICloud:
				userPlaceholder = "you@icloud.com"
			case core.PresetFastmail:
				userPlaceholder = "you@fastmail.com"
			}
			ui.Field(c, i18n.T("onboarding.username"), func() {
				kit.Input(c, &v.username, kit.InputOpts{Placeholder: userPlaceholder})
			})
			secLabel := presetSecretName(v.preset)
			ui.Field(c, secLabel, func() { secretInput(c, &v.password, &v.showSecret) })
			secretWhitespaceHint(c, &v.password)
			ui.Field(c, i18n.T("settings.addressbooks.name.optional"), func() {
				kit.Input(c, &v.name, kit.InputOpts{Placeholder: presetPlaceholderName(v.preset)})
			})
		}

		if v.dlgErr != nil {
			kit.Alert(c, kit.Danger, humanize(v.dlgErr), nil)
		}
	}

	footer := func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(v.close)
		if v.preset == core.PresetGoogle {
			client := v.client.client(pub)
			incomplete := client.ID == "" || client.Secret == ""
			kit.Button(c, i18n.T("onboarding.own_client.signin"), kit.ButtonOpts{
				Disabled: v.busy || v.oauthWait || incomplete,
			}).OnClick(func() { v.submitGoogleAdd(client) })
			return
		}
		incomplete := strings.TrimSpace(v.username) == "" || v.password == "" ||
			(v.preset == core.PresetCustom && strings.TrimSpace(v.serverURL) == "")
		kit.Button(c, i18n.T("onboarding.connect"), kit.ButtonOpts{
			Disabled: v.busy || incomplete,
		}).OnClick(v.submitPasswordAdd)
	}

	kit.Dialog(c, &v.open, i18n.T("settings.addressbooks.add"),
		kit.DialogOpts{Size: kit.DialogMd, Top: true, Desc: i18n.T("settings.addressbooks.dialog.sub"), Close: i18n.T("common.close")},
		body, footer)
}

// added closes the add dialog onto the new address book's page.
func (v *addressBooksView) added(gen uint64, ab core.AddressBook, err error) {
	a := v.a
	if gen == v.dlgGen && v.dialog == "add" {
		v.busy, v.dlgErr = false, err
		if err == nil {
			v.close()
			a.navigate(Route{Page: PageSettings, Settings: addressBookRoute(ab.ID)})
		}
	}
	a.load()
}

func (v *addressBooksView) submitPasswordAdd() {
	if v.busy {
		return
	}
	a := v.a
	v.busy, v.dlgErr = true, nil
	gen, add := v.dlgGen, v.add
	rawURL := ""
	if v.preset == core.PresetCustom {
		rawURL = strings.TrimSpace(v.serverURL)
	}
	in := core.AddressBookInput{
		Preset:   v.preset,
		Name:     strings.TrimSpace(v.name),
		URL:      rawURL,
		Username: strings.TrimSpace(v.username),
		Password: v.password,
	}
	var ab core.AddressBook
	a.writes.Chain("addressbooks", func(ctx context.Context) (err error) {
		ab, err = add(ctx, in)
		return err
	}, func(err error) { v.added(gen, ab, err) })
}

func (v *addressBooksView) submitGoogleAdd(client core.OAuthClient) {
	if v.busy || v.oauthWait {
		return
	}
	a := v.a
	oauth, plat, add := a.oauth, a.plat, v.add
	name := strings.TrimSpace(v.name)
	v.dlgGen++
	gen := v.dlgGen
	v.oauthWait, v.dlgErr = true, nil
	var flow, email string
	a.do(func(ctx context.Context) error {
		fid, authURL, err := oauth.Begin(ctx, core.ProviderGoogleCardDAV, "", client)
		if err != nil {
			return err
		}
		a.onUI(func() {
			if gen == v.dlgGen {
				v.flowID, v.authURL = fid, authURL
			}
		})
		if err := plat.OpenURL(authURL); err != nil {
			oauth.Cancel(fid)
			return err
		}
		flow = fid
		email, err = oauth.Await(ctx, fid)
		return err
	}, func(err error) {
		if gen != v.dlgGen || v.dialog != "add" {
			return
		}
		v.oauthWait, v.flowID, v.authURL = false, "", ""
		if !errors.Is(err, context.Canceled) {
			a.plat.ShowWindow()
		}
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				v.dlgErr = err
			}
			return
		}
		v.busy = true
		in := core.AddressBookInput{
			Preset:      core.PresetGoogle,
			Name:        name,
			Username:    email,
			OAuthFlowID: flow,
			Client:      client,
		}
		var ab core.AddressBook
		a.writes.Chain("addressbooks", func(ctx context.Context) (err error) {
			ab, err = add(ctx, in)
			return err
		}, func(err error) { v.added(gen, ab, err) })
	})
}

func presetMark(c *ui.Context, preset string, size float32) {
	brandID := ""
	switch preset {
	case core.PresetICloud:
		brandID = "icloud"
	case core.PresetFastmail:
		brandID = "fastmail"
	case core.PresetGoogle:
		brandID = "gmail"
	}
	if brandID != "" {
		if b := icons.Brand(brandID); b != nil {
			markTile(c, b, size)
			return
		}
	}
	t := theme.Of(c)
	r := min(t.Radius.Lg, size/4)
	if size <= theme.MarkSm {
		r = t.Radius.Sm
	}
	iconSz := float32(theme.Icon)
	if size <= theme.MarkSm {
		iconSz = theme.IconSm
	}
	ui.Box(c).Size(size, size).Shrink(0).Radius(r).Background(t.Hover).Border(theme.Hairline, t.Border).Center().Children(func() {
		ui.Icon(c, icons.Get("contacts")).Size(iconSz, iconSz).TextColor(t.Foreground)
	})
}

func presetTitle(pr core.AddressBookPreset) string {
	switch pr.ID {
	case core.PresetGoogle:
		return i18n.T("settings.addressbooks.preset.google")
	case core.PresetCustom:
		return i18n.T("settings.addressbooks.preset.custom")
	default:
		return pr.Name
	}
}

func presetPlaceholderName(preset string) string {
	switch preset {
	case core.PresetICloud:
		return "iCloud"
	case core.PresetFastmail:
		return "Fastmail"
	case core.PresetGoogle:
		return "Google"
	default:
		return "CardDAV"
	}
}

func presetSecretName(preset string) string {
	if preset == core.PresetICloud || preset == core.PresetFastmail {
		return i18n.T("onboarding.login.app_password")
	}
	return i18n.T("onboarding.login.password")
}

func presetHint(preset string) string {
	switch preset {
	case core.PresetICloud:
		return i18n.T("settings.addressbooks.icloud.hint")
	case core.PresetFastmail:
		return i18n.T("settings.addressbooks.fastmail.hint")
	default:
		return ""
	}
}

func abDetailText(ab core.AddressBook) string {
	if ab.StatusDetail != "" {
		if s := tr("settings.addressbooks.detail." + ab.StatusDetail); s != "" {
			return s
		}
	}
	if ab.Status == "reauth_required" {
		return i18n.T("settings.addressbooks.detail.auth_rejected")
	}
	return i18n.T("settings.addressbooks.detail.sync_failed")
}

// accountEntry is a row of Settings' accounts list: a mail account or an
// address book with none.
type accountEntry struct {
	acc *core.Account
	ab  *core.AddressBook
}

func (e accountEntry) key() core.AccountEntry {
	if e.acc != nil {
		return core.AccountEntry{Account: e.acc.ID}
	}
	return core.AccountEntry{AddressBook: e.ab.ID}
}

// accountEntries are the mail accounts and standalone address books in their
// shared order, which is also the address books' priority.
func (a *App) accountEntries() []accountEntry {
	var es []accountEntry
	for i := range a.accounts {
		es = append(es, accountEntry{acc: &a.accounts[i]})
	}
	for i := range a.addressBooks {
		if a.addressBooks[i].Account == 0 {
			es = append(es, accountEntry{ab: &a.addressBooks[i]})
		}
	}
	slices.SortStableFunc(es, func(x, y accountEntry) int { // as core's abPriority: the account first on a tie
		px, py := 0, 0
		if x.acc != nil {
			px = x.acc.Position
		} else {
			px = x.ab.SortOrder
		}
		if y.acc != nil {
			py = y.acc.Position
		} else {
			py = y.ab.SortOrder
		}
		return cmp.Or(cmp.Compare(px, py), cmp.Compare(boolInt(x.acc == nil), boolInt(y.acc == nil)))
	})
	return es
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// moveEntry puts the accounts list's row i before the one at to (len for the
// end): at once here, then saved. A failed save reloads the order saved.
func (p *settingsPage) moveEntry(i, to int) {
	a := p.a
	if to > i {
		to-- // i is taken out first
	}
	if to == i {
		return
	}
	es := a.accountEntries()
	e := es[i]
	es = slices.Insert(slices.Delete(es, i, i+1), to, e)
	order := make([]core.AccountEntry, len(es))
	pos := map[core.AccountEntry]int{}
	for k, e := range es {
		order[k], pos[e.key()] = e.key(), k
	}
	accs := slices.Clone(a.accounts)
	for k := range accs {
		accs[k].Position = pos[core.AccountEntry{Account: accs[k].ID}]
	}
	slices.SortStableFunc(accs, func(x, y core.Account) int { return cmp.Compare(x.Position, y.Position) })
	books := slices.Clone(a.addressBooks)
	for k := range books {
		if books[k].Account != 0 {
			books[k].SortOrder = pos[core.AccountEntry{Account: books[k].Account}]
		} else {
			books[k].SortOrder = pos[core.AccountEntry{AddressBook: books[k].ID}]
		}
	}
	slices.SortStableFunc(books, func(x, y core.AddressBook) int { return cmp.Compare(x.SortOrder, y.SortOrder) })
	a.accounts, a.addressBooks = accs, books
	a.writes.Chain("accounts", func(ctx context.Context) error { return a.svc.ReorderAccountEntries(ctx, order) }, func(err error) {
		if err != nil {
			a.toastErr(err)
			a.load()
		}
	})
}

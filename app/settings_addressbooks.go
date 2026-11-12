package app

import (
	"cmp"
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// addressBooksView is Settings › Address Books: read-only CardDAV address
// books, dragged to order field priority, with per-collection enable switches.
type addressBooksView struct {
	a *App

	order   ui.ListState
	heights map[int64]float32

	syncing map[int64]bool
	reauth  map[int64]bool

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
		heights:    map[int64]float32{},
		syncing:    map[int64]bool{},
		reauth:     map[int64]bool{},
		add:        a.svc.AddAddressBook,
		updateAuth: a.svc.UpdateAddressBookAuth,
	}
}

// addressBooksDot reports the worst sync status across configured address books
// for the Settings sidebar's status indicator.
func (a *App) addressBooksDot() (kit.Status, string, bool) {
	retrying := false
	for _, ab := range a.addressBooks {
		switch {
		case ab.Status == "reauth_required":
			return kit.Error, "settings.dot.action", true
		case ab.Status != "" && ab.Status != "ok":
			retrying = true
		}
	}
	if retrying {
		return kit.Attention, "settings.dot.retrying", true
	}
	return 0, "", false
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

func (v *addressBooksView) openAdd() {
	v.close()
	pub := auth.GoogleCardDAVOAuth().Public
	v.dialog, v.open = "add", true
	v.preset = core.PresetICloud
	v.name, v.serverURL, v.username, v.password = "", "", "", ""
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

func (v *addressBooksView) view(c *ui.Context) {
	a, th := v.a, c.Theme()
	books := a.addressBooks
	var add []func()
	if len(books) > 0 {
		add = append(add, func() {
			kit.Button(c, i18n.T("settings.addressbooks.add"), kit.ButtonOpts{Icon: "plus"}).PaddingX(th.Space(3)).OnClick(v.openAdd)
		})
	}
	pageHeading(c, i18n.T("settings.addressbooks"), i18n.T("settings.addressbooks.sub"), add...)
	if len(books) == 0 {
		kit.Empty(c, kit.EmptyOpts{
			Icon:   "contacts",
			Title:  i18n.T("settings.addressbooks.empty.title"),
			Desc:   i18n.T("settings.addressbooks.empty.desc"),
			Dashed: true,
			Actions: func() {
				kit.EmptyAction(c, i18n.T("settings.addressbooks.add"), kit.ButtonOpts{}).OnClick(v.openAdd)
			},
		})
	} else {
		settingsSection(c, i18n.T("settings.addressbooks.list"), i18n.T("settings.addressbooks.list.foot"), func() { v.list(c) })
	}
	v.dialogView(c)
}

func (v *addressBooksView) list(c *ui.Context) {
	a, th := v.a, c.Theme()
	n := len(a.addressBooks)
	v.order.Key = func(i int) any { return a.addressBooks[i].ID }
	v.order.Label = func(i int) string { return a.addressBooks[i].Name }
	v.order.Reorder = func(rows []int, to int) { v.move(rows[0], to) }
	var h float32
	for _, ab := range a.addressBooks {
		fallback := th.Space(16) + float32(len(ab.Collections))*th.Space(10)
		h += cmp.Or(v.heights[ab.ID], fallback)
	}
	if _, wh := c.Size(); wh > 0 {
		h = min(h, max(wh/2, th.Space(30)))
	}
	kit.List(c, &v.order, n, func(i int) {
		id := a.addressBooks[i].ID
		row := cardListRow(c, i, n, func() { v.row(c, a.addressBooks[i]) })
		if b := row.LayoutBounds(); b.H > 0 && b.H != v.heights[id] {
			v.heights[id] = b.H
			c.Invalidate()
		}
	}).FillWidth().Height(h)
}

func (v *addressBooksView) move(i, to int) {
	a := v.a
	if to > i {
		to--
	}
	if to == i {
		return
	}
	moved := slices.Clone(a.addressBooks)
	ab := moved[i]
	a.addressBooks = slices.Insert(slices.Delete(moved, i, i+1), to, ab)
	ids := make([]int64, len(a.addressBooks))
	for k, x := range a.addressBooks {
		ids[k] = x.ID
	}
	a.writes.Chain("addressbooks", func(ctx context.Context) error { return a.svc.ReorderAddressBooks(ctx, ids) }, func(err error) {
		if err != nil {
			a.toastErr(err)
			a.load()
		}
	})
}

func (v *addressBooksView) row(c *ui.Context, ab core.AddressBook) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(th.Space(3)).Padding(th.Space(3), th.Space(4)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Get("grip")).Size(theme.Icon, theme.Icon).TextColor(t.Idle).Shrink(0)
			presetMark(c, ab.Preset, theme.MarkMd)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Wrap().Children(func() {
					kit.Text(c, ab.Name, t.Text.Body).FontWeight(500).SingleLine().Shrink(1)
					v.statusBadge(c, ab)
				})
				if sub := abSubtitle(ab); sub != "" {
					kit.Text(c, sub, t.Text.Sm).Font(theme.Mono).TextColor(t.Muted).SingleLine()
				}
				if ab.Status != "" && ab.Status != "ok" {
					if reason := abDetailText(ab); reason != "" {
						color := t.WarningText
						if ab.Status == "reauth_required" {
							color = t.DangerText
						}
						kit.Text(c, reason, t.Text.Sm).TextColor(color)
					}
				}
			})
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Shrink(0).Children(func() {
				kit.Button(c, i18n.T("settings.account.sync_now"), kit.ButtonOpts{
					Variant: kit.Outline, Size: kit.SizeSm, Disabled: v.syncing[ab.ID],
				}).Label(i18n.Tf("settings.addressbooks.sync.aria", ab.Name)).OnClick(func() { v.syncNow(ab.ID) })
				v.authAction(c, ab)
				kit.IconButton(c, "trash-2", i18n.Tf("settings.addressbooks.remove.aria", ab.Name), kit.ButtonOpts{
					Variant: kit.Ghost, Size: kit.SizeSm,
				}).OnClick(func() { v.openRemove(ab) })
			})
		})
		for _, col := range ab.Collections {
			col := col
			ui.Row(c.Key(col.ID)).FillWidth().Gap(th.Space(3)).Padding(th.Space(2), th.Space(4), th.Space(2), th.Space(12)).
				BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).AlignItems(ui.Center).Children(func() {
				fg := t.Foreground
				if !col.Enabled {
					fg = t.Muted
				}
				kit.Text(c, col.Name, t.Text.Base).TextColor(fg).SingleLine().Grow(1).Shrink(1)
				kit.Text(c, i18n.Plural("settings.addressbooks.cards", col.CardCount), t.Text.Sm).TextColor(t.Muted).Shrink(0)
				on := col.Enabled
				kit.Switch(c, &on).Label(i18n.Tf("settings.addressbooks.collection.toggle", col.Name))
				if on != col.Enabled {
					v.toggleCollection(ab.ID, col.ID, on)
				}
			})
		}
	})
}

func (v *addressBooksView) statusBadge(c *ui.Context, ab core.AddressBook) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	if v.syncing[ab.ID] {
		ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			kit.Spinner(c)
			kit.Text(c, i18n.T("shell.sync.syncing"), t.Text.Sm).TextColor(t.Muted)
		})
		return
	}
	dot, text, fg := kit.OK, i18n.T("settings.account.status.ok"), t.Muted
	switch {
	case ab.Status == "reauth_required":
		dot, text, fg = kit.Error, i18n.T("settings.addressbooks.status.reauth"), t.DangerText
	case ab.Status != "" && ab.Status != "ok":
		dot, text, fg = kit.Attention, i18n.T("settings.addressbooks.status.error"), t.WarningText
	case !ab.LastSyncedAt.IsZero():
		text = i18n.Tf("shell.sync.synced", a.ago(ab.LastSyncedAt))
	}
	ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
		kit.StatusDot(c, dot)
		kit.Text(c, text, t.Text.Sm).TextColor(fg)
	})
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
								delete(v.heights, id)
								v.close()
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
		v.dlgErr = nil
		v.showSecret = false
	}

	body := func() {
		for i := 0; i < len(presets); i += 2 {
			ui.Row(c.Key(i)).Gap(th.Space(2)).AlignItems(ui.Stretch).Children(func() {
				for _, pr := range presets[i:min(i+2, len(presets))] {
					pr := pr
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

func (v *addressBooksView) submitPasswordAdd() {
	if v.busy {
		return
	}
	a := v.a
	v.busy, v.dlgErr = true, nil
	gen, add := v.dlgGen, v.add
	in := core.AddressBookInput{
		Preset:   v.preset,
		Name:     strings.TrimSpace(v.name),
		URL:      strings.TrimSpace(v.serverURL),
		Username: strings.TrimSpace(v.username),
		Password: v.password,
	}
	a.writes.Chain("addressbooks", func(ctx context.Context) error {
		_, err := add(ctx, in)
		return err
	}, func(err error) {
		if gen == v.dlgGen && v.dialog == "add" {
			v.busy, v.dlgErr = false, err
			if err == nil {
				v.close()
			}
		}
		a.load()
	})
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
		a.writes.Chain("addressbooks", func(ctx context.Context) error {
			_, err := add(ctx, in)
			return err
		}, func(err error) {
			if gen == v.dlgGen && v.dialog == "add" {
				v.busy, v.dlgErr = false, err
				if err == nil {
					v.close()
				}
			}
			a.load()
		})
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
		ui.Icon(c, icons.Get("globe")).Size(iconSz, iconSz).TextColor(t.Foreground)
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

func abSubtitle(ab core.AddressBook) string {
	var parts []string
	if ab.Username != "" {
		parts = append(parts, ab.Username)
	}
	if u, err := url.Parse(ab.URL); err == nil && u.Host != "" {
		parts = append(parts, u.Host)
	} else if ab.URL != "" {
		parts = append(parts, ab.URL)
	}
	return strings.Join(parts, " · ")
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

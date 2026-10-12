package app

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// roleOrder is the order of folders with a role; the others follow by name.
var roleOrder = []string{"inbox", "drafts", "sent", "archive", "junk", "trash"}

func sortFolders(fs []core.Folder) []core.Folder {
	rank := func(f core.Folder) int {
		if i := slices.Index(roleOrder, f.Role); i >= 0 {
			return i
		}
		return len(roleOrder)
	}
	out := slices.Clone(fs)
	slices.SortStableFunc(out, func(a, b core.Folder) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), strings.Compare(a.Name, b.Name))
	})
	return out
}

// folderIcons are the icons of folder roles; another folder shows "folder".
var folderIcons = map[string]string{"inbox": "inbox", "drafts": "file-pen", "sent": "send", "archive": "archive", "junk": "spam", "trash": "trash-2"}

func folderName(f core.Folder) string {
	if f.Role != "" {
		return i18n.T("mail.folder." + f.Role)
	}
	return f.Name
}

// Sidebar: the page switch and search; Compose; the unified views; each account's folders under its
// foldable header; the outbox while it holds mail; Add account.
func (p *mailPage) Sidebar(c *ui.Context) {
	a := p.a
	th := c.Theme()
	ui.Row(c).Padding(0, th.Space(1.5)+sidebarInset(c), th.Space(3), 0).Gap(th.Space(1)).Children(func() {
		a.pageSwitch(c)
		ui.Spacer(c)
		kit.IconButton(c, "search", i18n.T("mail.search"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
			OnClick(func() { p.searchOpen = true })
	})
	sidebarScroll(c, func() {
		compose := kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: p.compose}).Gap(th.Space(2.5))
		compose.Children(func() {
			kit.SidebarIcon(c, compose, "square-pen", false)
			ui.Text(c, i18n.T("mail.compose"))
		})
		ui.Box(c).Height(th.Space(2))
		p.navItem(c, "inbox", i18n.T("mail.unified.inbox"), core.View{Unified: "inbox"}, p.inboxUnread(), 0)
		p.navItem(c, "star", i18n.T("mail.unified.starred"), core.View{Unified: "starred"}, 0, 0)
		ui.Box(c).Height(th.Space(2.5))
		for _, acc := range a.accounts {
			p.account(c, acc)
		}
		if n := len(p.outbox); n > 0 {
			p.navItem(c, "outbox", i18n.T("mail.outbox"), core.View{Unified: "outbox"}, n, 0)
		}
		kit.SidebarItem(c, kit.SidebarItemOpts{Muted: true, Small: true, OnClick: func() {
			a.navigate(Route{Page: PageSettings})
			a.openAddAccount()
		}}).Children(func() {
			ui.Icon(c, icons.Get("plus")).Size(theme.IconXs, theme.IconXs)
			ui.Text(c, i18n.T("mail.add_account"))
		})
	})
}

// navItem is a view in the sidebar, with its icon and count; indent is in spacing steps.
func (p *mailPage) navItem(c *ui.Context, icon, label string, v core.View, count int, indent float32) {
	t := theme.Of(c)
	on := p.a.route.Page == PageMail && p.a.route.View == v && !p.searched // results: no view is shown
	item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, Indent: indent, OnClick: func() { p.a.navigate(Route{Page: PageMail, View: v}) }})
	item.Children(func() {
		kit.SidebarIcon(c, item, icon, on)
		ui.Text(c, label).SingleLine().Grow(1).Shrink(1)
		if count > 0 {
			kit.Text(c, strconv.Itoa(count), t.Text.Sm).TextColor(t.Muted)
		}
	})
}

// account is an account's foldable header over its folders; folded, the
// header shows the unread count.
func (p *mailPage) account(c *ui.Context, acc core.Account) {
	open := p.accountOpen(acc.ID)
	kit.Collapsible(c, open, func() {
		ui.Text(c, cmp.Or(acc.Alias, acc.Email)).SingleLine().Grow(1).Shrink(1)
		if n := p.a.unread[acc.ID]; !*open && n > 0 {
			ui.Text(c, strconv.Itoa(n))
		}
	}, func() {
		fs := p.folders[acc.ID]
		if !slices.ContainsFunc(fs, func(f core.Folder) bool { return f.Role == "drafts" }) {
			fs = append(slices.Clone(fs), core.Folder{AccountID: acc.ID, Role: "drafts"})
		}
		ui.Column(c).Gap(c.Theme().Space(0.5)).Children(func() { // the sidebar's own gap
			for _, f := range sortFolders(fs) {
				v := core.View{FolderID: f.ID}
				if f.ID == 0 {
					v = localDrafts(acc.ID)
				}
				p.navItem(c, cmp.Or(folderIcons[f.Role], "folder"), folderName(f), v, f.Unread, 4.5)
			}
		})
	})
}

// accountOpen is whether an account's folders show in the sidebar; at first they do.
func (p *mailPage) accountOpen(id int64) *bool { return boolAt(p.accOpen, id, true) }

// boolAt is m[k], first set to def: a pointer that outlives the frame, as kit.Collapsible needs.
func boolAt[K comparable](m map[K]*bool, k K, def bool) *bool {
	if m[k] == nil {
		m[k] = &def
	}
	return m[k]
}

// compose starts a new draft from the view's account, else the default
// one, and opens it in a compose window.
func (p *mailPage) compose() {
	p.newDraft(func(d core.Draft) { p.a.plat.OpenCompose(d.ID) })
}

// newDraft starts a new draft from the viewed account, else the default or
// first one, and hands it to then.
func (p *mailPage) newDraft(then func(core.Draft)) {
	a := p.a
	acc := p.viewAccount(a.route.View)
	if i := slices.IndexFunc(a.accounts, func(x core.Account) bool { return x.IsDefault }); acc == 0 && i >= 0 {
		acc = a.accounts[i].ID
	} else if acc == 0 && len(a.accounts) > 0 {
		acc = a.accounts[0].ID
	}
	if acc == 0 {
		return
	}
	var d core.Draft
	a.do(func(ctx context.Context) (err error) {
		d, err = a.svc.NewDraft(ctx, acc, "new", 0)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		then(d)
	})
}

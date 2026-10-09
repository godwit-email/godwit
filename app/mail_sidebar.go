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

func folderName(f core.Folder) string {
	if f.Role != "" {
		return i18n.T("mail.folder." + f.Role)
	}
	return f.Name
}

// Sidebar: Compose; the unified views; each account's folders, grouped
// when there are several accounts; the outbox while it holds mail; Add account.
func (p *mailPage) Sidebar(c *ui.Context) {
	a := p.a
	t := theme.Of(c)
	s := t.Scale
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(2 * s).Children(func() {
			ui.ButtonBase(c).Height(32 * s).PaddingX(8 * s).Gap(10 * s).Radius(8).FontWeight(500).Children(func() {
				ui.Icon(c, icons.Get("square-pen")).Size(16, 16)
				ui.Text(c, i18n.T("mail.compose"))
			}).OnClick(p.compose)
			ui.Box(c).Height(8 * s)
			p.navItem(c, i18n.T("mail.unified.inbox"), core.View{Unified: "inbox"}, p.inboxUnread(), 0)
			p.navItem(c, i18n.T("mail.unified.starred"), core.View{Unified: "starred"}, 0, 0)
			ui.Box(c).Height(10 * s)
			grouped := len(a.accounts) > 1
			for _, acc := range a.accounts {
				indent := float32(0)
				if grouped {
					p.accountHeader(c, acc)
					if p.collapsed[acc.ID] {
						continue
					}
					indent = 18
				}
				for _, f := range sortFolders(p.folders[acc.ID]) {
					p.navItem(c, folderName(f), core.View{FolderID: f.ID}, f.Unread, indent)
				}
			}
			if n := len(p.outbox); n > 0 {
				p.navItem(c, i18n.T("mail.outbox"), core.View{Unified: "outbox"}, n, 0)
			}
			ui.ButtonBase(c).Height(30 * s).PaddingX(8 * s).Gap(6 * s).Radius(8).TextColor(t.Muted).FontSize(t.FontSize - 1).Children(func() {
				ui.Icon(c, icons.Get("plus")).Size(12, 12)
				ui.Text(c, i18n.T("mail.add_account"))
			}).OnClick(func() {
				a.navigate(Route{Page: PageSettings})
				a.openAddAccount()
			})
		})
	})
}

// navItem is a view in the sidebar, with its count.
func (p *mailPage) navItem(c *ui.Context, label string, v core.View, count int, indent float32) {
	t := theme.Of(c)
	s := t.Scale
	on := p.a.route.Page == PageMail && p.a.route.View == v
	b := ui.ButtonBase(c).Height(32*s).Padding(0, 8*s, 0, (8+indent)*s).Radius(8).Gap(8 * s)
	switch {
	case on:
		b.Background(t.SidebarSelected).FontWeight(500)
	case b.Hovered():
		b.Background(t.Hover)
	}
	b.Children(func() {
		ui.Text(c, label).SingleLine().Grow(1).Shrink(1)
		if count > 0 {
			ui.Text(c, strconv.Itoa(count)).FontSize(t.FontSize - 1).TextColor(t.Muted)
		}
	})
	if !on {
		b.OnClick(func() { p.a.navigate(Route{Page: PageMail, View: v}) })
	}
}

// accountHeader folds an account's folders; folded, it shows the unread count.
func (p *mailPage) accountHeader(c *ui.Context, acc core.Account) {
	t := theme.Of(c)
	s := t.Scale
	open := !p.collapsed[acc.ID]
	chevron := "chevron-down"
	if !open {
		chevron = "chevron-right"
	}
	ui.ButtonBase(c).Height(30 * s).PaddingX(8 * s).Gap(6 * s).Radius(8).TextColor(t.Muted).FontSize(t.FontSize - 1).Children(func() {
		ui.Icon(c, icons.Get(chevron)).Size(12, 12)
		ui.Text(c, cmp.Or(acc.Alias, acc.Email)).SingleLine().Grow(1).Shrink(1)
		if n := p.a.unread[acc.ID]; !open && n > 0 {
			ui.Text(c, strconv.Itoa(n))
		}
	}).OnClick(func() { p.collapsed[acc.ID] = open })
}

// compose starts a new draft from the view's account, else the default
// one, and opens it.
func (p *mailPage) compose() {
	a := p.a
	var acc int64
	if f, ok := p.folder(a.route.View.FolderID); ok {
		acc = f.AccountID
	} else if i := slices.IndexFunc(a.accounts, func(x core.Account) bool { return x.IsDefault }); i >= 0 {
		acc = a.accounts[i].ID
	} else if len(a.accounts) > 0 {
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
		a.navigate(Route{Page: PageCompose, DraftID: d.ID})
	})
}

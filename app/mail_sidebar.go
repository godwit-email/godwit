package app

import (
	"cmp"
	"context"
	"maps"
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

// roleOrder is the order of the role folders, above an account's own folders.
var roleOrder = []string{"inbox", "flagged", "drafts", "sent", "archive", "junk", "trash"}

// folderIcons are the icons of folder roles; another folder shows "folder".
var folderIcons = map[string]string{"inbox": "inbox", "flagged": "star", "drafts": "file-pen", "sent": "send", "archive": "archive", "junk": "spam", "trash": "trash-2"}

// folderName is a role folder's name in the UI's language, else the server's name.
func folderName(f core.Folder) string {
	if f.Role == "flagged" {
		return i18n.T("mail.unified.starred")
	}
	if slices.Contains(roleOrder, f.Role) {
		return i18n.T("mail.folder." + f.Role)
	}
	return f.Name
}

// sideFolder is a row of an account's folders in the sidebar.
type sideFolder struct {
	core.Folder        // ID 0 and no Role: a parent only named in its children's paths
	name        string // what the row shows
	key         string // a user folder's path segments joined by "\x00"; "" for a role folder
	depth       int    // 1 for role folders and top-level user folders
	kids        bool
	sum         int // unread in it and every folder under it
}

// sidebarFolders are an account's folder rows: its role folders in roleOrder
// (the local drafts for missing drafts), then its own folders as a tree split on the server's
// delimiter, each parent before its children and siblings by name, with a row
// for a parent only named in paths. Folders under a role folder (Courier's
// INBOX.Projects) show at the top, but keep the role folder in their key.
// Only folders that map onto a row are folded into it: Gmail's Starred into
// 已标星 at the top, its All Mail into 归档 when there is no archive. The rest
// show as they are.
func sidebarFolders(acc int64, fs []core.Folder) []sideFolder {
	byRole := map[string]core.Folder{}
	for _, f := range fs {
		if _, ok := byRole[f.Role]; f.Role != "" && !ok {
			byRole[f.Role] = f
		}
	}
	if byRole["flagged"].Role == "" {
		byRole["flagged"] = core.Folder{AccountID: acc, Role: "flagged"}
	}
	if byRole["drafts"].Role == "" {
		byRole["drafts"] = core.Folder{AccountID: acc, Role: "drafts"}
	}
	var out []sideFolder
	for _, r := range roleOrder {
		if f, ok := byRole[r]; ok {
			out = append(out, sideFolder{Folder: f, name: folderName(f), depth: 1, sum: f.Unread})
		}
	}
	nodes := map[string]*sideFolder{}
	for _, f := range fs {
		if f.Role != "" {
			continue
		}
		segs := []string{f.Name}
		if f.Delim != "" {
			segs = slices.DeleteFunc(strings.Split(f.Name, f.Delim), func(s string) bool { return s == "" })
		}
		top := 0 // segments of the role folder it is under, shown by no row
		for _, r := range fs {
			if p := r.Name + f.Delim; r.Role != "" && f.Delim != "" && strings.HasPrefix(f.Name, p) {
				top = max(top, min(len(strings.Split(r.Name, f.Delim)), len(segs)-1))
			}
		}
		for k := top + 1; k <= len(segs); k++ {
			key := strings.Join(segs[:k], "\x00")
			n := nodes[key]
			if n == nil {
				n = &sideFolder{Folder: core.Folder{AccountID: acc}, name: segs[k-1], key: key, depth: k - top}
				nodes[key] = n
			}
			n.sum += f.Unread
			if k == len(segs) {
				n.Folder = f
			} else {
				n.kids = true
			}
		}
	}
	// "\x00" sorts before any other byte: a parent's subtree comes right after it.
	for _, k := range slices.Sorted(maps.Keys(nodes)) {
		out = append(out, *nodes[k])
	}
	return out
}

// Sidebar: the page switch and search; Compose; the unified views; the label
// and account groups, each foldable; the outbox while it holds mail.
func (p *mailPage) Sidebar(c *ui.Context) {
	a := p.a
	th := c.Theme()
	ui.Row(c).Padding(0, th.Space(1.5)+sidebarInset(c), th.Space(3), 0).Gap(th.Space(1)).Children(func() {
		a.pageSwitch(c)
		ui.Spacer(c)
		kit.IconButton(c, "search", i18n.T("mail.search"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
			OnClick(a.openPalette)
	})
	sidebarScroll(c, func() {
		compose := kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: p.compose}).Gap(th.Space(2.5))
		compose.Children(func() {
			kit.SidebarIcon(c, compose, "square-pen", false)
			ui.Text(c, i18n.T("mail.compose"))
		})
		ui.Box(c).Height(th.Space(2))
		p.navItem(c, "inbox", i18n.T("mail.unified.inbox"), core.View{Unified: "inbox"}, p.inboxUnread())
		p.navItem(c, "star", i18n.T("mail.unified.starred"), core.View{Unified: "starred"}, 0)
		if n := len(p.outbox); n > 0 {
			p.navItem(c, "outbox", i18n.T("mail.outbox"), core.View{Unified: "outbox"}, n)
		}
		ui.Box(c).Height(th.Space(2))
		p.labelNav(c)
		kit.SidebarCollapsible(c, &p.accountsOpen, true, func() {
			ui.Text(c, i18n.T("mail.accounts")).SingleLine().Grow(1).Shrink(1)
		}, func() {
			for _, acc := range a.accounts {
				p.account(c, acc)
			}
			kit.SidebarItem(c, kit.SidebarItemOpts{Muted: true, Small: true, OnClick: func() {
				a.navigate(Route{Page: PageSettings})
				a.openAddAccount()
			}}).Children(func() {
				ui.Icon(c, icons.Get("plus")).Size(theme.IconXs, theme.IconXs)
				ui.Text(c, i18n.T("mail.add_account"))
			})
		})
	})
}

// navItem is a view in the sidebar, with its icon and count.
func (p *mailPage) navItem(c *ui.Context, icon, label string, v core.View, count int) {
	t := theme.Of(c)
	on := p.viewShown(v)
	item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, OnClick: func() { p.a.navigate(Route{Page: PageMail, View: v}) }})
	item.Children(func() {
		kit.SidebarIcon(c, item, icon, on)
		ui.Text(c, label).SingleLine().Grow(1).Shrink(1)
		if count > 0 {
			kit.Text(c, strconv.Itoa(count), t.Text.Sm).TextColor(t.Muted)
		}
	})
}

// viewShown is whether the mail page shows v; while results show, no view is.
func (p *mailPage) viewShown(v core.View) bool {
	return p.a.route.Page == PageMail && p.a.route.View == v && !p.searched
}

// account is an account's foldable row, its provider's mark (badged while its
// sync is in trouble), name and unread count, over all its folders.
func (p *mailPage) account(c *ui.Context, acc core.Account) {
	t := theme.Of(c)
	name := accountName(acc)
	kit.SidebarCollapsible(c, p.accountOpen(acc.ID), false, func() {
		p.a.accountMark(c, acc) // as in the settings sidebar
		ui.Text(c, name).SingleLine().Grow(1).Shrink(1).TextColor(t.Foreground)
		if n := p.a.unread[acc.ID]; n > 0 {
			kit.Text(c, strconv.Itoa(n), t.Text.Sm).TextColor(t.Muted)
		}
	}, func() {
		hide := "" // the subtree of a folded folder
		for _, f := range sidebarFolders(acc.ID, p.folders[acc.ID]) {
			if hide != "" && strings.HasPrefix(f.key, hide) {
				continue
			}
			hide = ""
			open := boolAt(p.folderOpen, folderKey{acc.ID, f.key}, true)
			if f.kids && !*open {
				hide = f.key + "\x00"
			}
			p.folderRow(c, f, open)
		}
	})
}

// folderKey names a user folder of an account by its path, folded or not.
type folderKey struct {
	acc  int64
	path string
}

// folderRow is a folder's row, indented a step per level; a parent's chevron
// folds it, and folded it counts the unread under it too. Junk and Trash show
// no count.
func (p *mailPage) folderRow(c *ui.Context, f sideFolder, open *bool) {
	t := theme.Of(c)
	v := core.View{FolderID: f.ID}
	if f.Role == "drafts" && f.ID == 0 {
		v = localDrafts(f.AccountID)
	}
	if f.Role == "flagged" {
		v = accountStarred(f.AccountID)
	}
	real := f.ID != 0 || f.Role != ""
	on := real && p.viewShown(v)
	toggle := func() { *open = !*open }
	click := func() { p.a.navigate(Route{Page: PageMail, View: v}) }
	if !real {
		click = toggle
	}
	item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, Indent: 4 * float32(f.depth), OnClick: click})
	item.Children(func() {
		kit.SidebarIcon(c, item, cmp.Or(folderIcons[f.Role], "folder"), on)
		ui.Text(c, f.name).SingleLine().Grow(1).Shrink(1)
		n := f.Unread
		if f.kids && !*open {
			n = f.sum
		}
		if n > 0 && f.Role != "junk" && f.Role != "trash" {
			kit.Text(c, strconv.Itoa(n), t.Text.Sm).TextColor(t.Muted)
		}
		if f.kids {
			ch, label := kit.SidebarChevron(c, 0), "mail.folder.expand"
			if *open {
				ch, label = ch.Rotate(90), "mail.folder.collapse"
			}
			if real { // the row opens the folder; the chevron folds it
				ch.OnClick(toggle).Label(i18n.Tf(label, f.name))
			}
		}
	})
}

// accountOpen is whether an account's folders show in the sidebar; at first
// only the first account's do.
func (p *mailPage) accountOpen(id int64) *bool {
	return boolAt(p.accOpen, id, len(p.a.accounts) > 0 && p.a.accounts[0].ID == id)
}

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

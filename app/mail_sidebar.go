package app

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/backend"
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
	totalSum    int // total messages in it and every folder under it
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
			out = append(out, sideFolder{Folder: f, name: folderName(f), depth: 1, sum: f.Unread, totalSum: f.Total})
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
			n.totalSum += f.Total
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

// Sidebar: the page switch; Compose; the unified views; the label and account
// groups, each foldable; the outbox and Scheduled while they hold mail.
func (p *mailPage) Sidebar(c *ui.Context) {
	a := p.a
	th := c.Theme()
	a.sidebarHead(c)
	sidebarScroll(c, func() {
		compose := kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: p.compose}).Gap(th.Space(2.5))
		compose.Children(func() {
			kit.SidebarIcon(c, compose, "square-pen", false)
			ui.Text(c, i18n.T("mail.compose"))
		})
		ui.Box(c).Height(th.Space(2))
		p.navItem(c, "inbox", i18n.T("mail.unified.inbox"), core.View{Unified: "inbox"}, p.inboxUnread())
		for _, acc := range a.accounts {
			if !isArchive(acc) {
				if f, ok := p.inboxFolder(acc.ID); ok {
					p.inboxAccountRow(c, acc, f)
				}
			}
		}
		p.navItem(c, "star", i18n.T("mail.unified.starred"), core.View{Unified: "starred"}, 0)
		if n := len(p.outbox); n > 0 {
			p.navItem(c, "outbox", i18n.T("mail.outbox"), core.View{Unified: "outbox"}, n)
		}
		if n := len(p.scheduled); n > 0 {
			p.navItem(c, "time", i18n.T("mail.scheduled"), core.View{Unified: "scheduled"}, n)
		}
		ui.Box(c).Height(th.Space(3.5))
		order := core.SidebarOrder(a.settings)
		shown := 0
		for _, section := range order {
			if !p.sectionShown(section) {
				continue
			}
			if shown > 0 {
				ui.Box(c).Height(th.Space(3.5))
			}
			shown++
			var head ui.Element
			var items func(*ui.Menu)
			switch section {
			case "views":
				head, items = p.viewNav(c), p.viewsMenu
			case "labels":
				head, items = p.labelNav(c), p.labelsMenu
			case "accounts":
				head, items = p.accountsNav(c), p.accountsMenu
			}
			p.sectionHead(c, head, order, section, items)
		}
	})
}

// sidebarSection is a section of core.SidebarSections, as its header drags it.
type sidebarSection string

// sectionHead lets a section's header be dragged onto another's to take its
// place, and its menu move it past the shown section above or below; either
// saves Settings.SidebarOrder. The menu starts with the section's own items, if any.
func (p *mailPage) sectionHead(c *ui.Context, head ui.Element, order []string, section string, items func(*ui.Menu)) {
	i := slices.Index(order, section)
	head.Drag(sidebarSection(section))
	if head.Dragging() {
		head.Opacity(0.4)
	}
	if v, ok := ui.DragOver[sidebarSection](head); ok && string(v) != section {
		below := slices.Index(order, string(v)) < i // one from above lands under this header
		accent := c.Theme().Accent
		head.DrawOver(func(pt *ui.Painter, r ui.Rect) {
			y := r.Y
			if below {
				y += r.H
			}
			// As ui.List draws where dragged rows go: a dot, then the line.
			pt.Fill(ui.Rect{X: r.X + 4, Y: y - 1, W: r.W - 4, H: 2}, accent, 1)
			pt.Fill(ui.Rect{X: r.X, Y: y - 4, W: 8, H: 8}, accent, 4)
		})
	}
	if v, ok := ui.Drop[sidebarSection](head); ok {
		p.moveSection(order, slices.Index(order, string(v)), i)
	}
	up, down := -1, -1
	for k, s := range order {
		if s != section && p.sectionShown(s) {
			if k < i {
				up = k
			} else if down < 0 {
				down = k
			}
		}
	}
	head.ContextMenu(func(m *ui.Menu) {
		if items != nil {
			items(m)
			m.Separator()
		}
		if m.Item(i18n.T("mail.section.up")).Disabled(up < 0).Chosen() {
			p.moveSection(order, i, up)
		}
		if m.Item(i18n.T("mail.section.down")).Disabled(down < 0).Chosen() {
			p.moveSection(order, i, down)
		}
	})
}

// moveSection takes the section at i out of order and puts it at to of
// what is left, or after the one at to when that is below i, and saves it.
func (p *mailPage) moveSection(order []string, i, to int) {
	if i < 0 || to < 0 || i == to {
		return
	}
	next := slices.Clone(order)
	s := next[i]
	next = slices.Insert(slices.Delete(next, i, i+1), to, s)
	st := p.a.settings
	st.SidebarOrder = strings.Join(next, ",")
	p.a.changeSettings(st)
}

// sectionShown reports whether the sidebar shows the section: labels only
// while one is on some mail.
func (p *mailPage) sectionShown(s string) bool {
	return s != "labels" || slices.ContainsFunc(p.a.labels, func(l core.Label) bool { return l.Count > 0 })
}

// accountsNav is the sidebar's other-folders section; its header's menu adds an
// account or local archive (accountsMenu). It returns the header.
func (p *mailPage) accountsNav(c *ui.Context) ui.Element {
	a := p.a
	head := kit.SidebarItem(c, kit.SidebarItemOpts{Muted: true, Open: &p.accountsOpen,
		Title: func() { ui.Text(c, i18n.T("mail.other_folders")).SingleLine().Shrink(1) },
		Group: func() {
			for _, acc := range a.accounts {
				p.account(c, acc)
			}
		}})
	return head
}

// inboxFolder returns accID's inbox folder, if loaded.
func (p *mailPage) inboxFolder(accID int64) (core.Folder, bool) {
	for _, f := range p.folders[accID] {
		if f.Role == "inbox" {
			return f, true
		}
	}
	return core.Folder{}, false
}

// inboxAccountRow is an account's inbox listed directly under All Inboxes.
func (p *mailPage) inboxAccountRow(c *ui.Context, acc core.Account, f core.Folder) {
	t := theme.Of(c)
	name := accountName(acc)
	v := core.View{FolderID: f.ID}
	on := p.viewShown(v)
	item := kit.SidebarItem(c, kit.SidebarItemOpts{
		Selected: on, Covered: p.viewEdit != nil, Muted: !on, Indent: 4,
		OnClick: func() { p.a.navigate(Route{Page: PageMail, View: v}) },
	}).Label(name + " · " + i18n.T("mail.folder.inbox"))
	item.Children(func() {
		p.a.accountMark(c, acc)
		ui.Text(c, name).SingleLine().Grow(1).Shrink(1).Role(ui.RoleNone)
		if f.Unread > 0 {
			kit.Text(c, strconv.Itoa(f.Unread), t.Text.Xs).TextColor(t.Muted)
		}
	})
	item.ContextMenu(func(m *ui.Menu) {
		if m.Item(i18n.T("mail.mark_all_read")).Disabled(f.Unread == 0).Chosen() {
			p.markViewAsRead(v)
		}
		m.Separator()
		p.accountItems(m, acc, f.ID)
	})
}

// accountsMenu is the accounts header's own menu items: add an account, or a local archive.
func (p *mailPage) accountsMenu(m *ui.Menu) {
	if m.Item(i18n.T("mail.add_account")).Icon(menuIcon("plus")).Chosen() {
		p.a.navigate(Route{Page: PageSettings})
		p.a.openAddAccount()
	}
	if m.Item(i18n.T("mail.archive.new")).Icon(menuIcon("archive")).Chosen() {
		p.newArchive()
	}
}

// navItem is a view in the sidebar, with its icon and count.
func (p *mailPage) navItem(c *ui.Context, icon, label string, v core.View, count int) {
	t := theme.Of(c)
	on := p.viewShown(v)
	item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, Covered: p.viewEdit != nil, OnClick: func() { p.a.navigate(Route{Page: PageMail, View: v}) }})
	item.Children(func() {
		kit.SidebarIcon(c, item, icon, on)
		ui.Text(c, label).SingleLine().Grow(1).Shrink(1)
		if count > 0 {
			kit.Text(c, strconv.Itoa(count), t.Text.Xs).TextColor(t.Muted)
		}
	})
	if v.Unified == "inbox" || v.Unified == "starred" {
		item.ContextMenu(func(m *ui.Menu) {
			switch v.Unified {
			case "inbox":
				if m.Item(i18n.T("mail.mark_all_read")).Disabled(count == 0).Chosen() {
					p.markViewAsRead(v)
				}
			case "starred":
				if m.Item(i18n.T("mail.mark_all_read")).Disabled(p.starredUnreadTotal == 0).Chosen() {
					p.markViewAsRead(v)
				}
			}
		})
	}
}

// viewShown is whether the mail page shows v; while results show, no view is.
func (p *mailPage) viewShown(v core.View) bool {
	return p.a.route.Page == PageMail && p.a.route.View == v && !p.searched
}

// account is an account's foldable row under Other folders, its provider's mark
// (badged while its sync is in trouble) and name over its folders (excluding
// inbox for mail accounts, whose inboxes sit directly under All Inboxes).
func (p *mailPage) account(c *ui.Context, acc core.Account) {
	t := theme.Of(c)
	name := accountName(acc)
	_, hasInbox := p.inboxFolder(acc.ID)
	hideInbox := !isArchive(acc) && hasInbox
	row := kit.SidebarItem(c, kit.SidebarItemOpts{Open: p.accountOpen(acc.ID),
		Title: func() {
			p.a.accountMark(c, acc) // as in the settings sidebar
			ui.Text(c, name).SingleLine().Shrink(1).TextColor(t.Foreground)
		},
		Group: func() {
			hide := "" // the subtree of a folded folder
			for _, f := range sidebarFolders(acc.ID, p.folders[acc.ID]) {
				if hideInbox && f.Role == "inbox" {
					continue
				}
				if f.Role == "drafts" {
					p.accountLabelsNav(c, acc.ID)
				}
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
		}}).Label(name + " · " + i18n.T("mail.other_folders"))
	row.ContextMenu(func(m *ui.Menu) { p.accountItems(m, acc, 0) })
	p.saveAccountOpen(acc.ID)
}

// accountItems are a sidebar menu's account entries: sync it now, or edit it
// in settings. A local archive's import into folderID instead of syncing.
func (p *mailPage) accountItems(m *ui.Menu, acc core.Account, folderID int64) {
	a, id, name := p.a, acc.ID, accountName(acc)
	if isArchive(acc) {
		p.archiveItems(m, acc, folderID)
	} else if m.Item(i18n.Tf("mail.account_sync", name)).Disabled(a.syncBusy(id)).Chosen() {
		a.syncNow(id)
	}
	if m.Item(i18n.Tf("mail.account_edit", name)).Chosen() {
		a.navigate(Route{Page: PageSettings, Settings: accountRoute(id)})
	}
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
	item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, Covered: p.viewEdit != nil, Muted: !on, Indent: 4 * float32(f.depth), OnClick: click})
	item.Children(func() {
		kit.SidebarIcon(c, item, cmp.Or(folderIcons[f.Role], "folder"), on)
		ui.Text(c, f.name).SingleLine().Grow(1).Shrink(1)
		n := f.Unread
		if f.kids && !*open {
			n = f.sum
		}
		if n > 0 && f.Role != "junk" && f.Role != "trash" {
			kit.Text(c, strconv.Itoa(n), t.Text.Xs).TextColor(t.Muted)
		}
		if f.kids {
			ch, label := kit.Chevron(c, *open, theme.IconXs), "mail.folder.expand"
			if *open {
				label = "mail.folder.collapse"
			}
			if real { // the row opens the folder; the chevron folds it
				ch.OnClick(toggle).Label(i18n.Tf(label, f.name))
			}
		}
	})
	item.ContextMenu(func(m *ui.Menu) {
		if real && f.Role != "drafts" && f.Role != "sent" { // a path-only parent is no folder to act on
			switch f.Role {
			case "trash":
				total := f.Total
				if f.kids && !*open {
					total = f.totalSum
				}
				why := p.a.unsupported(f.AccountID, backend.FeaturePurge)
				if m.Item(i18n.T("mail.empty_trash")).Disabled(total == 0 || why != "").Subtitle(why).Chosen() {
					p.confirmEmptyFolder(f.ID, "trash", f.AccountID)
				}
			case "junk":
				total := f.Total
				if f.kids && !*open {
					total = f.totalSum
				}
				why := p.a.unsupported(f.AccountID, backend.FeaturePurge)
				if m.Item(i18n.T("mail.empty_spam")).Disabled(total == 0 || why != "").Subtitle(why).Chosen() {
					p.confirmEmptyFolder(f.ID, "junk", f.AccountID)
				}
			case "flagged":
				unread := p.starredUnread[f.AccountID]
				if m.Item(i18n.T("mail.mark_all_read")).Disabled(unread == 0).Chosen() {
					p.markViewAsRead(core.View{Unified: fmt.Sprintf("starred:%d", f.AccountID)})
				}
			default:
				unread := f.Unread
				if f.kids && !*open {
					unread = f.sum
				}
				if m.Item(i18n.T("mail.mark_all_read")).Disabled(unread == 0).Chosen() {
					p.markViewAsRead(core.View{FolderID: f.ID})
				}
			}
			if f.ID != 0 && f.Role == "" && p.a.isArchiveID(f.AccountID) {
				p.localFolderItems(m, f)
			}
			m.Separator()
		}
		if acc, ok := p.a.accountByID(f.AccountID); ok {
			target := int64(0) // an archive imports into the folder, else its inbox
			if f.Role == "" || f.Role == "inbox" {
				target = f.ID
			}
			p.accountItems(m, acc, target)
		}
	})
}

// accountOpen is whether an account's folders show in the sidebar: as the
// user left them (Settings.SidebarAccountsOpen), else only the first account's do.
func (p *mailPage) accountOpen(id int64) *bool {
	open, ok := p.a.settings.SidebarAccountsOpen[id]
	if !ok {
		open = len(p.a.accounts) > 0 && p.a.accounts[0].ID == id
	}
	return boolAt(p.accOpen, id, open)
}

// saveAccountOpen saves an account's fold once its row changes it; the
// row's first frame only notes what it starts as.
func (p *mailPage) saveAccountOpen(id int64) {
	open := *p.accOpen[id]
	seen, ok := p.accOpenSeen[id]
	p.accOpenSeen[id] = open
	if !ok || seen == open {
		return
	}
	st := p.a.settings
	st.SidebarAccountsOpen = maps.Clone(st.SidebarAccountsOpen)
	if st.SidebarAccountsOpen == nil {
		st.SidebarAccountsOpen = map[int64]bool{}
	}
	st.SidebarAccountsOpen[id] = open
	p.a.changeSettings(st)
}

// accountLabelsOpen is whether an account's labels show in the sidebar; default false.
func (p *mailPage) accountLabelsOpen(id int64) *bool {
	return boolAt(p.accLabelsOpen, id, false)
}

// accountLabelsNav is an account's foldable label group in the sidebar, placed
// between flagged and drafts.
func (p *mailPage) accountLabelsNav(c *ui.Context, accID int64) {
	labels := p.a.AccountLabels(accID)
	hasLabels := slices.ContainsFunc(labels, func(l core.Label) bool { return l.Count > 0 })
	viewInAccount := p.a.route.Page == PageMail && p.a.route.View.AccountID == accID && p.a.route.View.LabelID != 0
	if !hasLabels && !viewInAccount {
		return
	}
	t := theme.Of(c)
	open := p.accountLabelsOpen(accID)
	totalUnread := 0
	for _, l := range labels {
		totalUnread += l.Unread
	}
	count := ""
	if !*open && totalUnread > 0 {
		count = strconv.Itoa(totalUnread)
	}
	kit.SidebarItem(c, kit.SidebarItemOpts{Open: open, Indent: 4, Count: count,
		Title: func() {
			ui.Icon(c, icons.Get("tag")).Size(theme.Icon, theme.Icon).Shrink(0).TextColor(t.Muted)
			ui.Text(c, i18n.T("mail.labels")).TextColor(t.Muted).SingleLine().Shrink(1)
		},
		Group: func() {
			for _, l := range labels {
				if l.Count == 0 {
					continue
				}
				v := core.View{AccountID: accID, LabelID: l.ID}
				on := p.viewShown(v)
				item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, Covered: p.viewEdit != nil, Muted: !on, Indent: 8, OnClick: func() { p.a.navigate(Route{Page: PageMail, View: v}) }})
				item.Children(func() {
					ui.Icon(c, icons.Get("label-"+l.Icon)).Size(theme.Icon, theme.Icon).TextColor(t.Label(l.Color).Text)
					ui.Text(c, l.Name).SingleLine().Grow(1).Shrink(1)
					if l.Unread > 0 {
						kit.Text(c, strconv.Itoa(l.Unread), t.Text.Xs).TextColor(t.Muted)
					}
				})
			}
		}})
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
	if acc == 0 || a.isArchiveID(acc) {
		acc = a.defaultAccount()
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

// sidebarHead is the top of either page's sidebar: the page switch. Search is
// the title bar's field. On splitChrome platforms (Windows/Linux), the page
// switch lives in the aligned Mica context bar above the sidebar.
func (a *App) sidebarHead(c *ui.Context) {
	if splitChrome() {
		return
	}
	th := c.Theme()
	ui.Row(c).Padding(0, sidebarInset(c), th.Space(3), 0).Children(func() { a.pageSwitch(c) })
}

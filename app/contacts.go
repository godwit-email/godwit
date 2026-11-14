package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// contactsPage lists everyone the user exchanged synced mail with, by
// category and filter, beside the chosen contact (Route.Contact): what they
// add up to, the ways to clean up after them, and all the mail between them
// and the user, a page at a time. Contacts are read only.
type contactsPage struct {
	a        *App
	all      []core.Contact
	detail   core.ContactDetail    // of detailOf, as the last load read it
	mail     []core.MessageSummary // the mail between detailOf and the user read so far, newest first
	cursor   string                // where mail's next page starts; "" once all is read
	more     bool                  // a next page is being read
	mailList *ui.ListState
	detailOf string // the address detail and mail are of: until it is the route's, neither shows
	cat      int    // contactCats index
	query    string
	list     *ui.ListState
	cur      int    // the row chosen, as the list keeps it; -1 none
	read     latest // the contacts and the detail; a load drops the one before and its next page
	syncing  bool

	unsub    core.Unsubscribe // a one-click unsubscribe from unsubID, waiting to be confirmed
	unsubID  int64
	unsubDlg bool

	avatars    map[string]*contactAvatar // by primary address and newest message, as settingsSavedVer was
	avatarsVer uint64
}

// contactAvatar is a contact's sender avatar once read; nil while it is read,
// or when there is none.
type contactAvatar struct{ img ui.ImageSource }

func contactAddrs(c core.Contact) []core.ContactAddress {
	if len(c.Addresses) > 0 {
		return c.Addresses
	}
	if c.Address == "" {
		return nil
	}
	return []core.ContactAddress{{Address: c.Address, Blocked: c.Blocked}}
}

func contactAnyBlocked(a *App, c core.Contact) bool {
	return c.Blocked || slices.ContainsFunc(contactAddrs(c), func(ea core.ContactAddress) bool {
		return slices.Contains(a.blocked, ea.Address)
	})
}

func contactAllBlocked(a *App, c core.Contact) bool {
	addrs := contactAddrs(c)
	return len(addrs) > 0 && !slices.ContainsFunc(addrs, func(ea core.ContactAddress) bool {
		return !slices.Contains(a.blocked, ea.Address)
	})
}

func findContact(all []core.Contact, addr string) (core.Contact, int) {
	key := gomail.AddrKey(addr)
	if key == "" {
		return core.Contact{}, -1
	}
	for i, c := range all {
		if c.Address == key || slices.ContainsFunc(c.Addresses, func(ea core.ContactAddress) bool { return ea.Address == key }) {
			return c, i
		}
	}
	return core.Contact{}, -1
}

// contactCats are the sidebar's categories: every contact, those with mail,
// those from an address book, those with a List-Unsubscribe, those blocked.
var contactCats = []struct {
	key, icon string
	in        func(a *App, c core.Contact) bool
}{
	{"all", "label-team", func(*App, core.Contact) bool { return true }},
	{"people", "label-chat-3", func(_ *App, c core.Contact) bool { return c.Messages > 0 }},
	{"addressbook", "contacts", func(_ *App, c core.Contact) bool { return c.FromAddressBook }},
	{"subscriptions", "label-newspaper", func(_ *App, c core.Contact) bool { return c.ListMessage != 0 }},
	{"blocked", "spam", contactAnyBlocked},
}

var _ pageView = (*contactsPage)(nil)

func newContactsPage(a *App) *contactsPage {
	return &contactsPage{a: a, list: &ui.ListState{}, mailList: &ui.ListState{}, cur: -1}
}

// openContact shows addr (a From header or its address) on the contacts page.
func (a *App) openContact(addr string) {
	a.navigate(Route{Page: PageContacts, Contact: gomail.AddrKey(addr)})
}

func (p *contactsPage) Title() (string, string) {
	if len(p.all) == 0 {
		return i18n.T("shell.nav.contacts"), ""
	}
	return i18n.T("shell.nav.contacts"), i18n.Tf("contacts.count", len(p.all))
}

func (p *contactsPage) refresh(hints []events.Event) {
	if slices.ContainsFunc(hints, func(ev events.Event) bool {
		return ev.Type == events.AddressBooksChanged || ev.Type == events.SettingsChanged
	}) {
		p.avatars = nil
	}
	p.load()
}

func (p *contactsPage) syncAddressBooks() {
	if p.syncing {
		return
	}
	a := p.a
	p.syncing = true
	a.do(func(ctx context.Context) error {
		return a.svc.SyncAddressBook(ctx, 0)
	}, func(err error) {
		p.syncing = false
		if err != nil && !errors.Is(err, core.ErrNoAddressBooks) {
			a.toastErr(err)
		}
		p.avatars = nil
		p.load()
	})
}

// contactMailPage is how much of a contact's mail one read takes.
const contactMailPage = 50

// load reads the contacts and the route's contact's detail and mail, with
// none chosen the first contact's; for the contact shown, as much mail as
// was read before.
func (p *contactsPage) load() {
	a := p.a
	asked := a.route.Contact
	addr := asked
	want := 0
	if addr == p.detailOf {
		want = len(p.mail)
	} else if ct, idx := findContact(p.all, addr); idx >= 0 && ct.Address == p.detailOf {
		want = len(p.mail)
	}
	var (
		all    []core.Contact
		d      core.ContactDetail
		mail   []core.MessageSummary
		cursor string
	)
	p.read.Run(a, func(ctx context.Context) (err error) {
		if all, err = a.svc.Contacts(ctx); err != nil {
			return err
		}
		if addr != "" {
			if ct, idx := findContact(all, addr); idx >= 0 {
				addr = ct.Address
			}
		} else if len(all) > 0 {
			addr = all[0].Address
		}
		if addr == "" {
			return nil
		}
		if d, err = a.svc.ContactDetail(ctx, addr); err != nil {
			return err
		}
		for first := true; first || cursor != "" && len(mail) < want; first = false {
			page, err := a.svc.ContactMail(ctx, addr, cursor, contactMailPage)
			if err != nil {
				return err
			}
			mail, cursor = append(mail, page.Items...), page.NextCursor
		}
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if a.route.Page == PageContacts && a.route.Contact == asked {
			a.route.Contact = addr // first contact or secondary address resolved in place
		}
		if addr != p.detailOf {
			p.mailList = &ui.ListState{} // another's mail starts at its top
		}
		p.all, p.detail, p.detailOf, p.mail, p.cursor, p.more = all, d, addr, mail, cursor, false
	})
}

// loadMore reads the next page of the contact's mail, one read at a time.
func (p *contactsPage) loadMore() {
	if p.more || p.cursor == "" || p.read.loading {
		return
	}
	a := p.a
	addr, cursor := p.detailOf, p.cursor
	p.more = true
	var page core.MessagePage
	p.read.Also(a, func(ctx context.Context) (err error) {
		page, err = a.svc.ContactMail(ctx, addr, cursor, contactMailPage)
		return err
	}, func(err error) {
		p.more = false
		if err != nil {
			a.toastErr(err)
			p.cursor = "" // no more pages until a refresh
			return
		}
		p.mail, p.cursor = append(p.mail, page.Items...), page.NextCursor
	})
}

// choose shows addr beside the list, as a click on its row does.
func (p *contactsPage) choose(addr string) {
	if p.a.route.Contact != addr {
		p.a.route.Contact = addr
		p.load()
	}
}

// shown are the category's contacts matching the filter, most mail first.
func (p *contactsPage) shown() []core.Contact {
	q := strings.ToLower(strings.TrimSpace(p.query))
	return slices.DeleteFunc(slices.Clone(p.all), func(c core.Contact) bool {
		if !contactCats[p.cat].in(p.a, c) {
			return true
		}
		if q == "" {
			return false
		}
		return !strings.Contains(strings.ToLower(c.Address), q) &&
			!strings.Contains(strings.ToLower(c.Name), q) &&
			!strings.Contains(strings.ToLower(c.Subtitle), q) &&
			!slices.ContainsFunc(c.SearchTerms, func(s string) bool {
				return strings.Contains(strings.ToLower(s), q)
			}) &&
			!slices.ContainsFunc(c.Addresses, func(ea core.ContactAddress) bool {
				return strings.Contains(strings.ToLower(ea.Address), q)
			})
	})
}

// current is the route's contact, once loaded.
func (p *contactsPage) current() (core.Contact, bool) {
	ct, i := findContact(p.all, p.a.route.Contact)
	return ct, i >= 0
}

// Sidebar: the page switch and search, then the categories with their counts.
func (p *contactsPage) Sidebar(c *ui.Context) {
	t := theme.Of(c)
	p.a.sidebarHead(c)
	sidebarScroll(c, func() {
		for i, cat := range contactCats {
			n := 0
			for _, ct := range p.all {
				if cat.in(p.a, ct) {
					n++
				}
			}
			on := p.cat == i
			item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, OnClick: func() { p.cat = i }})
			item.Children(func() {
				kit.SidebarIcon(c, item, cat.icon, on)
				ui.Text(c, i18n.T("contacts.cat."+cat.key)).SingleLine().Grow(1).Shrink(1)
				if n > 0 {
					kit.Text(c, strconv.Itoa(n), t.Text.Xs).TextColor(t.Muted)
				}
			})
		}
	})
}

// Content is the list and the chosen contact, each a panel, as the mail page's.
func (p *contactsPage) Content(c *ui.Context) {
	a, th, r := p.a, c.Theme(), panelRadius(c)
	if !a.palette.open && !p.unsubDlg && c.Shortcut(ui.Cmd, ui.KeyR) {
		p.syncAddressBooks()
	}
	a.listW = a.listWidth()
	changed, _ := kit.Resizer(c, &a.listW, th.Space(2), false, i18n.T("mail.resize_list"), nil, func() {
		kit.Panel(c, r).Grow(1).Children(func() { p.listView(c) })
	}, func() {
		kit.Panel(c, r).Grow(1).Children(func() { p.detailView(c) })
	})
	a.listW = min(max(a.listW, theme.ListMinWidth), theme.ListMaxWidth)
	if changed {
		a.saveWidths()
	}
	if p.unsubDlg {
		id, u := p.unsubID, p.unsub
		unsubscribeDialog(c, &p.unsubDlg, u, func() { p.sendUnsubscribe(id, u) })
	}
}

// listView is the category's name and count, the filter, and the contacts.
func (p *contactsPage) listView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	rows := p.shown()
	ui.Row(c).AlignItems(ui.Center).Padding(th.Space(3), th.Space(3), th.Space(2), th.Space(4)).Gap(th.Space(2)).Children(func() {
		kit.Text(c, i18n.T("contacts.cat."+contactCats[p.cat].key)+" · "+strconv.Itoa(len(rows)), t.Text.Xs).FontWeight(500).TextColor(t.Muted)
		ui.Spacer(c)
		refreshLabel := i18n.T("contacts.refresh")
		if p.syncing {
			b := kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: true}).
				Width(th.Space(7)).Label(refreshLabel)
			kit.Tooltip(c, b, refreshLabel)
			b.Children(func() { kit.Spinner(c) })
		} else {
			kit.IconButton(c, "refresh-cw", refreshLabel, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(p.syncAddressBooks)
		}
	})
	box := kit.InputGroup(c, kit.InputOpts{}).Gap(th.Space(2)).Margin(0, th.Space(3), th.Space(2), th.Space(3)).Padding(0, th.Space(1), 0, th.Space(2.5))
	box.Children(func() {
		ui.Icon(c, icons.Get("search")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		kit.Input(c, &p.query, kit.InputOpts{Placeholder: i18n.T("contacts.filter"), Bare: true}).FocusRing(false).Grow(1).Label(i18n.T("contacts.filter"))
	})
	_, p.cur = findContact(rows, p.a.route.Contact)
	p.list.Key = func(i int) any { return rows[i].Address }
	p.list.Selected = &p.cur
	l := kit.List(c, p.list, len(rows), func(i int) { p.row(c, rows[i]) }).Grow(1).Padding(0, th.Space(2)).Gap(theme.Stroke)
	l.Children(func() {
		if len(rows) == 0 && !p.read.loading {
			msg := i18n.T("contacts.empty")
			if strings.TrimSpace(p.query) != "" {
				msg = i18n.T("contacts.no_match")
			}
			ui.Text(c, msg).TextColor(t.Muted).Padding(th.Space(3))
		}
	})
	if l.Changed() && p.cur >= 0 && p.cur < len(rows) {
		p.choose(rows[p.cur].Address)
	}
}

// row is a contact as the mail list shows a message: who and when, then the
// address and how much mail; one with unread mail in bold, with the gutter's
// dot.
func (p *contactsPage) row(c *ui.Context, ct core.Contact) ui.Element {
	t := theme.Of(c)
	th, small := c.Theme(), t.Text.Xs
	weight := 400
	if ct.Unread > 0 {
		weight = 600
	}
	name := cmp.Or(ct.Name, ct.Address)
	return ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center).Padding(th.Space(2.5), th.Space(2.5), th.Space(2.5), th.Space(5)).Children(func() {
		if ct.Unread > 0 {
			ui.Box(c).Absolute().Left(th.Space(1.5)).Top(th.Space(5.5)).Size(th.Space(2), th.Space(2)).Radius(th.Space(1)).Background(t.Unread).
				Label(i18n.Tf("mail.row.unread", ct.Unread))
		}
		kit.AvatarImage(c, name, p.avatar(ct))
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
			ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).FontWeight(weight).SingleLine().Shrink(1)
				if contactAllBlocked(p.a, ct) {
					kit.Badge(c, i18n.T("reader.blocked_badge"))
				}
				ui.Spacer(c)
				if !ct.Last.IsZero() {
					kit.Text(c, listTime(p.a.clock.Now(), ct.Last), small).TextColor(t.Muted).SingleLine().Shrink(0)
				}
			})
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				kit.Text(c, ct.Address, small).TextColor(t.Muted).SingleLine().Shrink(1)
				ui.Spacer(c)
				kit.Text(c, i18n.Tf("palette.contact.mails", ct.Messages), small).TextColor(t.Muted).SingleLine().Shrink(0) // the name and address give way first
			})
		})
	})
}

// detailView is the route's contact: who, the ways to clean up after them,
// what they add up to, then all their mail with the user, newest first, read
// on as it scrolls.
func (p *contactsPage) detailView(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	ct, ok := p.current()
	if !ok {
		if !p.read.loading {
			ui.Column(c).Grow(1).Center().Children(func() { ui.Text(c, i18n.T("contacts.empty")).TextColor(t.Muted) })
		}
		return
	}
	addrs := contactAddrs(ct)
	d, mail, name := p.detail, p.mail, cmp.Or(ct.Name, ct.Address)
	ready := p.detailOf != "" && (p.detailOf == ct.Address || slices.ContainsFunc(addrs, func(ea core.ContactAddress) bool {
		return ea.Address == p.detailOf
	})) // another's while this one's loads, or failed to
	if !ready {
		d, mail = core.ContactDetail{}, nil
	}
	blocked := contactAllBlocked(a, ct)
	// one column as wide as the reader's, centered: the head, then a row per message
	column := func() ui.Element {
		return ui.Column(c).FillWidth().MaxWidth(theme.ReaderMaxWidth).AlignSelf(ui.Center).Padding(0, th.Space(10))
	}
	p.mailList.Key = func(i int) any {
		if i == 0 {
			return "head"
		}
		return mail[i-1].ID
	}
	kit.List(c, p.mailList, 1+len(mail), func(i int) {
		if i > 0 {
			column().Children(func() { p.mailRow(c, mail[i-1]) })
			return
		}
		column().Padding(th.Space(10), th.Space(10), th.Space(1), th.Space(10)).Gap(th.Space(6)).Children(func() {
			// who, centered: the avatar, the name, subtitle and address, then the actions
			ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(4)).Children(func() {
				p.bigAvatar(c, ct, name)
				ui.Column(c).MaxWidth(theme.ReaderMaxWidth).AlignItems(ui.Center).Gap(th.Space(0.5)).Children(func() {
					kit.Text(c, name, t.Text.Xl).FontWeight(600).TextAlign(ui.Center)
					if ct.Subtitle != "" {
						kit.Text(c, ct.Subtitle, t.Text.Xs).TextColor(t.Muted).TextAlign(ui.Center)
					}
					p.addressLine(c, addrs)
					if ct.ListMessage != 0 || blocked {
						ui.Row(c).Wrap().Gap(th.Space(1.5)).Padding(th.Space(1.5), 0, 0, 0).Children(func() {
							if ct.ListMessage != 0 {
								kit.Badge(c, i18n.T("contacts.subscription"))
							}
							if blocked {
								kit.Badge(c, i18n.T("reader.blocked_badge"))
							}
						})
					}
				})
				ui.Row(c).Gap(th.Space(2)).Children(func() {
					if ct.ListMessage != 0 {
						kit.RoundAction(c, "mail-forbid", i18n.T("reader.unsubscribe"), "", false, false).OnClick(func() { p.unsubscribe(ct) })
					}
					if ct.ListMessage == 0 || ct.FromAddressBook {
						compose := kit.RoundAction(c, "square-pen", i18n.T("palette.action.compose"), "", false, false)
						if len(addrs) <= 1 {
							compose.OnClick(func() { a.composeTo(ct.Address) })
						} else {
							compose.Menu(func(m *ui.Menu) {
								for _, ea := range addrs {
									addr := ea.Address
									if m.Item(addr).Chosen() {
										a.composeTo(addr)
									}
								}
							})
						}
					}
					kit.RoundAction(c, "archive", i18n.T("contacts.archive_all"), archiveLabel(d, ready), d.Archivable == 0, false).OnClick(func() { p.archive(ct.Address) })
					short, long := i18n.T("contacts.block"), i18n.T("reader.block_sender")
					if blocked {
						short, long = i18n.T("contacts.unblock"), i18n.T("reader.unblock_sender")
					}
					kit.RoundAction(c, "spam", short, long, false, blocked).OnClick(func() { p.blockAll(addrs, !blocked) })
				})
			})
			// the counts in a row between hairlines, each centered
			ui.Column(c).FillWidth().Children(func() {
				kit.Separator(c, false)
				ui.Row(c).Padding(th.Space(3.5), 0).Children(func() {
					lastStat := "—"
					if !ct.Last.IsZero() {
						lastStat = listTime(a.clock.Now(), ct.Last)
					}
					for i, st := range [][2]string{
						{i18n.T("contacts.stat.received"), strconv.Itoa(ct.Received)},
						{i18n.T("contacts.stat.sent"), strconv.Itoa(ct.Sent)},
						{i18n.T("mail.filter.unread"), strconv.Itoa(ct.Unread)},
						{i18n.T("contacts.stat.last"), lastStat},
					} {
						if i > 0 {
							kit.Separator(c, true)
						}
						ui.Column(c).Grow(1).Basis(0).MinWidth(0).AlignItems(ui.Center).Gap(th.Space(0.5)).Padding(0, th.Space(3)).Children(func() {
							kit.Text(c, st[0], t.Text.Xs).TextColor(t.Muted).SingleLine()
							kit.Text(c, st[1], t.Text.Xl).FontWeight(600).SingleLine()
						})
					}
				})
				kit.Separator(c, false)
			})
			kit.Text(c, i18n.Tf("contacts.mail", ct.Messages), t.Text.Xs).FontWeight(500).TextColor(t.Muted).Padding(0, th.Space(3))
		})
	}).Grow(1)
	if _, last := p.mailList.Visible(); ready && last >= len(mail)-5 {
		p.loadMore()
	}
}

// addressLine is the contact's first address, a menu of what to do with it,
// and with more, +N: a native menu of them all, the blocked last, each a
// submenu of the same actions. One address or thirty, the head keeps one line.
func (p *contactsPage) addressLine(c *ui.Context, addrs []core.ContactAddress) {
	if len(addrs) == 0 {
		return
	}
	a := p.a
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		first := addrs[0].Address
		kit.Button(c, first, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).TextColor(t.Muted).
			Label(i18n.Tf("contacts.address_menu", first)).Menu(func(m *ui.Menu) { p.addressMenu(c, m, first, slices.Contains(a.blocked, first)) })
		if len(addrs) < 2 {
			return
		}
		more := len(addrs) - 1
		filterChip(c, "+"+strconv.Itoa(more), false, nil, nil).Label(i18n.Tf("contacts.addresses.more", more)).Menu(func(m *ui.Menu) {
			var groups [2][]core.ContactAddress // the others, then the blocked
			for _, ea := range addrs {
				i := 0
				if slices.Contains(a.blocked, ea.Address) {
					i = 1
				}
				groups[i] = append(groups[i], ea)
			}
			for gi, g := range groups {
				if len(g) == 0 {
					continue
				}
				title := i18n.T("contacts.addresses")
				if gi == 1 {
					m.Separator()
					title = i18n.T("reader.blocked_badge")
				}
				m.Item(title + " · " + strconv.Itoa(len(g))).Disabled(true)
				for _, ea := range g {
					addr, blocked := ea.Address, gi == 1
					it := m.Submenu(addr, func(m *ui.Menu) { p.addressMenu(c, m, addr, blocked) })
					if ea.Pref {
						it.Trailing(i18n.T("contacts.preferred"))
					}
				}
			}
		})
	})
}

// addressMenu is what can be done with one address: write, copy, block.
func (p *contactsPage) addressMenu(c *ui.Context, m *ui.Menu, addr string, blocked bool) {
	a := p.a
	if m.Item(i18n.T("palette.action.compose")).Icon(menuIcon("square-pen")).Chosen() {
		a.composeTo(addr)
	}
	if m.Item(i18n.T("contacts.copy_address")).Icon(menuIcon("copy")).Chosen() {
		c.WriteClipboard(addr)
		a.toast(i18n.T("contacts.address_copied"))
	}
	m.Separator()
	blk, icon := i18n.T("reader.block_sender"), "spam"
	if blocked {
		blk, icon = i18n.T("reader.unblock_sender"), "shield" // as the mail list's menu
	}
	if m.Item(blk).Icon(menuIcon(icon)).Danger().Chosen() {
		p.blockAddrs([]string{addr}, !blocked)
	}
}

// mailRow is a message between the contact and the user: its subject and
// start, whether the user starred or answered it, its folder and time; a
// click opens it in the reader.
func (p *contactsPage) mailRow(c *ui.Context, m core.MessageSummary) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	id := m.ID
	weight := 400
	if !slices.Contains(m.Flags, `\Seen`) {
		weight = 600
	}
	kit.Pressable(c, kit.PressableOpts{OnClick: func() { a.OpenMessage(id) }}).Label(m.Subject).Padding(th.Space(2.5), th.Space(3)).Gap(th.Space(3)).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
			ui.Text(c, cmp.Or(m.Subject, i18n.T("mail.no_subject"))).FontWeight(weight).SingleLine()
			if m.Snippet != "" {
				kit.Text(c, m.Snippet, t.Text.Xs).TextColor(t.Muted).SingleLine()
			}
		})
		switch {
		case slices.Contains(m.Flags, `\Flagged`):
			kit.Text(c, i18n.T("mail.unified.starred"), t.Text.Xs).TextColor(t.Muted)
		case slices.Contains(m.Flags, `\Answered`):
			kit.Text(c, i18n.T("contacts.replied"), t.Text.Xs).TextColor(t.Muted)
		}
		if f, ok := findFolder(a.mail.folders, m.FolderID); ok {
			kit.Badge(c, folderName(f))
		}
		kit.Text(c, listTime(a.clock.Now(), m.Date), t.Text.Xs).TextColor(t.Muted).Shrink(0)
	})
}

// archiveLabel names 全部归档 by what it does with the contact's inbox mail
// now: how many it moves and keeps, or why it moves none; while the detail
// is another's, just its name.
func archiveLabel(d core.ContactDetail, ready bool) string {
	all := i18n.T("contacts.archive_all")
	switch {
	case !ready:
		return all
	case d.Archivable > 0 && d.Kept > 0:
		return i18n.Tf("contacts.archive_n", d.Archivable) + " · " + i18n.Tf("contacts.archive.tip_kept", d.Kept)
	case d.Archivable > 0:
		return i18n.Tf("contacts.archive_n", d.Archivable) + " · " + i18n.T("contacts.archive.tip_undo")
	case d.Kept > 0:
		return all + " · " + i18n.Tf("contacts.archive.tip_all_kept", d.Kept)
	}
	return all + " · " + i18n.T("contacts.archive.tip_none")
}

// unsubscribe unsubscribes from the contact's newest list message by its
// headers, the reader's way: one-click asks first, a web page opens after
// the link confirmation, a mailto drafts.
func (p *contactsPage) unsubscribe(ct core.Contact) {
	a, id := p.a, ct.ListMessage
	var u core.Unsubscribe
	a.do(func(ctx context.Context) (err error) {
		u, err = a.svc.UnsubscribeMethod(ctx, id)
		return err
	}, func(err error) {
		switch {
		case err != nil:
			a.toastErr(err)
		case u.Method == core.UnsubscribeOneClick:
			p.unsub, p.unsubID, p.unsubDlg = u, id, true
		case u.Method == core.UnsubscribeWeb:
			a.openLink(u.URL)
		case u.Method == core.UnsubscribeMail:
			p.sendUnsubscribe(id, u)
		default:
			a.toastErr(core.ErrNoUnsubscribe)
		}
	})
}

// sendUnsubscribe has core unsubscribe from message id by u.
func (p *contactsPage) sendUnsubscribe(id int64, u core.Unsubscribe) {
	a := p.a
	var res core.UnsubscribeResult
	a.do(func(ctx context.Context) (err error) {
		res, err = a.svc.Unsubscribe(ctx, id)
		return err
	}, func(err error) { a.unsubscribed(u, res, err) })
}

// archive archives addr's inbox mail; its toast says how much moved and
// stayed, and offers to put back what moved, even when the move stopped
// part way.
func (p *contactsPage) archive(addr string) {
	a := p.a
	start := a.clock.Now()
	var res core.SenderArchive
	a.do(func(ctx context.Context) (err error) {
		res, err = a.svc.ArchiveSender(ctx, addr)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		}
		if err != nil && len(res.Moved) == 0 {
			return
		}
		toast := ui.Toast{ID: "archive-contact:" + addr, Title: i18n.Tf("contacts.archived", len(res.Moved))}
		if res.Skipped > 0 {
			toast.Description = i18n.Tf("contacts.archive.skipped", res.Skipped)
		}
		left := start.Add(core.UndoArchiveWindow).Sub(a.clock.Now())
		if len(res.Moved) > 0 && left > 0 {
			toast.Timeout = left
			toast.Action, toast.OnAction = i18n.T("compose.undo"), func() {
				a.do(func(ctx context.Context) error { return a.svc.UndoArchive(ctx, res) }, func(err error) {
					if err != nil {
						a.toastErr(err)
					}
				})
			}
		}
		a.toasts = append(a.toasts, toast)
	})
}

func (p *contactsPage) syncBlockedState() {
	for i := range p.all {
		all := len(p.all[i].Addresses) > 0
		for j := range p.all[i].Addresses {
			b := slices.Contains(p.a.blocked, p.all[i].Addresses[j].Address)
			p.all[i].Addresses[j].Blocked = b
			if !b {
				all = false
			}
		}
		if len(p.all[i].Addresses) == 0 {
			all = slices.Contains(p.a.blocked, p.all[i].Address)
		}
		p.all[i].Blocked = all
	}
}

func (p *contactsPage) blockAll(addrs []core.ContactAddress, on bool) {
	raw := make([]string, len(addrs))
	for i, ea := range addrs {
		raw[i] = ea.Address
	}
	p.blockAddrs(raw, on)
}

func (p *contactsPage) blockAddrs(addrs []string, on bool) {
	a := p.a
	a.do(func(ctx context.Context) error {
		for _, addr := range addrs {
			var err error
			if on {
				err = a.svc.BlockSender(ctx, addr)
			} else {
				err = a.svc.UnblockSender(ctx, addr)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		l := slices.Clone(a.blocked)
		for _, addr := range addrs {
			key := gomail.AddrKey(addr)
			l = slices.DeleteFunc(l, func(s string) bool { return s == key })
			if on {
				l = append(l, key)
			}
		}
		a.blocked, a.blockedVer = l, a.blockedVer+1
		p.syncBlockedState()
		msg := i18n.T("reader.sender_unblocked")
		if on {
			msg = i18n.T("reader.sender_blocked")
		}
		a.toast(msg)
	})
}

// contacts is the contacts page.
func (a *App) contacts() *contactsPage { return a.pages[PageContacts].(*contactsPage) }

// avatar is ct's avatar: an enabled address-book card's cached PHOTO first
// (even with zero received mail), else SenderAvatar of their newest message
// under the remote-image setting.
func (p *contactsPage) avatar(ct core.Contact) ui.ImageSource {
	a, addr := p.a, ct.Address
	if addr == "" {
		return nil
	}
	if p.avatarsVer != a.settingsSavedVer {
		p.avatars, p.avatarsVer = nil, a.settingsSavedVer
	}
	// keyed by the newest message too: a sent-only contact's first received mail brings a sender avatar
	key := addr + "\x00" + strconv.FormatInt(ct.Latest, 10)
	if av := p.avatars[key]; av != nil {
		return av.img
	}
	if a.settingsVer != a.settingsSavedVer {
		return nil // as the reader, none while settings are unsaved
	}
	if p.avatars == nil {
		p.avatars = map[string]*contactAvatar{}
	}
	av := &contactAvatar{}
	p.avatars[key] = av
	var img ui.ImageSource
	a.do(func(ctx context.Context) error {
		got, err := a.svc.ContactAvatar(ctx, addr)
		if err != nil || len(got.Data) == 0 {
			return err
		}
		img, err = avatarImage(got)
		return err
	}, func(error) {
		// shortcut: a failed read leaves the initial until the setting changes; retry if users miss avatars.
		av.img = img
	})
	return nil
}

// bigAvatar is the detail's avatar: ct's image, else their initial, 72 px.
func (p *contactsPage) bigAvatar(c *ui.Context, ct core.Contact, name string) {
	t, th := theme.Of(c), c.Theme()
	size := th.Space(18)
	if img := p.avatar(ct); img != nil {
		ui.Box(c).Size(size, size).Shrink(0).Radius(t.Radius.X2l).Clip().Background(t.MarkTile).Label(name).Children(func() {
			ui.Image(c, img).Size(size, size).Fit(ui.Cover)
		})
		return
	}
	kit.AvatarSized(c, name, size).Radius(t.Radius.X2l)
}

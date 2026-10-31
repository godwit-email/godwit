package app

import (
	"cmp"
	"context"
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
	cur      int // the row chosen, as the list keeps it; -1 none
	loading  bool
	gen      uint64 // bumped per load; older results are dropped

	unsub    core.Unsubscribe // a one-click unsubscribe from unsubID, waiting to be confirmed
	unsubID  int64
	unsubDlg bool

	avatars    map[int64]*contactAvatar // by the message read for, as settingsSavedVer was
	avatarsVer uint64
}

// contactAvatar is a contact's sender avatar once read; nil while it is read,
// or when there is none.
type contactAvatar struct{ img ui.ImageSource }

// contactCats are the sidebar's categories: every contact, those the user
// wrote to, those with a List-Unsubscribe, those blocked (as App.blocked).
var contactCats = []struct {
	key, icon string
	in        func(a *App, c core.Contact) bool
}{
	{"all", "label-team", func(*App, core.Contact) bool { return true }},
	{"people", "label-chat-3", func(_ *App, c core.Contact) bool { return c.Sent > 0 }},
	{"subscriptions", "label-newspaper", func(_ *App, c core.Contact) bool { return c.ListMessage != 0 }},
	{"blocked", "spam", func(a *App, c core.Contact) bool { return slices.Contains(a.blocked, c.Address) }},
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

func (p *contactsPage) refresh([]events.Event) { p.load() }

// contactMailPage is how much of a contact's mail one read takes.
const contactMailPage = 50

// load reads the contacts and the route's contact's detail and mail, with
// none chosen the first contact's; for the contact shown, as much mail as
// was read before.
func (p *contactsPage) load() {
	a := p.a
	p.gen++
	gen, addr := p.gen, a.route.Contact
	want := 0
	if addr == p.detailOf {
		want = len(p.mail)
	}
	p.loading = true
	var (
		all    []core.Contact
		d      core.ContactDetail
		mail   []core.MessageSummary
		cursor string
	)
	a.do(func(ctx context.Context) (err error) {
		if all, err = a.svc.Contacts(ctx); err != nil {
			return err
		}
		if addr == "" && len(all) > 0 {
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
		if gen != p.gen {
			return
		}
		p.loading = false
		if err != nil {
			a.toastErr(err)
			return
		}
		if a.route.Page == PageContacts && a.route.Contact == "" {
			a.route.Contact = addr // the first shown, not a step in the history
		}
		if addr != p.detailOf {
			p.mailList = &ui.ListState{} // another's mail starts at its top
		}
		p.all, p.detail, p.detailOf, p.mail, p.cursor, p.more = all, d, addr, mail, cursor, false
	})
}

// loadMore reads the next page of the contact's mail, one read at a time.
func (p *contactsPage) loadMore() {
	if p.more || p.cursor == "" || p.loading {
		return
	}
	a := p.a
	gen, addr, cursor := p.gen, p.detailOf, p.cursor
	p.more = true
	var page core.MessagePage
	a.do(func(ctx context.Context) (err error) {
		page, err = a.svc.ContactMail(ctx, addr, cursor, contactMailPage)
		return err
	}, func(err error) {
		if gen != p.gen {
			return
		}
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
		return !contactCats[p.cat].in(p.a, c) || q != "" && !strings.Contains(c.Address, q) && !strings.Contains(strings.ToLower(c.Name), q)
	})
}

// current is the route's contact, once loaded.
func (p *contactsPage) current() (core.Contact, bool) {
	i := slices.IndexFunc(p.all, func(c core.Contact) bool { return c.Address == p.a.route.Contact })
	if i < 0 {
		return core.Contact{}, false
	}
	return p.all[i], true
}

// Sidebar: the page switch and search, then the categories with their counts.
func (p *contactsPage) Sidebar(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
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
					kit.Text(c, strconv.Itoa(n), t.Text.Sm).TextColor(t.Muted)
				}
			})
		}
		kit.Text(c, i18n.T("contacts.note"), t.Text.Sm).TextColor(t.Muted).Padding(th.Space(3), th.Space(2), 0, th.Space(2))
	})
}

// Content is the list and the chosen contact, each a panel, as the mail page's.
func (p *contactsPage) Content(c *ui.Context) {
	a, th, r := p.a, c.Theme(), panelRadius(c)
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
	kit.Text(c, i18n.T("contacts.cat."+contactCats[p.cat].key)+" · "+strconv.Itoa(len(rows)), t.Text.Base).FontWeight(500).TextColor(t.Muted).
		Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	box := kit.InputGroup(c, kit.InputOpts{}).Gap(th.Space(2)).Margin(0, th.Space(3), th.Space(2), th.Space(3)).Padding(0, th.Space(1), 0, th.Space(2.5)).Radius(t.Radius.Lg)
	box.Children(func() {
		ui.Icon(c, icons.Get("search")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		kit.Input(c, &p.query, kit.InputOpts{Placeholder: i18n.T("contacts.filter"), Bare: true}).FocusRing(false).Grow(1).Label(i18n.T("contacts.filter"))
	})
	p.cur = slices.IndexFunc(rows, func(ct core.Contact) bool { return ct.Address == p.a.route.Contact })
	p.list.Key = func(i int) any { return rows[i].Address }
	p.list.Selected = &p.cur
	l := kit.List(c, p.list, len(rows), func(i int) { p.row(c, rows[i]) }).Grow(1).Padding(0, th.Space(2)).Gap(theme.Stroke)
	l.Children(func() {
		if len(rows) == 0 && !p.loading {
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
	th, small := c.Theme(), t.Text.Sm
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
				if slices.Contains(p.a.blocked, ct.Address) {
					kit.Badge(c, i18n.T("reader.blocked_badge"))
				}
				ui.Spacer(c)
				kit.Text(c, listTime(p.a.clock.Now(), ct.Last), small).TextColor(t.Muted).SingleLine().Shrink(0)
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
		if !p.loading {
			ui.Column(c).Grow(1).Center().Children(func() { ui.Text(c, i18n.T("contacts.empty")).TextColor(t.Muted) })
		}
		return
	}
	d, mail, name := p.detail, p.mail, cmp.Or(ct.Name, ct.Address)
	ready := p.detailOf == ct.Address // another's while this one's loads, or failed to
	if !ready {
		d, mail = core.ContactDetail{}, nil
	}
	blocked := slices.Contains(a.blocked, ct.Address)
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
		column().Padding(th.Space(9), th.Space(10), th.Space(1), th.Space(10)).Gap(th.Space(6)).Children(func() {
			ui.Row(c).Gap(th.Space(4)).AlignItems(ui.Center).Children(func() {
				p.bigAvatar(c, ct, name)
				ui.Column(c).Shrink(1).MinWidth(0).Gap(th.Space(1)).Children(func() {
					ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						kit.Text(c, name, t.Text.X2l).FontWeight(600)
						if ct.ListMessage != 0 {
							kit.Badge(c, i18n.T("contacts.subscription"))
						}
						if blocked {
							kit.Badge(c, i18n.T("reader.blocked_badge"))
						}
					})
					kit.Text(c, ct.Address, t.Text.Base).TextColor(t.Muted)
				})
			})
			ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
				outline := kit.ButtonOpts{Variant: kit.Outline}
				if ct.ListMessage != 0 {
					outline.Icon = "mail-forbid"
					kit.Button(c, i18n.T("reader.unsubscribe"), outline).OnClick(func() { p.unsubscribe(ct) })
				} else {
					outline.Icon = "square-pen"
					kit.Button(c, i18n.T("palette.action.compose"), outline).OnClick(func() { a.composeTo(ct.Address) })
				}
				outline.Icon, outline.Disabled = "archive", d.Archivable == 0
				label := i18n.T("contacts.archive_all")
				if d.Archivable > 0 {
					label = i18n.Tf("contacts.archive_n", d.Archivable)
				}
				arch := kit.Button(c, label, outline).OnClick(func() { p.archive(ct.Address) })
				if ready {
					kit.Tooltip(c, arch, archiveTip(d))
				}
				ghost := kit.ButtonOpts{Variant: kit.Ghost, Icon: "spam"}
				blockLabel := i18n.T("reader.block_sender")
				if blocked {
					blockLabel = i18n.T("reader.unblock_sender")
				}
				kit.Button(c, blockLabel, ghost).OnClick(func() { p.block(ct.Address, !blocked) })
			})
			ui.Row(c).Gap(th.Space(4)).Padding(th.Space(4), th.Space(5)).Radius(t.Radius.Xl).Background(t.Sidebar).Border(theme.Hairline, t.Border).Children(func() {
				for _, st := range []struct {
					label, value string
					date         bool
				}{
					{i18n.T("contacts.stat.received"), strconv.Itoa(ct.Received), false},
					{i18n.T("contacts.stat.sent"), strconv.Itoa(ct.Sent), false},
					{i18n.T("mail.filter.unread"), strconv.Itoa(ct.Unread), false},
					{i18n.T("contacts.stat.last"), listTime(a.clock.Now(), ct.Last), true},
				} {
					ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
						kit.Text(c, st.label, t.Text.Sm).TextColor(t.Muted)
						size := t.Text.Xl
						if st.date {
							size = t.Text.Body
						}
						kit.Text(c, st.value, size).FontWeight(600).SingleLine()
					})
				}
			})
			kit.Text(c, i18n.Tf("contacts.mail", ct.Messages), t.Text.Base).FontWeight(500).TextColor(t.Muted).Padding(0, th.Space(3))
		})
	}).Grow(1)
	if _, last := p.mailList.Visible(); ready && last >= len(mail)-5 {
		p.loadMore()
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
				kit.Text(c, m.Snippet, t.Text.Base).TextColor(t.Muted).SingleLine()
			}
		})
		switch {
		case slices.Contains(m.Flags, `\Flagged`):
			kit.Text(c, i18n.T("mail.unified.starred"), t.Text.Sm).TextColor(t.Muted)
		case slices.Contains(m.Flags, `\Answered`):
			kit.Text(c, i18n.T("contacts.replied"), t.Text.Sm).TextColor(t.Muted)
		}
		if f, ok := findFolder(a.mail.folders, m.FolderID); ok {
			kit.Badge(c, folderName(f))
		}
		kit.Text(c, listTime(a.clock.Now(), m.Date), t.Text.Sm).TextColor(t.Muted).Shrink(0)
	})
}

// archiveTip says what 全部归档 does with the contact's inbox mail now.
func archiveTip(d core.ContactDetail) string {
	switch {
	case d.Archivable > 0 && d.Kept > 0:
		return i18n.Tf("contacts.archive.tip_kept", d.Kept)
	case d.Archivable > 0:
		return i18n.T("contacts.archive.tip_undo")
	case d.Kept > 0:
		return i18n.Tf("contacts.archive.tip_all_kept", d.Kept)
	}
	return i18n.T("contacts.archive.tip_none")
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
		if len(res.Moved) > 0 {
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

// block blocks or unblocks addr, with a toast saying so.
func (p *contactsPage) block(addr string, on bool) {
	msg := i18n.T("reader.sender_unblocked")
	if on {
		msg = i18n.T("reader.sender_blocked")
	}
	p.a.setBlocked(addr, on, func() { p.a.toast(msg) })
}

// contacts is the contacts page.
func (a *App) contacts() *contactsPage { return a.pages[PageContacts].(*contactsPage) }

// avatar is ct's sender avatar the reader's way: core's SenderAvatar of their
// latest message, under the remote-image setting, read off the UI thread the
// first time it shows and again once that setting is saved changed; nil until
// read, or for none (one only written to has none).
func (p *contactsPage) avatar(ct core.Contact) ui.ImageSource {
	a, id := p.a, ct.Latest
	if id == 0 {
		return nil
	}
	if p.avatarsVer != a.settingsSavedVer {
		p.avatars, p.avatarsVer = nil, a.settingsSavedVer
	}
	if av := p.avatars[id]; av != nil {
		return av.img
	}
	if a.settingsVer != a.settingsSavedVer {
		return nil // as the reader, none while settings are unsaved
	}
	if p.avatars == nil {
		p.avatars = map[int64]*contactAvatar{}
	}
	av := &contactAvatar{}
	p.avatars[id] = av
	var img ui.ImageSource
	a.do(func(ctx context.Context) error {
		got, err := a.svc.SenderAvatar(ctx, id, false)
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

// bigAvatar is the detail's avatar: ct's image, else their initial, 56 px.
func (p *contactsPage) bigAvatar(c *ui.Context, ct core.Contact, name string) {
	t, th := theme.Of(c), c.Theme()
	size := th.Space(14)
	if img := p.avatar(ct); img != nil {
		ui.Box(c).Size(size, size).Shrink(0).Radius(t.Radius.Xl).Clip().Label(name).Children(func() {
			ui.Image(c, img).Size(size, size).Fit(ui.Cover)
		})
		return
	}
	kit.Avatar(c, name).Size(size, size).Radius(t.Radius.Xl)
}

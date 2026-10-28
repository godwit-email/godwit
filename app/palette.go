package app

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// paletteKinds are the palette's kind tabs, as core.FindQuery.Kind names
// them: all, then one kind each.
var paletteKinds = []string{"", "mail", "conversation", "contact", "attachment"}

// paletteFew is how many hits of each kind 全部 shows.
const paletteFew = 3

// palette is the ⌘K search panel over any page: Find's hits by kind and the
// app's commands. Only the newest search's answer shows.
type palette struct {
	a     *App
	open  bool
	query string
	kind  int // into paletteKinds

	// asked and askedKind are what the latest search sent; started, that
	// one was sent since the palette opened.
	asked     string
	askedKind int
	started   bool
	gen       uint64
	res       core.FindResult
	hl        string // the text res answers
	hf        bool   // res answers filters
	err       error

	filters   []palFilter // the chips, in the order chosen
	filtering bool        // / shows the filter list
	fsel      int         // into filterOpts
	typed     string      // query as the last frame had it, to tell a / typed

	sel    int       // into rows
	hover  int       // the row the pointer was over last frame, -1 for none
	scroll bool      // the arrows moved sel: bring it into view
	rows   []palItem // as last built, which the keys act on

	asking bool // Tab made the box a question to Wit

	m     paletteMenu        // ⌥↵'s
	cards map[int64]*palCard // by message id, loaded as each is chosen
}

// palItem is a row of the palette: what it shows and what choosing it does.
type palItem struct {
	icon, title, sub string
	meta             string // in place of when, as a contact's mail count
	kbd              string // its key, after the rest
	when             time.Time
	unread           bool
	open             func()
	msg              *core.MessageSummary // a mail's, for its preview card
	acts             func() []palAct      // its ⌥↵ menu; nil for none
}

// palGroup is a heading and its rows; total, when not 0, is how many there
// are in all.
type palGroup struct {
	title string
	total int
	items []palItem
}

// openPalette shows the palette afresh, as ⌘K, ⌘F and the search buttons do.
func (a *App) openPalette() {
	// gen carries on: an answer still on its way from before must not match a new search
	a.palette = palette{a: a, open: true, hover: -1, gen: a.palette.gen}
}

// view builds the palette (closed, it fades out), searching again when the
// query or the kind changed.
func (p *palette) view(c *ui.Context) {
	if p.a == nil {
		return // never opened
	}
	if !p.asking && slashed(p.typed, p.query) {
		p.query, p.filtering, p.fsel = p.typed, true, 0
	}
	p.typed = p.query
	if p.open && !p.asking && (!p.started || p.query != p.asked || p.kind != p.askedKind) {
		p.find()
	}
	t, th := theme.Of(c), c.Theme()
	groups := p.groups()
	p.rows = p.rows[:0]
	for _, g := range groups {
		p.rows = append(p.rows, g.items...)
	}
	p.sel = min(max(p.sel, 0), max(len(p.rows)-1, 0))
	p.m.acts = p.menuActs()
	hover := -1
	o := kit.PaletteOpts{
		Label: i18n.T("palette.label"), Placeholder: i18n.T("palette.placeholder"), Close: i18n.T("common.close"),
		OnKey: p.key, Chips: func() { p.chips(c) },
		Hints: []kit.PaletteHint{
			{Label: i18n.T("palette.hint.close"), Key: "Esc"},
			{Label: i18n.T("palette.hint.kind"), Key: "←→"},
			{Label: i18n.T("palette.hint.filter"), Key: "/"},
			{Label: i18n.T("palette.hint.actions"), Key: "⌥↵"},
			{Label: i18n.T("palette.hint.wit"), Key: "Tab", OnClick: p.toggleAsk},
		},
	}
	if p.asking {
		o.Label, o.Placeholder, o.Send, o.OnSend, o.OnKey, o.Chips = i18n.T("wit.input"), i18n.T("wit.placeholder"), i18n.T("wit.send"), p.askWit, p.askKey, nil
		o.Hints = []kit.PaletteHint{
			{Label: i18n.T("compose.send"), Key: "↵"},
			{Label: i18n.T("mail.search"), Key: "Tab", OnClick: p.toggleAsk},
		}
	}
	if p.m.open {
		o.Keys = p.menuShortcut
	}
	kit.Palette(c, &p.open, &p.query, o, func() {
		for i, k := range paletteKinds {
			kit.PaletteTab(c, kindLabel(k), i == p.kind).OnClick(func() { p.kind = i })
		}
	}, func() {
		switch {
		case p.filtering:
			p.filterList(c)
			return
		case p.err != nil:
			kit.Text(c, humanize(p.err), t.Text.Sm).TextColor(t.Danger).Padding(th.Space(2))
			return
		case len(p.rows) == 0 && p.hl != "":
			ui.Column(c).Gap(th.Space(1)).Padding(th.Space(6), th.Space(4)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.Tf("palette.empty.title", p.hl)).FontWeight(500)
				kit.Text(c, i18n.T("palette.empty.desc"), t.Text.Sm).TextColor(t.Muted)
			})
			return
		case len(p.rows) == 0 && p.hf:
			kit.Text(c, i18n.T("mail.search.empty_filtered"), t.Text.Sm).TextColor(t.Muted).Padding(th.Space(6), th.Space(4)).AlignSelf(ui.Center)
			return
		}
		i := 0
		for _, g := range groups {
			head := g.title
			if g.total > 0 {
				head += " · " + strconv.Itoa(g.total)
			}
			kit.Text(c, head, t.Text.Xs).FontWeight(500).TextColor(t.Muted).Padding(th.Space(2), th.Space(2.5), th.Space(1), th.Space(2.5))
			for _, it := range g.items {
				if p.row(c, i, it).Hovered() {
					hover = i
				}
				i++
			}
		}
	})
	if p.open && !p.asking && !p.m.open && p.sel < len(p.rows) && p.rows[p.sel].msg != nil { // the menu covers the card
		m := *p.rows[p.sel].msg
		kit.PalettePreview(c, func() { p.preview(c, m) })
	}
	if hover >= 0 && hover != p.hover && !p.m.open { // the menu stays on its row
		p.sel = hover // the pointer moved onto a row; keys move it from there
	}
	p.hover, p.scroll = hover, false
	p.menuFrame(c)
}

// row is rows[i], one line: its icon, title, the muted line after it, an
// unread dot and the time; selected, the menu's button and ↵ in its place.
func (p *palette) row(c *ui.Context, i int, it palItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	r := kit.PaletteRow(c, i == p.sel).Label(it.title).Gap(th.Space(2.5)).OnClick(func() { p.choose(i) })
	if i == p.sel && p.scroll {
		r.ScrollIntoView()
	}
	r.Children(func() {
		ui.Icon(c, icons.Get(it.icon)).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		marked(c, it.title, p.hl).SingleLine().Shrink(1)
		if it.sub != "" {
			marked(c, it.sub, p.hl).FontSize(t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		if it.unread {
			ui.Box(c).Shrink(0).Size(th.Space(2), th.Space(2)).Radius(th.Space(1)).Background(t.Unread).Label(i18n.Tf("mail.row.unread", 1))
		}
		if i == p.sel { // what the keys do in place of the time
			if it.acts != nil {
				kit.IconButton(c, "ellipsis", i18n.T("palette.more"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(p.openMenu)
			}
			kit.Kbd(c, "↵").Shrink(0)
			return
		}
		meta := it.meta
		if meta == "" && !it.when.IsZero() {
			meta = listTime(p.a.clock.Now(), it.when)
		}
		if meta != "" {
			kit.Text(c, meta, t.Text.Xs).TextColor(t.Muted).Shrink(0)
		}
		if it.kbd != "" {
			kit.Kbd(c, it.kbd).Shrink(0)
		}
	})
	if i == p.sel && p.m.open {
		p.menuView(c, r)
	}
	return r
}

// key is the search box's: ↑↓ move through every group and wrap, ↵ opens,
// ←→ change the kind while the box is empty, ⌃Tab and ⌃⇧Tab any time; Tab
// asks Wit, ⌘↵ shows every mail hit in the list.
func (p *palette) key(k ui.TextKey) bool {
	if p.m.open && p.menuKey(k) {
		return true
	}
	if p.filtering && p.filterKey(k) {
		return true
	}
	n := len(p.rows)
	switch {
	case k.Mods == 0 && k.Key == ui.KeyBackspace && k.Text() == "" && len(p.filters) > 0:
		p.removeFilter(len(p.filters) - 1)
	case k.Mods == 0 && (k.Key == ui.KeyDown || k.Key == ui.KeyUp):
		if n > 0 {
			p.sel, p.scroll = wrapStep(p.sel, n, k.Key == ui.KeyUp), true
		}
	case k.Mods == 0 && k.Key == ui.KeyEnter:
		p.choose(p.sel)
	case k.Mods == ui.Alt && k.Key == ui.KeyEnter:
		p.openMenu()
	case k.Mods == 0 && (k.Key == ui.KeyLeft || k.Key == ui.KeyRight) && k.Text() == "":
		p.step(k.Key == ui.KeyRight)
	case k.Mods == 0 && k.Key == ui.KeyTab:
		p.toggleAsk()
	case k.Mods == ui.Cmd && k.Key == ui.KeyEnter:
		p.viewAll()
	case k.Key == ui.KeyTab && (k.Mods == ui.Ctrl || k.Mods == ui.Ctrl|ui.Shift):
		p.step(k.Mods == ui.Ctrl)
	default:
		return false
	}
	return true
}

// wrapStep is the index after i of n, or before it when up, wrapping, as
// ↑↓ move in the palette's lists.
func wrapStep(i, n int, up bool) int {
	if up {
		return (i + n - 1) % n
	}
	return (i + 1) % n
}

// step moves to the next kind tab, or the one before, wrapping.
func (p *palette) step(forth bool) {
	p.kind = wrapStep(p.kind, len(paletteKinds), !forth)
}

// choose opens rows[i] and closes the palette.
func (p *palette) choose(i int) {
	if i < 0 || i >= len(p.rows) {
		return
	}
	open := p.rows[i].open
	p.open = false
	open()
}

// find asks core for the query's hits of the kind: a few of each in 全部,
// each kind's first page in its own tab.
func (p *palette) find() {
	a := p.a
	p.gen++
	p.asked, p.askedKind, p.started, p.sel = p.query, p.kind, true, 0
	p.m.open = false
	sf := p.sf()
	q := core.FindQuery{SearchQuery: sf.query(a.clock.Now(), sf.acc, p.query), Kind: paletteKinds[p.kind]}
	// shortcut: a kind's tab shows at most core's cap of 200 hits; page it
	// (or link 查看全部 for every kind) once people dig past that.
	q.Limit = 200
	if q.Kind == "" {
		q.Limit = paletteFew
	}
	gen := p.gen
	var res core.FindResult
	a.do(func(ctx context.Context) (err error) {
		res, err = a.svc.Find(ctx, q)
		return err
	}, func(err error) {
		if gen != p.gen {
			return
		}
		p.res, p.err, p.hl, p.hf = res, err, q.Text, q.Filtered()
		p.m.open, p.m.labeling = false, false // a menu opened on the old rows would act on another hit
	})
}

// groups are the rows by kind: with nothing typed, 全部 has the recent
// mail, conversations and attachments together, then the commands.
func (p *palette) groups() []palGroup {
	r, kind := p.res, paletteKinds[p.kind]
	mail, convs, contacts, atts := p.mailItems(r.Mail.Items), p.convItems(r.Conversations.Items), p.contactItems(r.Contacts.Items), p.attachmentItems(r.Attachments.Items)
	var out []palGroup
	add := func(g palGroup) {
		if len(g.items) > 0 {
			out = append(out, g)
		}
	}
	if kind == "" && p.hl == "" && !p.hf {
		recent := slices.Concat(mail, convs, atts)
		slices.SortStableFunc(recent, func(a, b palItem) int { return b.when.Compare(a.when) })
		add(palGroup{title: i18n.T("palette.recent"), items: recent})
		add(palGroup{title: i18n.T("palette.actions"), items: p.actions()})
		return out
	}
	total := func(n int) int {
		if p.hl == "" && !p.hf {
			return 0 // a kind's recent hits: no count
		}
		return n
	}
	if (p.hl != "" || p.hf) && r.Mail.Total > len(mail) {
		mail = append(mail, p.viewAllItem(r.Mail.Total))
	}
	add(palGroup{kindLabel("mail"), total(r.Mail.Total), mail})
	add(palGroup{kindLabel("conversation"), total(r.Conversations.Total), convs})
	add(palGroup{kindLabel("contact"), total(r.Contacts.Total), contacts})
	add(palGroup{kindLabel("attachment"), total(r.Attachments.Total), atts})
	if kind == "" && !p.hf {
		acts := p.actions()
		add(palGroup{i18n.T("palette.actions"), len(acts), acts})
	}
	return out
}

func kindLabel(kind string) string { return i18n.T("palette.kind." + cmp.Or(kind, "all")) }

func (p *palette) mailItems(ms []core.MessageSummary) []palItem {
	out := make([]palItem, 0, len(ms))
	mp := p.a.mail
	for _, m := range ms {
		it := messageItem(m)
		sub := mp.resultWho(it)
		// The folder, but the inbox's, and the account when there are more.
		if f, ok := mp.folder(m.FolderID); ok && (f.Role != "inbox" || len(p.a.accounts) > 1) {
			sub += " · " + mp.folderLine(it)
		}
		thread, id := m.ThreadID, m.ID
		out = append(out, palItem{icon: "mail", title: cmp.Or(m.Subject, i18n.T("mail.no_subject")), sub: sub, when: m.Date, unread: it.Unread > 0, msg: &m,
			open: func() {
				p.a.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}, ThreadID: thread, MessageID: id})
			}, acts: p.mailActs(m, it.Unread > 0)})
	}
	return out
}

func (p *palette) convItems(cs []core.Conversation) []palItem {
	out := make([]palItem, 0, len(cs))
	for _, cv := range cs {
		id := cv.ID
		open := func() { p.a.navigate(Route{Page: PageWit, ConversationID: id}) }
		out = append(out, palItem{icon: "label-chat-3", title: cmp.Or(cv.Title, i18n.T("ask.new")), when: cv.UpdatedAt,
			open: open, acts: p.convActs(id, open)})
	}
	return out
}

func (p *palette) contactItems(cs []core.Contact) []palItem {
	out := make([]palItem, 0, len(cs))
	for _, ct := range cs {
		addr := ct.Address
		open := func() { p.a.mail.mailFrom(addr) }
		out = append(out, palItem{icon: "user", title: cmp.Or(ct.Name, addr), sub: addr, when: ct.Last, meta: i18n.Tf("palette.contact.mails", ct.Messages),
			open: open, acts: p.contactActs(addr, open)})
	}
	return out
}

func (p *palette) attachmentItems(as []core.FoundAttachment) []palItem {
	out := make([]palItem, 0, len(as))
	for _, at := range as {
		id := at.MessageID
		open := func() { p.a.OpenMessage(id) }
		out = append(out, palItem{icon: "paperclip", title: at.Filename, sub: displayName(at.From) + " · " + at.Subject, when: at.Date,
			open: open, acts: p.attachmentActs(at.AttachmentInfo, open)})
	}
	return out
}

// actions are the app's commands whose names hold the query.
func (p *palette) actions() []palItem {
	a := p.a
	all := []palItem{
		{icon: "square-pen", title: i18n.T("palette.action.compose"), open: a.Compose},
		{icon: "sparkles", title: i18n.T("palette.action.new_conversation"), open: a.NewConversation},
		{icon: "file-pen", title: i18n.T("palette.action.drafts"), open: func() { a.navigate(Route{Page: PageMail, View: a.mail.firstDrafts()}) }},
		{icon: "tag", title: i18n.T("palette.action.labels"), open: func() { a.navigate(Route{Page: PageSettings, Settings: "labels"}) }},
		{icon: "plus", title: i18n.T("palette.action.add_account"), open: a.openAddAccount},
		{icon: "settings", title: i18n.T("palette.action.settings"), open: a.OpenSettings},
	}
	q := []rune(strings.TrimSpace(p.query))
	return slices.DeleteFunc(all, func(it palItem) bool {
		for i := range it.title {
			if matchAt(it.title[i:], q) > 0 {
				return false
			}
		}
		return len(q) > 0
	})
}

// firstDrafts is the first account's drafts, the local drafts where it has
// no folder for them.
func (p *mailPage) firstDrafts() core.View {
	if len(p.a.accounts) == 0 {
		return core.View{Unified: "inbox"}
	}
	acc := p.a.accounts[0].ID
	for _, f := range p.folders[acc] {
		if f.Role == "drafts" {
			return core.View{FolderID: f.ID}
		}
	}
	return localDrafts(acc)
}

// mailFrom lists the synced mail from addr in every account: the list's
// search, filtered by sender.
func (p *mailPage) mailFrom(addr string) {
	p.searchFor("", searchFilters{from: addr, fromExact: true, accSet: true})
}

// searchFor shows the mail page and opens the list's search afresh on query
// and filters sf, in the view left there unless it draws no search (Drafts,
// the Outbox, none yet), where it is the unified inbox.
func (p *mailPage) searchFor(query string, sf searchFilters) {
	v := p.view
	if _, drafts := draftsOf(p.folders, v); drafts || v.Unified == "outbox" || v == (core.View{}) {
		v = core.View{Unified: "inbox"}
	}
	p.a.navigate(Route{Page: PageMail, View: v})
	p.closeSearch()
	p.searchOpen, p.query, p.sf = true, query, sf
	p.runSearch()
}

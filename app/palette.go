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
	err       error

	sel    int       // into rows
	hover  int       // the row the pointer was over last frame, -1 for none
	scroll bool      // the arrows moved sel: bring it into view
	rows   []palItem // as last built, which the keys act on
}

// palItem is a row of the palette: what it shows and what choosing it does.
type palItem struct {
	icon, title, sub string
	when             time.Time
	unread           bool
	open             func()
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
	a.palette = palette{a: a, open: true, hover: -1}
}

// view builds the palette (closed, it fades out), searching again when the
// query or the kind changed.
func (p *palette) view(c *ui.Context) {
	if p.a == nil {
		return // never opened
	}
	if p.open && (!p.started || p.query != p.asked || p.kind != p.askedKind) {
		p.find()
	}
	t, th := theme.Of(c), c.Theme()
	groups := p.groups()
	p.rows = p.rows[:0]
	for _, g := range groups {
		p.rows = append(p.rows, g.items...)
	}
	p.sel = min(max(p.sel, 0), max(len(p.rows)-1, 0))
	hover := -1
	o := kit.PaletteOpts{
		Label: i18n.T("palette.label"), Placeholder: i18n.T("palette.placeholder"), Close: i18n.T("common.close"),
		OnKey: p.key, Glass: p.a.vibrancy,
		Hints: []kit.PaletteHint{
			{Label: i18n.T("palette.hint.open"), Key: "↵"},
			{Label: i18n.T("palette.hint.kind"), Key: "←→"},
			{Label: i18n.T("palette.hint.close"), Key: "Esc"},
		},
	}
	kit.Palette(c, &p.open, &p.query, o, func() {
		for i, k := range paletteKinds {
			kit.PaletteTab(c, kindLabel(k), i == p.kind).OnClick(func() { p.kind = i })
		}
	}, func() {
		switch {
		case p.err != nil:
			kit.Text(c, humanize(p.err), t.Text.Sm).TextColor(t.Danger).Padding(th.Space(2))
			return
		case len(p.rows) == 0 && p.hl != "":
			ui.Column(c).Gap(th.Space(1)).Padding(th.Space(6), th.Space(4)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.Tf("palette.empty.title", p.hl)).FontWeight(500)
				kit.Text(c, i18n.T("palette.empty.desc"), t.Text.Sm).TextColor(t.Muted)
			})
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
	if hover >= 0 && hover != p.hover {
		p.sel = hover // the pointer moved onto a row; keys move it from there
	}
	p.hover, p.scroll = hover, false
}

// row is rows[i], one line: its icon, title, the muted line after it, the
// time and an unread dot.
func (p *palette) row(c *ui.Context, i int, it palItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	r := kit.PaletteRow(c, i == p.sel).Label(it.title).Gap(th.Space(2.5)).OnClick(func() { p.choose(i) })
	if i == p.sel && p.scroll {
		r.ScrollIntoView()
	}
	return r.Children(func() {
		ui.Icon(c, icons.Get(it.icon)).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		marked(c, it.title, p.hl).SingleLine().Shrink(1)
		if it.sub != "" {
			marked(c, it.sub, p.hl).FontSize(t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		if !it.when.IsZero() {
			kit.Text(c, listTime(p.a.clock.Now(), it.when), t.Text.Xs).TextColor(t.Muted).Shrink(0)
		}
		if it.unread {
			ui.Box(c).Shrink(0).Size(th.Space(2), th.Space(2)).Radius(th.Space(1)).Background(t.Unread).Label(i18n.Tf("mail.row.unread", 1))
		}
	})
}

// key is the search box's: ↑↓ move through every group and wrap, ↵ opens,
// ←→ change the kind while the box is empty, ⌃Tab and ⌃⇧Tab any time.
func (p *palette) key(k ui.TextKey) bool {
	n := len(p.rows)
	switch {
	case k.Mods == 0 && (k.Key == ui.KeyDown || k.Key == ui.KeyUp):
		if n > 0 {
			step := 1
			if k.Key == ui.KeyUp {
				step = n - 1
			}
			p.sel, p.scroll = (p.sel+step)%n, true
		}
	case k.Mods == 0 && k.Key == ui.KeyEnter:
		p.choose(p.sel)
	case k.Mods == 0 && (k.Key == ui.KeyLeft || k.Key == ui.KeyRight) && k.Text() == "":
		p.step(k.Key == ui.KeyRight)
	case k.Key == ui.KeyTab && (k.Mods == ui.Ctrl || k.Mods == ui.Ctrl|ui.Shift):
		p.step(k.Mods == ui.Ctrl)
	default:
		return false
	}
	return true
}

// step moves to the next kind tab, or the one before, wrapping.
func (p *palette) step(forth bool) {
	n := len(paletteKinds)
	if forth {
		p.kind = (p.kind + 1) % n
	} else {
		p.kind = (p.kind + n - 1) % n
	}
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
	q := core.FindQuery{SearchQuery: core.SearchQuery{Text: strings.TrimSpace(p.query)}, Kind: paletteKinds[p.kind]}
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
		p.res, p.err, p.hl = res, err, q.Text
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
	if kind == "" && p.hl == "" {
		recent := slices.Concat(mail, convs, atts)
		slices.SortStableFunc(recent, func(a, b palItem) int { return b.when.Compare(a.when) })
		add(palGroup{title: i18n.T("palette.recent"), items: recent})
		add(palGroup{title: i18n.T("palette.actions"), items: p.actions()})
		return out
	}
	total := func(n int) int {
		if p.hl == "" {
			return 0 // a kind's recent hits: no count
		}
		return n
	}
	add(palGroup{kindLabel("mail"), total(r.Mail.Total), mail})
	add(palGroup{kindLabel("conversation"), total(r.Conversations.Total), convs})
	add(palGroup{kindLabel("contact"), total(r.Contacts.Total), contacts})
	add(palGroup{kindLabel("attachment"), total(r.Attachments.Total), atts})
	if kind == "" {
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
		out = append(out, palItem{icon: "mail", title: cmp.Or(m.Subject, i18n.T("mail.no_subject")), sub: sub, when: m.Date, unread: it.Unread > 0,
			open: func() {
				p.a.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}, ThreadID: thread, MessageID: id})
			}})
	}
	return out
}

func (p *palette) convItems(cs []core.Conversation) []palItem {
	out := make([]palItem, 0, len(cs))
	for _, cv := range cs {
		id := cv.ID
		out = append(out, palItem{icon: "label-chat-3", title: cmp.Or(cv.Title, i18n.T("ask.new")), when: cv.UpdatedAt,
			open: func() { p.a.navigate(Route{Page: PageWit, ConversationID: id}) }})
	}
	return out
}

func (p *palette) contactItems(cs []core.Contact) []palItem {
	out := make([]palItem, 0, len(cs))
	for _, ct := range cs {
		addr := ct.Address
		out = append(out, palItem{icon: "user", title: cmp.Or(ct.Name, addr), sub: addr, when: ct.Last,
			open: func() { p.a.mail.mailFrom(addr) }})
	}
	return out
}

func (p *palette) attachmentItems(as []core.FoundAttachment) []palItem {
	out := make([]palItem, 0, len(as))
	for _, at := range as {
		id := at.MessageID
		out = append(out, palItem{icon: "paperclip", title: at.Filename, sub: displayName(at.From) + " · " + at.Subject, when: at.Date,
			open: func() { p.a.OpenMessage(id) }})
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

// mailFrom lists the synced mail from addr: the list's search, filtered by
// sender.
func (p *mailPage) mailFrom(addr string) {
	p.a.navigate(Route{Page: PageMail, View: p.view})
	p.closeSearch()
	p.searchOpen, p.sf.from = true, addr
	p.runSearch()
}

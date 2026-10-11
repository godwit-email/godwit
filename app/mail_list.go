package app

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

const (
	pageSize    = 50
	maxPage     = 200 // the Service's largest page
	searchLimit = 100 // the most results a search reads: a full answer may have more
)

// mailItem is a row of the list: a thread, or a message (flat view, search).
type mailItem struct {
	ID                     int64
	Thread                 bool
	Who, Subject, Snippet  string
	Date                   time.Time
	Count, Unread, Flagged int
	Attach                 bool
	Msg                    core.MessageSummary // a message row's own summary
}

func threadItem(t core.ThreadSummary) mailItem {
	return mailItem{ID: t.ID, Thread: true, Who: strings.Join(t.Participants, ", "), Subject: t.Subject, Snippet: t.Snippet,
		Date: t.LastDate, Count: t.Count, Unread: t.Unread, Flagged: t.Flagged, Attach: t.HasAttachments}
}

func messageItem(m core.MessageSummary) mailItem {
	it := mailItem{ID: m.ID, Who: displayName(m.From), Subject: m.Subject, Snippet: m.Snippet, Date: m.Date,
		Count: 1, Attach: m.HasAttachments, Msg: m}
	if !slices.Contains(m.Flags, `\Seen`) {
		it.Unread = 1
	}
	if slices.Contains(m.Flags, `\Flagged`) {
		it.Flagged = 1
	}
	return it
}

func displayName(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if a.Name != "" {
			return a.Name
		}
		return a.Address
	}
	return from
}

// mailPage is the Mail section: the sidebar of views and folders, the list
// column, and (later) the reader.
type mailPage struct {
	a *App

	view    core.View // what the list shows
	threads bool      // items are threads (Settings.ThreadView when loaded)
	items   []mailItem
	cursor  string // where the next page starts; "" once all is loaded
	loading bool   // a page or a refresh is in flight: one at a time
	stalled bool   // the last page failed: no more pages until a refresh
	gen     uint64 // bumped per reload; older list results are dropped

	main   listSel // the list's choice and place
	found  listSel // the search results', apart: they are other rows
	filter int     // 0 all, 1 unread

	folders   map[int64][]core.Folder // by account
	outbox    []outboxEntry           // every account's
	drafts    []core.Draft            // the drafts folder's account's, when it shows
	draftList ui.ListState
	draftSel  int64               // the draft the pane shows; 0 for none
	draftView map[int64]draftInfo // by draft, made when the drafts load
	accOpen   map[int64]*bool     // account groups unfolded in the sidebar, by account

	searchOpen, searched bool
	query                string
	results              []mailItem
	hlQuery              string // the query the results answer: what they highlight
	searchGen            uint64 // bumped per request and on leaving; older answers are dropped

	reader *reader // the thread or message the route names

	exec    func(ctx context.Context, op string, ids []int64, folder int64) error // write; tests wrap it
	fetched func()                                                                // tests: runs off the UI thread once a reload has read the list
}

// listSel is a list's place and choice, kept on its items by key.
type listSel struct {
	list ui.ListState
	sel  ui.Selection[string]
	cur  int // the row last chosen, -1 for none; the list keeps it on its item
}

func (l *listSel) reset() { *l = listSel{cur: -1} }

// key is the row's identity in the list and its choice: thread and message
// IDs are separate sequences.
func (it mailItem) key() string {
	if it.Thread {
		return "t:" + strconv.FormatInt(it.ID, 10)
	}
	return "m:" + strconv.FormatInt(it.ID, 10)
}

func newMailPage(a *App) *mailPage {
	p := &mailPage{a: a, folders: map[int64][]core.Folder{}, accOpen: map[int64]*bool{}}
	p.main.reset()
	p.found.reset()
	p.exec = p.write
	p.reader = newReader(p)
	return p
}

// ls is the choice of the rows shown.
func (p *mailPage) ls() *listSel {
	if p.searched {
		return &p.found
	}
	return &p.main
}

// write is a batch's call for one account's messages.
func (p *mailPage) write(ctx context.Context, op string, ids []int64, folder int64) error {
	svc := p.a.svc
	switch op {
	case "read":
		return svc.SetFlags(ctx, ids, []string{`\Seen`}, nil)
	case "unread":
		return svc.SetFlags(ctx, ids, nil, []string{`\Seen`})
	case "star":
		return svc.SetFlags(ctx, ids, []string{`\Flagged`}, nil)
	case "unstar":
		return svc.SetFlags(ctx, ids, nil, []string{`\Flagged`})
	case "archive":
		return svc.Move(ctx, ids, folder)
	case "delete":
		return svc.Delete(ctx, ids)
	}
	return fmt.Errorf("unknown mail action %q", op)
}

func (p *mailPage) refresh([]events.Event) {
	p.reload()
	p.reader.sync(true)
}

// reload re-reads the folders, the outbox and the list. In the same view it
// re-queries the range already loaded (everything, when all was loaded);
// in a new one it starts again with the first page.
func (p *mailPage) reload() {
	a := p.a
	p.gen++
	gen := p.gen
	v := a.route.View
	if a.route.Page != PageMail {
		v = p.view // a draft open: the list stays as it was
	}
	reset := v != p.view
	want, toEnd := len(p.items), p.cursor == "" && len(p.items) > 0
	if reset {
		p.view, p.items, p.cursor, p.stalled = v, nil, "", false
		p.main.reset()
		p.closeSearch()
		p.draftList, p.draftSel = ui.ListState{}, 0
		want, toEnd = 0, false
	}
	if p.searched {
		p.search()
	}
	p.loading = true
	hook := p.fetched
	var (
		folders = map[int64][]core.Folder{}
		outbox  []outboxEntry
		drafts  []core.Draft
		items   []mailItem
		cursor  string
		threads bool
	)
	a.do(func(ctx context.Context) error {
		accs, err := a.svc.ListAccounts(ctx)
		if err != nil {
			return err
		}
		for _, acc := range accs {
			if folders[acc.ID], err = a.svc.ListFolders(ctx, acc.ID); err != nil {
				return err
			}
			ob, err := a.svc.ListOutbox(ctx, acc.ID)
			if err != nil {
				return err
			}
			for _, it := range ob {
				outbox = append(outbox, outboxEntry{it, cmp.Or(acc.Alias, acc.Email)})
			}
		}
		st, err := a.svc.GetSettings(ctx)
		if err != nil {
			return err
		}
		threads = st.ThreadView
		draftsAcc, isDrafts := draftsOf(folders, v)
		switch {
		case v.Unified == "outbox" || v == (core.View{}):
		case isDrafts:
			drafts, err = a.svc.ListDrafts(ctx, draftsAcc)
		default:
			items, cursor, err = fetchItems(ctx, a.svc, v, threads, "", want, toEnd)
		}
		if hook != nil {
			hook()
		}
		return err
	}, func(err error) {
		if gen != p.gen {
			return
		}
		p.loading = false
		if err != nil {
			a.toastErr(err)
			return
		}
		if threads != p.threads {
			p.main.reset() // thread IDs and message IDs do not mix
		}
		p.folders, p.outbox, p.drafts, p.threads = folders, outbox, drafts, threads
		p.items, p.cursor, p.stalled = items, cursor, false
		p.setDrafts()
	})
}

// fetchItems reads pages from cursor until it has want items, or to the end
// with toEnd; at least one page. Items are unique by ID.
func fetchItems(ctx context.Context, svc *core.Service, v core.View, threads bool, cursor string, want int, toEnd bool) ([]mailItem, string, error) {
	var out []mailItem
	seen := map[int64]bool{}
	for {
		limit := maxPage
		if !toEnd {
			limit = min(maxPage, max(pageSize, want-len(out)))
		}
		var page []mailItem
		if threads {
			tp, err := svc.ListThreads(ctx, v, cursor, limit)
			if err != nil {
				return nil, "", err
			}
			for _, t := range tp.Items {
				page = append(page, threadItem(t))
			}
			cursor = tp.NextCursor
		} else {
			mp, err := svc.ListMessages(ctx, v, cursor, limit)
			if err != nil {
				return nil, "", err
			}
			for _, m := range mp.Items {
				page = append(page, messageItem(m))
			}
			cursor = mp.NextCursor
		}
		for _, it := range page {
			if !seen[it.ID] {
				seen[it.ID] = true
				out = append(out, it)
			}
		}
		if cursor == "" || !toEnd && len(out) >= want {
			return out, cursor, nil
		}
	}
}

// loadMore appends the next page, unless one is loading already.
func (p *mailPage) loadMore() {
	if p.loading || p.stalled || p.cursor == "" || p.searched {
		return
	}
	a := p.a
	p.loading = true
	gen, v, threads, cursor := p.gen, p.view, p.threads, p.cursor
	var got []mailItem
	var next string
	a.do(func(ctx context.Context) (err error) {
		got, next, err = fetchItems(ctx, a.svc, v, threads, cursor, pageSize, false)
		return err
	}, func(err error) {
		if gen != p.gen {
			return
		}
		p.loading = false
		if err != nil {
			p.stalled = true
			a.toastErr(err)
			return
		}
		have := map[int64]bool{}
		for _, it := range p.items {
			have[it.ID] = true
		}
		for _, it := range got {
			if !have[it.ID] {
				p.items = append(p.items, it)
			}
		}
		p.cursor = next
	})
}

// shown are the rows the list shows: the search's results, or the items,
// only unread ones under the unread filter.
func (p *mailPage) shown() []mailItem {
	switch {
	case p.searched:
		return p.results
	case p.filter == 0:
		return p.items
	}
	return slices.DeleteFunc(slices.Clone(p.items), func(it mailItem) bool { return it.Unread == 0 })
}

// open shows rows[i] in the reader. Choosing a row is not navigation: the
// route changes in place.
func (p *mailPage) open(rows []mailItem, i int) {
	if i < 0 || i >= len(rows) {
		return
	}
	it, r := rows[i], &p.a.route
	if it.Thread {
		r.ThreadID, r.MessageID = it.ID, 0
	} else {
		r.ThreadID, r.MessageID = it.Msg.ThreadID, it.ID
	}
	p.reader.sync(false)
}

// chosen are the rows chosen, or the one open when none is.
func (p *mailPage) chosen(rows []mailItem) []mailItem {
	l := p.ls()
	var out []mailItem
	for _, it := range rows {
		if l.sel.Has(it.key()) {
			out = append(out, it)
		}
	}
	if i := rowOf(p.entries(rows), l.cur); len(out) == 0 && i >= 0 {
		out = append(out, rows[i])
	}
	return out
}

func (p *mailPage) folder(id int64) (core.Folder, bool) { return findFolder(p.folders, id) }

// localDrafts is the drafts entry of an account whose server has no drafts
// folder: drafts are local, so every account has one.
func localDrafts(acc int64) core.View {
	return core.View{Unified: "drafts:" + strconv.FormatInt(acc, 10)}
}

// draftsOf returns the account whose drafts v lists: a drafts-role folder's,
// or a localDrafts entry's.
func draftsOf(folders map[int64][]core.Folder, v core.View) (int64, bool) {
	if id, ok := strings.CutPrefix(v.Unified, "drafts:"); ok {
		n, err := strconv.ParseInt(id, 10, 64)
		return n, err == nil
	}
	f, ok := findFolder(folders, v.FolderID)
	return f.AccountID, ok && f.Role == "drafts"
}

// viewAccount is the account a view belongs to; 0 for a unified view.
func (p *mailPage) viewAccount(v core.View) int64 {
	if acc, ok := draftsOf(p.folders, v); ok {
		return acc
	}
	f, _ := p.folder(v.FolderID)
	return f.AccountID
}

func findFolder(folders map[int64][]core.Folder, id int64) (core.Folder, bool) {
	for _, fs := range folders {
		for _, f := range fs {
			if f.ID == id {
				return f, true
			}
		}
	}
	return core.Folder{}, false
}

func (p *mailPage) inboxUnread() int {
	n := 0
	for _, fs := range p.folders {
		for _, f := range fs {
			if f.Role == "inbox" {
				n += f.Unread
			}
		}
	}
	return n
}

func unreadSub(n int) string {
	if n == 0 {
		return ""
	}
	return i18n.Tf("mail.unread_count", n)
}

// Title names the view the route shows, with its unread count.
func (p *mailPage) Title() (string, string) {
	switch v := p.a.route.View; v.Unified {
	case "inbox":
		return i18n.T("mail.unified.inbox"), unreadSub(p.inboxUnread())
	case "starred":
		return i18n.T("mail.unified.starred"), ""
	case "outbox":
		return i18n.T("mail.outbox"), ""
	default:
		if _, ok := draftsOf(p.folders, v); ok && v.FolderID == 0 {
			return i18n.T("mail.folder.drafts"), ""
		}
		if f, ok := p.folder(v.FolderID); ok {
			return folderName(f), unreadSub(f.Unread)
		}
	}
	return "", ""
}

// listTime is a row's time: HH:mm today, then yesterday, a weekday within
// this calendar week (from Monday), else M/DD.
func listTime(now, t time.Time) string {
	t = t.In(now.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return t.Format("15:04")
	case !t.Before(today.AddDate(0, 0, -1)):
		return i18n.T("mail.yesterday")
	case !t.Before(today.AddDate(0, 0, -(int(now.Weekday())+6)%7)): // this week, from Monday
		return i18n.T(fmt.Sprintf("mail.weekday.%d", t.Weekday()))
	}
	return fmt.Sprintf("%d/%02d", t.Month(), t.Day())
}

// Content is the list column and the reader beside it.
func (p *mailPage) Content(c *ui.Context) {
	t := theme.Of(c)
	_, isDrafts := draftsOf(p.folders, p.view)
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(theme.ListWidth).BorderWidth(0, theme.Hairline, 0, 0).BorderColor(t.Border).Children(func() {
			switch {
			case p.view.Unified == "outbox":
				p.outboxView(c)
			case isDrafts:
				p.draftsView(c)
			default:
				p.listView(c)
			}
		})
		if isDrafts {
			p.draftPane(c)
		} else {
			p.reader.view(c)
		}
	})
}

// listHeader is the search box, or the first date group's heading (the
// list carries the later ones) with the all/unread tabs.
func (p *mailPage) listHeader(c *ui.Context, head string) {
	t, th := theme.Of(c), c.Theme()
	if p.searchOpen {
		ui.Column(c).Padding(th.Space(3), th.Space(3), th.Space(2), th.Space(3)).Gap(th.Space(2)).Children(func() {
			box := ui.Row(c).Height(th.Space(8)).AlignItems(ui.Center).Gap(th.Space(2)).Padding(0, th.Space(1), 0, th.Space(2.5)).
				Radius(t.Radius.Lg).Border(theme.Hairline, t.Input).Background(t.Background)
			if box.FocusWithin() { // the row is the box, so it carries the ring
				box.Border(theme.Hairline, t.Ring).Shadow(0, 0, 0, theme.RingWidth, th.Focus)
			}
			box.Children(func() {
				ui.Icon(c, icons.Get("search")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
				in := kit.Input(c, &p.query, kit.InputOpts{Placeholder: i18n.T("mail.search.placeholder"), Bare: true}).FocusRing(false).AutoFocus().Grow(1)
				if in.Submitted() {
					p.runSearch()
				}
				if p.query != "" {
					kit.IconButton(c, "x", i18n.T("mail.search.clear"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						Size(th.Space(6), th.Space(6)).TextColor(t.Muted).OnClick(p.clearSearch)
				}
			})
			kit.Text(c, p.searchHint(), t.Text.Sm).TextColor(t.Muted)
		})
		if p.searched && len(p.results) > 0 {
			q := p.hlQuery
			kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { p.a.askWit(q) }}).Label(i18n.Tf("mail.search.ask_wit_row", q)).
				Margin(0, th.Space(2), th.Space(1), th.Space(2)).Padding(th.Space(2), th.Space(3)).Gap(th.Space(2)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
				ui.Icon(c, icons.Get("sparkles")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
				kit.Text(c, i18n.Tf("mail.search.ask_wit_row", q), t.Text.Sm).SingleLine().Grow(1).Shrink(1)
			})
		}
		if c.Shortcut(0, ui.KeyEscape) {
			p.closeSearch()
		}
		return
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		p.searchOpen = true
	}
	ui.Row(c).AlignItems(ui.Center).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4)).Gap(th.Space(2)).Children(func() {
		kit.Text(c, head, t.Text.Base).FontWeight(500).TextColor(t.Muted)
		ui.Spacer(c)
		kit.Tabs(c, &p.filter, i18n.T("mail.filter.all"), i18n.T("mail.filter.unread"))
		if p.a.sidebarHidden { // else the sidebar's header has it
			kit.IconButton(c, "search", i18n.T("mail.search"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() { p.searchOpen = true })
		}
	})
}

// searchHint is the line under the search box: what was found, in which
// accounts.
func (p *mailPage) searchHint() string {
	n := len(p.results)
	switch {
	case !p.searched:
		return i18n.T("mail.search.hint")
	case n == 0:
		return i18n.T("mail.search.none")
	}
	count := strconv.Itoa(n)
	if n >= searchLimit {
		count += "+"
	}
	scope := i18n.T("mail.search.all_accounts")
	if acc := p.viewAccount(p.view); acc != 0 {
		if a, ok := p.a.accountByID(acc); ok {
			scope = a.Email
		}
	}
	return i18n.Tf("mail.search.found", count, scope)
}

// dateGroup names the heading a day's mail sits under: today, yesterday,
// this calendar week (from Monday) or earlier.
func dateGroup(now, t time.Time) string {
	t = t.In(now.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return i18n.T("mail.group.today")
	case !t.Before(today.AddDate(0, 0, -1)):
		return i18n.T("mail.group.yesterday")
	case !t.Before(today.AddDate(0, 0, -(int(now.Weekday())+6)%7)):
		return i18n.T("mail.group.week")
	}
	return i18n.T("mail.group.older")
}

// highlight splits text into spans, the matches of query (a plain substring,
// each rune compared case-insensitively) marked with the highlight tokens.
// No query gives no spans.
func highlight(t theme.Tokens, text, query string) []ui.Span {
	q := []rune(query)
	if len(q) == 0 {
		return nil
	}
	var out []ui.Span
	start := 0
	for i := 0; i < len(text); {
		if n := matchAt(text[i:], q); n > 0 {
			if i > start {
				out = append(out, ui.Span{Text: text[start:i]})
			}
			out = append(out, ui.Span{Text: text[i : i+n], Background: t.Highlight, Color: t.HighlightText})
			i += n
			start = i
			continue
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	if start < len(text) {
		out = append(out, ui.Span{Text: text[start:]})
	}
	return out
}

// matchAt is the length in bytes of the start of s that equals q rune for
// rune, ignoring case; 0 for no match.
func matchAt(s string, q []rune) int {
	n := 0
	for _, want := range q {
		r, w := utf8.DecodeRuneInString(s[n:])
		if w == 0 || !foldEq(r, want) {
			return 0
		}
		n += w
	}
	return n
}

func foldEq(a, b rune) bool {
	for f := unicode.SimpleFold(a); ; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
		if f == a {
			return a == b
		}
	}
}

// marked is s with the query's matches highlighted; plain text without one.
func marked(c *ui.Context, s, query string) ui.Element {
	if sp := highlight(theme.Of(c), s, query); sp != nil {
		return ui.RichText(c, sp...)
	}
	return ui.Text(c, s)
}

func (p *mailPage) listView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	rows := p.shown()
	ls := p.ls()
	es := p.entries(rows)
	head := ""
	if len(rows) > 0 && !p.searched {
		head = dateGroup(p.a.clock.Now(), rows[0].Date)
	}
	p.listHeader(c, head)
	ls.list.Key = func(i int) any {
		if e := es[i]; e.head != "" {
			return "h:" + e.head
		}
		return rows[es[i].row].key()
	}
	ls.list.Header = nil
	if !p.searched {
		ls.list.Header = func(i int) bool { return es[i].head != "" }
	}
	ls.list.Selected, ls.list.Selection = &ls.cur, &ls.sel
	act, target := "", []mailItem(nil)
	l := kit.List(c, &ls.list, len(es), func(i int) {
		if es[i].head != "" { // a date heading: no row, never chosen
			kit.Text(c, es[i].head, t.Text.Base).FontWeight(500).TextColor(t.Muted).Background(t.Background).
				Padding(th.Space(2), th.Space(2.5), th.Space(0.5), th.Space(5))
			return
		}
		it := rows[es[i].row]
		p.item(c, it).ContextMenu(func(m *ui.Menu) {
			on := []mailItem{it}
			if ls.sel.Has(it.key()) {
				on = p.chosen(rows)
			}
			// as S and U: read/star when any of them is unread/unstarred
			read, star := "unread", "unstar"
			if slices.ContainsFunc(on, func(r mailItem) bool { return r.Unread > 0 }) {
				read = "read"
			}
			if slices.ContainsFunc(on, func(r mailItem) bool { return r.Flagged < r.Count }) {
				star = "star"
			}
			for _, k := range []string{read, star, "", "archive", "delete"} {
				if k == "" {
					m.Separator()
				} else if m.Item(i18n.T("mail.action." + k)).Chosen() {
					act, target = k, on
				}
			}
		})
	}).Grow(1).Padding(0, th.Space(2))
	l.Children(func() {
		if len(rows) == 0 && p.loading { // the first load: placeholder rows
			for range 6 {
				ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(2.5), th.Space(2.5), th.Space(2.5), th.Space(5)).Children(func() {
					kit.Skeleton(c, 0, t.Text.Body)
					kit.Skeleton(c, theme.SkeletonShort, t.Text.Sm)
				})
			}
		}
		if len(rows) == 0 && !p.loading {
			if p.searched {
				p.noResults(c)
			} else {
				ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted).Padding(th.Space(3))
			}
		}
	})
	if l.Changed() {
		p.open(rows, rowOf(es, ls.cur))
	}
	for _, k := range []struct {
		mods ui.Modifiers
		key  ui.Key
		act  string
	}{{0, ui.KeyE, "archive"}, {ui.Shift, ui.Key3, "delete"}, {0, ui.KeyU, "toggle_read"}, {0, ui.KeyS, "toggle_star"}} {
		if ls.list.Shortcut(c, k.mods, k.key) {
			act, target = k.act, p.chosen(rows)
		}
	}
	if act != "" {
		p.batch(act, target)
	}
	if _, last := ls.list.Visible(); last >= len(es)-5 {
		p.loadMore()
	}
}

// listEntry is a line of the list: a date heading (head), or the row
// rows[row].
type listEntry struct {
	head string
	row  int
}

// entries are the list's lines: each group of rows under its date heading
// (but the first, whose heading the list header shows), a search's results
// as they are. The list's own choice (cur) counts these.
func (p *mailPage) entries(rows []mailItem) []listEntry {
	var out []listEntry
	now, last := p.a.clock.Now(), ""
	for i, it := range rows {
		if !p.searched {
			if g := dateGroup(now, it.Date); g != last {
				if i > 0 {
					out = append(out, listEntry{head: g})
				}
				last = g
			}
		}
		out = append(out, listEntry{row: i})
	}
	return out
}

// rowOf is the row entry cur is, -1 for none or a heading.
func rowOf(es []listEntry, cur int) int {
	if cur < 0 || cur >= len(es) || es[cur].head != "" {
		return -1
	}
	return es[cur].row
}

// item is a row of the list: a search result, or a thread or message.
func (p *mailPage) item(c *ui.Context, it mailItem) ui.Element {
	if p.searched {
		return p.resultRow(c, it)
	}
	return p.row(c, it)
}

// folderLine is "folder · account" of a result.
func (p *mailPage) folderLine(it mailItem) string {
	f, ok := p.folder(it.Msg.FolderID)
	if !ok {
		return ""
	}
	if acc, ok := p.a.accountByID(f.AccountID); ok {
		return folderName(f) + " · " + acc.Email
	}
	return folderName(f)
}

// resultWho is the sender of a result; of mail sent, who it went to.
func (p *mailPage) resultWho(it mailItem) string {
	if f, ok := p.folder(it.Msg.FolderID); ok && f.Role == "sent" && len(it.Msg.To) > 0 {
		return i18n.Tf("mail.search.sent_to", displayName(it.Msg.To[0]))
	}
	return it.Who
}

// resultRow is a search result: no unread gutter, the subject and the start
// of the text with the matches marked, and the folder it is in.
func (p *mailPage) resultRow(c *ui.Context, it mailItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	q := p.hlQuery
	return ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(2), th.Space(3)).Children(func() {
		ui.Row(c).Gap(th.Space(1.5)).Children(func() {
			ui.Text(c, p.resultWho(it)).SingleLine().Grow(1).Shrink(1)
			if it.Attach {
				ui.Icon(c, icons.Get("paperclip")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted).Label(i18n.T("mail.attachment"))
			}
			kit.Text(c, listTime(p.a.clock.Now(), it.Date), t.Text.Sm).TextColor(t.Muted)
		})
		subject := it.Subject
		if subject == "" {
			subject = i18n.T("mail.no_subject")
		}
		marked(c, subject, q).SingleLine()
		if it.Snippet != "" {
			marked(c, it.Snippet, q).FontSize(t.Text.Sm).LineHeight(t.Text.Line(t.Text.Sm)).TextColor(t.Muted).SingleLine()
		}
		if line := p.folderLine(it); line != "" {
			kit.Text(c, line, t.Text.Sm).TextColor(t.Muted).SingleLine()
		}
	})
}

// openAccountSettings opens an account's settings (the first account's for
// a unified view).
func (a *App) openAccountSettings(acc int64) func() {
	return func() {
		if acc == 0 && len(a.accounts) > 0 {
			acc = a.accounts[0].ID
		}
		r := Route{Page: PageSettings}
		if acc != 0 {
			r.Settings = "account:" + strconv.FormatInt(acc, 10)
		}
		a.navigate(r)
	}
}

// noResults is the list of a search that found nothing: it offers Wit and a
// wider sync.
func (p *mailPage) noResults(c *ui.Context) {
	q, th := p.hlQuery, c.Theme()
	kit.Empty(c, kit.EmptyOpts{Icon: "search", Title: i18n.Tf("mail.search.empty_title", q), Desc: i18n.T("mail.search.empty_desc"), Actions: func() {
		kit.Button(c, i18n.T("mail.search.ask_wit"), kit.ButtonOpts{}).OnClick(func() { p.a.askWit(q) })
		kit.Button(c, i18n.T("mail.search.expand"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(p.a.openAccountSettings(p.viewAccount(p.view)))
	}}).Margin(th.Space(8), th.Space(1))
}

// row is one thread or message: who, when, the subject and the start of the
// text; unread ones in bold, with a dot in the row's left gutter that says
// how many are unread to assistive technology.
func (p *mailPage) row(c *ui.Context, it mailItem) ui.Element {
	t := theme.Of(c)
	th, small := c.Theme(), t.Text.Sm
	weight := 400
	if it.Unread > 0 {
		weight = 600
	}
	return ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(2.5), th.Space(2.5), th.Space(2.5), th.Space(5)).Children(func() {
		if it.Unread > 0 {
			ui.Box(c).Absolute().Left(th.Space(1.5)).Top(th.Space(4)).Size(th.Space(2), th.Space(2)).Radius(th.Space(1)).Background(t.Unread).
				Label(i18n.Tf("mail.row.unread", it.Unread))
		}
		ui.Row(c).Gap(th.Space(1.5)).Children(func() {
			ui.Text(c, it.Who).FontWeight(weight).SingleLine().Grow(1).Shrink(1)
			if it.Attach {
				ui.Icon(c, icons.Get("paperclip")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted).Label(i18n.T("mail.attachment"))
			}
			kit.Text(c, listTime(p.a.clock.Now(), it.Date), small).TextColor(t.Muted)
		})
		subject := it.Subject
		if subject == "" {
			subject = i18n.T("mail.no_subject")
		}
		ui.Text(c, subject).FontWeight(weight).SingleLine()
		if it.Snippet != "" {
			kit.Text(c, it.Snippet, t.Text.Base).TextColor(t.Muted).SingleLine()
		}
	})
}

// runSearch searches the synced mail of the view's account, every
// account's in a unified view.
func (p *mailPage) runSearch() {
	p.found.reset() // new results, a new choice
	p.search()
}

// search runs the query; only the newest request's answer shows, and none
// once the search is left.
func (p *mailPage) search() {
	p.searchGen++
	q := strings.TrimSpace(p.query)
	if q == "" {
		p.searched, p.results = false, nil
		return
	}
	a := p.a
	acc, gen := p.viewAccount(p.view), p.searchGen
	var found []core.MessageSummary
	a.do(func(ctx context.Context) (err error) {
		found, err = a.svc.Search(ctx, acc, q, searchLimit)
		return err
	}, func(err error) {
		if gen != p.searchGen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		p.results, p.hlQuery = nil, q
		r := p.a.route
		for i, m := range found {
			it := messageItem(m)
			if p.found.cur < 0 && (m.ID == r.MessageID || r.MessageID == 0 && m.ThreadID == r.ThreadID && r.ThreadID != 0) {
				p.found.cur = i // the mail open stays chosen among the results
				p.found.sel.Add(it.key())
			}
			p.results = append(p.results, it)
		}
		p.searched = true
	})
}

// clearSearch empties the box and the results; the search stays open.
func (p *mailPage) clearSearch() {
	p.searchGen++
	p.searched, p.query, p.results = false, "", nil
	p.found.reset()
}

func (p *mailPage) closeSearch() {
	p.searchGen++
	p.searchOpen, p.searched, p.query, p.results = false, false, "", nil
	p.found.reset()
}

// batch applies act to rows: threads become their messages (GetThread),
// grouped by account, one call per account. Archiving and deleting take
// only the messages in the view; an account without an archive folder is
// skipped and named.
func (p *mailPage) batch(act string, rows []mailItem) {
	if len(rows) == 0 {
		return
	}
	a := p.a
	acctOf, role, archive := map[int64]int64{}, map[int64]string{}, map[int64]int64{}
	for acc, fs := range p.folders {
		for _, f := range fs {
			acctOf[f.ID], role[f.ID] = acc, f.Role
			if f.Role == "archive" && archive[acc] == 0 {
				archive[acc] = f.ID
			}
		}
	}
	names := map[int64]string{}
	for _, acc := range a.accounts {
		names[acc.ID] = acc.Email
	}
	v, search := p.view, p.searched
	inView := func(m core.MessageSummary) bool {
		switch {
		case search:
			return true
		case v.Unified == "inbox":
			return role[m.FolderID] == "inbox"
		case v.Unified == "starred":
			return slices.Contains(m.Flags, `\Flagged`)
		}
		return m.FolderID == v.FolderID
	}
	l, exec := p.ls(), p.exec
	var op string
	failed, byAcc := 0, map[int64][]int64{}
	// Expansions run in the order asked, so the writes they chain do too.
	a.writes.Chain("mail:expand", func(ctx context.Context) error {
		var msgs []core.MessageSummary
		for _, it := range rows {
			if !it.Thread {
				msgs = append(msgs, it.Msg)
				continue
			}
			th, err := a.svc.GetThread(ctx, it.ID)
			if err != nil {
				failed += it.Count
				continue
			}
			msgs = append(msgs, th.Messages...)
		}
		op = act
		switch act {
		case "toggle_read":
			op = "unread"
			if slices.ContainsFunc(msgs, func(m core.MessageSummary) bool { return !slices.Contains(m.Flags, `\Seen`) }) {
				op = "read"
			}
		case "toggle_star":
			op = "unstar"
			if slices.ContainsFunc(msgs, func(m core.MessageSummary) bool { return !slices.Contains(m.Flags, `\Flagged`) }) {
				op = "star"
			}
		}
		for _, m := range msgs {
			if (op == "archive" || op == "delete") && !inView(m) {
				continue
			}
			acc, ok := acctOf[m.FolderID]
			if !ok {
				failed++
				continue
			}
			byAcc[acc] = append(byAcc[acc], m.ID)
		}
		return nil
	}, func(error) {
		var skipped []string
		pending := 0
		finish := func() {
			if failed > 0 {
				a.toastAs(kit.ToastError, i18n.Tf("mail.failed", failed))
			}
			if len(skipped) > 0 {
				a.toastAs(kit.ToastWarning, i18n.Tf("mail.no_archive", strings.Join(skipped, i18n.T("mail.list_sep"))))
			}
			if op == "archive" || op == "delete" {
				l.sel.Clear()
			}
		}
		for _, acc := range slices.Sorted(maps.Keys(byAcc)) {
			ids := byAcc[acc]
			if op == "archive" && archive[acc] == 0 {
				skipped = append(skipped, names[acc])
				continue
			}
			pending++
			folder := archive[acc]
			a.writes.Chain(fmt.Sprintf("account:%d", acc), func(ctx context.Context) error {
				return exec(ctx, op, ids, folder)
			}, func(err error) {
				if err != nil {
					failed += len(ids)
				}
				if pending--; pending == 0 {
					finish()
				}
			})
		}
		if pending == 0 {
			finish()
		}
	})
}

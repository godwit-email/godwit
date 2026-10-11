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

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

const (
	pageSize = 50
	maxPage  = 200 // the Service's largest page
)

// mailItem is a row of the list: a thread, or a message (flat view, search).
type mailItem struct {
	ID                    int64
	Thread                bool
	Who, Subject, Snippet string
	Date                  time.Time
	Count, Unread         int
	Attach                bool
	Msg                   core.MessageSummary // a message row's own summary
}

func threadItem(t core.ThreadSummary) mailItem {
	return mailItem{ID: t.ID, Thread: true, Who: strings.Join(t.Participants, ", "), Subject: t.Subject, Snippet: t.Snippet,
		Date: t.LastDate, Count: t.Count, Unread: t.Unread, Attach: t.HasAttachments}
}

func messageItem(m core.MessageSummary) mailItem {
	it := mailItem{ID: m.ID, Who: displayName(m.From), Subject: m.Subject, Snippet: m.Snippet, Date: m.Date,
		Count: 1, Attach: m.HasAttachments, Msg: m}
	if !slices.Contains(m.Flags, `\Seen`) {
		it.Unread = 1
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
	accOpen   map[int64]*bool // account groups unfolded in the sidebar, by account

	searchOpen, searched bool
	query                string
	results              []mailItem
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
		p.draftList = ui.ListState{}
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
	if len(out) == 0 && l.cur >= 0 && l.cur < len(rows) {
		out = append(out, rows[l.cur])
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
// this calendar week (from Monday), else M/D.
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
	return fmt.Sprintf("%d/%d", t.Month(), t.Day())
}

// Content is the list column and the reader beside it.
func (p *mailPage) Content(c *ui.Context) {
	t := theme.Of(c)
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(theme.ListWidth).BorderWidth(0, theme.Hairline, 0, 0).BorderColor(t.Border).Children(func() {
			_, isDrafts := draftsOf(p.folders, p.view)
			switch {
			case p.view.Unified == "outbox":
				p.outboxView(c)
			case isDrafts:
				p.draftsView(c)
			default:
				p.listView(c)
			}
		})
		p.reader.view(c)
	})
}

func (p *mailPage) listHeader(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	if p.searchOpen {
		ui.Column(c).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4)).Gap(th.Space(1.5)).Children(func() {
			in := kit.Input(c, &p.query, kit.InputOpts{Placeholder: i18n.T("mail.search.placeholder")}).AutoFocus()
			if in.Submitted() {
				p.runSearch()
			}
			ui.Text(c, i18n.T("mail.search.hint")).FontSize(t.Text.Sm).TextColor(t.Muted)
		})
		if c.Shortcut(0, ui.KeyEscape) {
			p.closeSearch()
		}
		return
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		p.searchOpen = true
	}
	ui.Row(c).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4)).Gap(th.Space(2)).Children(func() {
		title, _ := p.Title()
		ui.Text(c, title).FontWeight(500).TextColor(t.Muted).SingleLine().Shrink(1)
		ui.Spacer(c)
		kit.Tabs(c, &p.filter, i18n.T("mail.filter.all"), i18n.T("mail.filter.unread"))
		if p.a.sidebarHidden { // else the sidebar's header has it
			kit.IconButton(c, "search", i18n.T("mail.search"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() { p.searchOpen = true })
		}
	})
}

func (p *mailPage) listView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p.listHeader(c)
	rows := p.shown()
	ls := p.ls()
	ls.list.Key = func(i int) any { return rows[i].key() }
	ls.list.Selected, ls.list.Selection = &ls.cur, &ls.sel
	act, target := "", []mailItem(nil)
	l := kit.List(c, &ls.list, len(rows), func(i int) {
		it := rows[i]
		p.row(c, it).ContextMenu(func(m *ui.Menu) {
			for _, k := range []string{"read", "unread", "star", "", "archive", "delete"} {
				if k == "" {
					m.Separator()
				} else if m.Item(i18n.T("mail.action." + k)).Chosen() {
					act, target = k, []mailItem{it}
					if ls.sel.Has(it.key()) {
						target = p.chosen(rows)
					}
				}
			}
		})
	}).Grow(1).Padding(0, th.Space(2)).Gap(theme.Hairline) // rows a hairline apart
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
			empty := i18n.T("mail.empty")
			if p.searched {
				empty = i18n.T("mail.search.empty")
			}
			ui.Text(c, empty).TextColor(t.Muted).Padding(th.Space(3))
		}
	})
	if l.Changed() {
		p.open(rows, ls.cur)
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
	if _, last := ls.list.Visible(); last >= len(rows)-5 {
		p.loadMore()
	}
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
			if it.Count > 1 {
				ui.Text(c, strconv.Itoa(it.Count)).FontSize(small).TextColor(t.Muted)
			}
			ui.Text(c, listTime(p.a.clock.Now(), it.Date)).FontSize(small).TextColor(t.Muted)
		})
		subject := it.Subject
		if subject == "" {
			subject = i18n.T("mail.no_subject")
		}
		ui.Text(c, subject).FontWeight(weight).SingleLine()
		if it.Snippet != "" {
			ui.Text(c, it.Snippet).TextColor(t.Muted).SingleLine()
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
		found, err = a.svc.Search(ctx, acc, q, 100)
		return err
	}, func(err error) {
		if gen != p.searchGen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		p.results = nil
		for _, m := range found {
			p.results = append(p.results, messageItem(m))
		}
		p.searched = true
	})
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

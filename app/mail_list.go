package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

const (
	pageSize = 50
	maxPage  = 200 // the Service's largest page
	// shortcut: core caps a search at 200 and the list has no paging past it ("200+"); page with a cursor if that bites.
	searchLimit = 200 // the most results a search reads, as many as the palette shows
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
	Labels                 []core.AppliedLabel
	Security               gomail.SecurityReport

	// In a view: its group, and what ViewRow says of it.
	Group       string
	Status      string
	NewReply    bool
	WaitingDays int
	Manual      bool
}

func threadItem(t core.ThreadSummary) mailItem {
	return mailItem{ID: t.ID, Thread: true, Who: strings.Join(t.Participants, ", "), Subject: t.Subject, Snippet: t.Snippet,
		Date: t.LastDate, Count: t.Count, Unread: t.Unread, Flagged: t.Flagged, Attach: t.HasAttachments,
		Security: t.Security, Msg: core.MessageSummary{From: t.From}}
}

func messageItem(m core.MessageSummary) mailItem {
	it := mailItem{ID: m.ID, Who: cmp.Or(m.FromName, displayName(m.From)), Subject: m.Subject, Snippet: m.Snippet, Date: m.Date,
		Count: 1, Attach: m.HasAttachments, Msg: m, Security: m.Security}
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

	devPath string    // dev: the HTML file just written, to copy
	view    core.View // what the list shows
	threads bool      // items are threads (Settings.ThreadView when loaded)
	items   []mailItem
	cursor  string // where the next page starts; "" once all is loaded
	loading bool   // a page or a refresh is in flight: one at a time
	stalled bool   // the last page failed: no more pages until a refresh
	gen     uint64 // bumped per reload; older list results are dropped
	older   olderLoad

	main   listSel // the list's choice and place
	found  listSel // the search results', apart: they are other rows
	filter int     // 0 all, 1 unread

	folders                  map[int64][]core.Folder // by account
	outbox                   []outboxEntry           // every account's
	drafts                   []core.Draft            // the drafts folder's account's, when it shows
	draftList                *ui.ListState           // replaced, never zeroed: the last frame's tree still points at the old one
	draftSel                 int64                   // the draft the pane shows; 0 for none
	draftView                map[int64]draftInfo     // by draft, made when the drafts load
	sending                  draftSend               // the pane's send in flight, or awaiting warnings' acknowledgement
	accOpen                  map[int64]*bool         // account groups unfolded in the sidebar, by account
	accLabelsOpen            map[int64]*bool         // account labels unfolded in the sidebar, by account
	folderOpen               map[folderKey]*bool     // user folders unfolded in the sidebar
	labelsOpen, accountsOpen bool                    // the sidebar's groups unfolded

	searchOpen, searched bool
	query                string
	results              []mailItem
	hlQuery              string // the query the results answer: what they highlight
	sf                   searchFilters
	searchGen            uint64 // bumped per request and on leaving; older answers are dropped

	reader *reader // the thread or message the route names

	purgeAsk  bool       // Delete in Trash awaits its confirmation
	purgeRows []mailItem // what it deletes permanently

	starredUnread      map[int64]int // unread starred count by account
	starredUnreadTotal int           // total unread starred across all accounts

	emptyFolderAsk  bool
	emptyFolderID   int64
	emptyFolderRole string
	emptyFolderAcc  int64

	nameDlg nameDialog                   // asks a new archive's or folder's name
	imports map[int64]context.CancelFunc // the archives importing, and their cancel

	views      []core.SavedView // in sidebar order, with their unread counts
	viewsKnown bool
	viewsOpen  bool             // the sidebar's group unfolded
	viewGroups []viewGroupState // the view shown's groups; p.items holds their rows in order
	viewEdit   *viewEditor      // shown in place of the list and reader
	viewLeave  func()           // asking to give up viewEdit's changes, for this
	viewDelete *core.SavedView  // awaiting its confirmation
	// The dialogs' own: kit.Dialog times its fade by this address, so it must outlive the frame.
	viewLeaveOpen, viewDeleteOpen bool
	viewsCached                   bool   // the last refresh changed nothing they show
	viewsReading                  bool   // a reload that reads them has not landed
	viewRefresh                   func() // stops the timer that rereads an open view

	exec    func(ctx context.Context, op string, ids []int64, from, folder int64) (core.ArchiveUndo, error) // write; tests wrap it
	fetched func()                                                                                          // tests: runs off the UI thread once a reload has read the list
}

// listSel is a list's place and choice, kept on its items by key.
type listSel struct {
	list *ui.ListState // replaced, never zeroed: the last frame's tree still points at the old one
	sel  ui.Selection[string]
	cur  int // the row last chosen, -1 for none; the list keeps it on its item
}

func (l *listSel) reset() { *l = listSel{list: &ui.ListState{}, cur: -1} }

// key is the row's identity in the list and its choice: thread and message
// IDs are separate sequences.
func (it mailItem) key() string {
	if it.Thread {
		return "t:" + strconv.FormatInt(it.ID, 10)
	}
	return "m:" + strconv.FormatInt(it.ID, 10)
}

func newMailPage(a *App) *mailPage {
	p := &mailPage{a: a, folders: map[int64][]core.Folder{}, accOpen: map[int64]*bool{}, accLabelsOpen: map[int64]*bool{}, folderOpen: map[folderKey]*bool{}, imports: map[int64]context.CancelFunc{}, labelsOpen: true, accountsOpen: true, viewsOpen: true, draftList: &ui.ListState{}}
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

// write is a batch's call for one account's messages: from is the folder of
// the view it came from (0 for none), folder where archive and inbox put them.
func (p *mailPage) write(ctx context.Context, op string, ids []int64, from, folder int64) (core.ArchiveUndo, error) {
	svc := p.a.svc
	switch op {
	case "read":
		return core.ArchiveUndo{}, svc.SetFlags(ctx, ids, []string{`\Seen`}, nil)
	case "unread":
		return core.ArchiveUndo{}, svc.SetFlags(ctx, ids, nil, []string{`\Seen`})
	case "star":
		return core.ArchiveUndo{}, svc.SetFlags(ctx, ids, []string{`\Flagged`}, nil)
	case "unstar":
		return core.ArchiveUndo{}, svc.SetFlags(ctx, ids, nil, []string{`\Flagged`})
	case "archive":
		return svc.ArchiveMessages(ctx, ids, folder)
	case "delete":
		return core.ArchiveUndo{}, svc.Delete(ctx, ids)
	case "junk":
		return core.ArchiveUndo{}, svc.MoveToRole(ctx, ids, "junk")
	case "inbox":
		return core.ArchiveUndo{}, svc.MoveFrom(ctx, ids, from, folder)
	case "purge":
		return core.ArchiveUndo{}, svc.DeleteForever(ctx, ids)
	}
	return core.ArchiveUndo{}, fmt.Errorf("unknown mail action %q", op)
}

func (p *mailPage) refresh(dirty []events.Event) {
	p.viewsCached = viewUnchanged(dirty)
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
	savedViewID, viewList := savedViewOf(v)
	cached := p.viewsCached && p.viewsKnown && !reset && !p.viewsReading
	p.viewsCached = false
	p.viewsReading = p.viewsReading || !cached // until read: this reload drops the last one's result
	var viewWant map[string]int
	if !reset {
		viewWant = p.viewsKnownCounts()
	}
	if reset {
		p.view, p.items, p.cursor, p.stalled = v, nil, "", false
		p.viewGroups = nil
		p.stopOlder()
		p.main.reset()
		p.closeSearch()
		p.draftList, p.draftSel = &ui.ListState{}, 0
		p.sending.ask = false // a warnings dialog does not outlive its view
		want, toEnd = 0, false
	}
	if p.searched {
		p.search()
	}
	p.loading = true
	hook := p.fetched
	var (
		folders      = map[int64][]core.Folder{}
		starredByAcc = map[int64]int{}
		starredTotal int
		outbox       []outboxEntry
		drafts       []core.Draft
		items        []mailItem
		cursor       string
		threads      bool
		saved        []core.SavedView
		viewGroups   []viewGroupState
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
		if starredByAcc, starredTotal, err = a.svc.StarredCounts(ctx); err != nil {
			return err
		}
		st, err := a.svc.GetSettings(ctx)
		if err != nil {
			return err
		}
		threads = st.ThreadView
		if !cached {
			if saved, err = a.svc.SavedViews(ctx); err != nil {
				return err
			}
		}
		draftsAcc, isDrafts := draftsOf(folders, v)
		switch {
		case v.Unified == "outbox" || v == (core.View{}):
		case viewList && cached:
		case viewList && !slices.ContainsFunc(saved, func(b core.SavedView) bool { return b.ID == savedViewID }): // deleted: viewsLoaded leaves it
		case viewList:
			items, viewGroups, err = fetchView(ctx, a.svc, savedViewID, viewWant)
		case isDrafts:
			drafts, err = a.svc.ListDrafts(ctx, draftsAcc)
			for i := range drafts { // shown, never saved: signature images as data: (richtext's only kind)
				if err == nil {
					drafts[i].HTML, err = a.svc.InlineSignatureImages(ctx, drafts[i].HTML)
				}
			}
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
		p.starredUnread, p.starredUnreadTotal = starredByAcc, starredTotal
		if viewList && cached {
			items, viewGroups, cursor = p.items, p.viewGroups, ""
		}
		p.items, p.cursor, p.stalled = items, cursor, false
		p.viewGroups = viewGroups
		if !cached {
			p.views, p.viewsKnown, p.viewsReading = saved, true, false
			p.viewsLoaded()
		}
		p.setDrafts()
		p.keepViewFresh()
	})
}

// keepViewFresh rereads an open view in a while, if nothing does sooner:
// its 等待回复 changes with time alone.
func (p *mailPage) keepViewFresh() {
	if p.viewRefresh != nil {
		p.viewRefresh()
		p.viewRefresh = nil
	}
	if p.savedViewID() != 0 {
		now := p.a.clock.Now()
		y, m, d := now.Date()
		midnight := time.Date(y, m, d+1, 0, 0, 1, 0, now.Location()) // 今天 turns over
		p.viewRefresh = p.a.after(min(viewRefreshEvery, midnight.Sub(now)), func() {
			p.viewRefresh = nil
			if p.savedViewID() != 0 {
				p.reload()
			}
		})
	}
}

// fetchItems reads pages from cursor until it has want items, or to the end
// with toEnd; at least one page. Items are unique by ID.
func fetchItems(ctx context.Context, svc Service, v core.View, threads bool, cursor string, want int, toEnd bool) ([]mailItem, string, error) {
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
			return out, cursor, withLabels(ctx, svc, out)
		}
	}
}

// loadMore appends the next page, unless one is loading already.
func (p *mailPage) loadMore() {
	if p.savedViewID() != 0 {
		return // each group loads on its own: viewLoadVisible
	}
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
	case p.filter == 0 || p.savedViewID() != 0: // a view says what it is about in its groups
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

// currentFolderRole returns the role of the folder currently shown (inbox, archive, junk, trash), or "".
func (p *mailPage) currentFolderRole() string {
	if p.view.Unified == "inbox" {
		return "inbox"
	}
	if f, ok := p.folder(p.view.FolderID); ok {
		return f.Role
	}
	return ""
}

// removes reports whether op takes the messages out of the view.
func removes(op string) bool {
	return op == "archive" || op == "delete" || op == "junk" || op == "inbox" || op == "purge"
}

// purgeDialog confirms Delete in Trash, which deletes permanently.
func (p *mailPage) purgeDialog(c *ui.Context) {
	if !p.purgeAsk {
		return
	}
	desc := i18n.T("mail.purge.desc")
	if slices.ContainsFunc(p.purgeRows, func(it mailItem) bool { return p.a.isArchiveID(p.accountOf(it.Msg.FolderID)) }) {
		desc = i18n.T("mail.archive.purge.desc") // no server copy, no undo
	}
	kit.Dialog(c, &p.purgeAsk, i18n.T("mail.purge.title"),
		kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: desc, Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { p.purgeAsk = false })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				p.purgeAsk = false
				p.batch("purge", p.purgeRows)
			})
		})
}

// localDrafts is the drafts entry of an account whose server has no drafts
// folder: drafts are local, so every account has one.
func localDrafts(acc int64) core.View {
	return core.View{Unified: "drafts:" + strconv.FormatInt(acc, 10)}
}

// accountStarred is the starred view of an account.
func accountStarred(acc int64) core.View {
	return core.View{Unified: "starred:" + strconv.FormatInt(acc, 10)}
}

// starredOf returns the account whose starred messages v lists.
func starredOf(v core.View) (int64, bool) {
	if id, ok := strings.CutPrefix(v.Unified, "starred:"); ok {
		n, err := strconv.ParseInt(id, 10, 64)
		return n, err == nil
	}
	return 0, false
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
	if acc, ok := starredOf(v); ok {
		return acc
	}
	if v.AccountID != 0 {
		return v.AccountID
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
	v := p.a.route.View
	if id, ok := savedViewOf(v); ok {
		if b, ok := p.savedView(id); ok {
			total := 0
			for _, g := range p.viewGroups {
				total += g.total
			}
			return b.Name, i18n.Tf("view.count", total)
		}
		return "", ""
	}
	if l, ok := p.a.label(v.LabelID); ok && v.LabelID != 0 {
		unread := l.Unread
		if v.AccountID != 0 {
			unread = p.accountLabelUnread(v.AccountID, v.LabelID)
		}
		return l.Name, unreadSub(unread)
	}
	switch {
	case v.Unified == "inbox":
		return i18n.T("mail.unified.inbox"), unreadSub(p.inboxUnread())
	case v.Unified == "starred" || strings.HasPrefix(v.Unified, "starred:"):
		return i18n.T("mail.unified.starred"), ""
	case v.Unified == "outbox":
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

func (p *mailPage) accountLabelUnread(accID, labelID int64) int {
	for _, l := range p.a.AccountLabels(accID) {
		if l.ID == labelID {
			return l.Unread
		}
	}
	return 0
}

// listTime is a row's time: HH:mm today, then yesterday, a weekday within
// this calendar week (from Monday), else localized date.
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
	return i18n.FormatDate(t, now)
}

// Content is the list column and the reader beside it, each a panel, the
// gap between them a grip that sets the list's width.
func (p *mailPage) Content(c *ui.Context) {
	a, th, r := p.a, c.Theme(), panelRadius(c)
	p.viewDeleteDialog(c)
	p.viewLeaveDialog(c)
	if e := p.viewEdit; e != nil {
		kit.Panel(c, r).Grow(1).Children(func() { e.view(c) })
		return
	}
	_, isDrafts := draftsOf(p.folders, p.view)
	a.listW = a.listWidth()
	changed, _ := kit.Resizer(c, &a.listW, th.Space(2), false, i18n.T("mail.resize_list"), nil, func() {
		kit.Panel(c, r).Grow(1).Children(func() {
			switch {
			case p.view.Unified == "outbox":
				p.outboxView(c)
			case isDrafts:
				p.draftsView(c)
			default:
				p.listView(c)
			}
		})
	}, func() {
		kit.Panel(c, r).Grow(1).Children(func() {
			if isDrafts {
				p.draftPane(c)
			} else {
				p.reader.view(c)
			}
		})
	})
	a.listW = min(max(a.listW, theme.ListMinWidth), theme.ListMaxWidth)
	if changed {
		a.saveWidths()
	}
}

// listHeader is the search box, or the first date group's heading (the
// list carries the later ones) with the all/unread tabs.
func (p *mailPage) listHeader(c *ui.Context, head string) {
	t, th := theme.Of(c), c.Theme()
	if p.searchOpen {
		ui.Column(c).Padding(th.Space(3), th.Space(3), th.Space(2), th.Space(3)).Gap(th.Space(2)).Children(func() {
			// the design's box is 32 inside its border (a content-box label)
			box := kit.InputGroup(c, kit.InputOpts{Glass: true}).Height(th.Space(8.5)).Gap(th.Space(2)).Padding(0, th.Space(1), 0, th.Space(2.5)).Radius(t.Radius.Lg)
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
			p.filterChips(c)
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				kit.Text(c, p.searchHint(), t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
				if p.savableSearch() {
					kit.Button(c, i18n.T("view.save_search"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Shrink(0).OnClick(p.viewFromSearch)
				}
			})
		})
		if p.searched && len(p.results) > 0 && p.hlQuery != "" {
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
		p.a.openPalette()
	}
	saved := p.savedViewID() != 0
	if saved && head == "" && !p.a.sidebarHidden { // its groups' headings are the list's
		return
	}
	ui.Row(c).AlignItems(ui.Center).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4)).Gap(th.Space(2)).Children(func() {
		kit.Text(c, head, t.Text.Base).FontWeight(500).TextColor(t.Muted)
		ui.Spacer(c)
		if !saved {
			kit.Tabs(c, &p.filter, i18n.T("mail.filter.all"), i18n.T("mail.filter.unread"))
		}
		if p.a.sidebarHidden { // else the sidebar's header has it
			kit.IconButton(c, "search", i18n.T("mail.search"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(p.a.openPalette)
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
	if acc := p.searchAccount(); acc != 0 {
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
	p.purgeDialog(c)
	// The header names the date group at the top of the list, as the list
	// scrolled last frame; that group's own heading, pinned under it, is
	// drawn clear so the name shows once.
	head := ""
	if len(rows) > 0 && !p.searched && !p.viewGrouped() {
		first, _ := ls.list.Visible()
		head = topGroup(p.a.clock.Now(), rows, es, first)
	}
	p.listHeader(c, head)
	ls.list.Key = func(i int) any {
		if e := es[i]; e.group != "" {
			return "g:" + e.group // two groups may share a name
		} else if e.head != "" {
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
	l := kit.List(c, ls.list, len(es), func(i int) {
		if es[i].group != "" {
			viewHeading(c, es[i])
			return
		}
		if es[i].head != "" { // a date heading: no row, never chosen
			// left as the header's name: the list's own Space(2) and Space(2) more
			h := kit.Text(c, es[i].head, t.Text.Base).FontWeight(500).Padding(th.Space(2), th.Space(2.5), th.Space(0.5), th.Space(2))
			if es[i].head == head { // the header names it: clear, the same size
				h.TextColor(ui.Transparent)
			} else {
				h.TextColor(t.Muted).Background(t.Background)
			}
			return
		}
		it := rows[es[i].row]
		p.item(c, it).ContextMenu(func(m *ui.Menu) {
			on := []mailItem{it}
			if ls.sel.Has(it.key()) {
				on = p.chosen(rows)
			}
			if len(on) == 1 && p.canReply(on[0]) {
				for _, mode := range []string{"reply", "reply_all", "forward"} {
					if m.Item(i18n.T("reader." + mode)).Icon(menuIcon(mailActionIcons[mode])).Chosen() {
						p.draft(on[0], mode)
					}
				}
				m.Separator()
			}
			// as S and U: read/star when any of them is unread/unstarred
			read, star := "unread", "unstar"
			if slices.ContainsFunc(on, func(r mailItem) bool { return r.Unread > 0 }) {
				read = "read"
			}
			if slices.ContainsFunc(on, func(r mailItem) bool { return r.Flagged < r.Count }) {
				star = "star"
			}
			for _, k := range []string{read, star} {
				why := p.cannot(on, k)
				if m.Item(i18n.T("mail.action." + k)).Icon(menuIcon(mailActionIcons[k])).Disabled(why != "").Subtitle(why).Chosen() {
					act, target = k, on
				}
			}
			m.Separator()

			role := p.currentFolderRole()
			for _, k := range []string{"inbox", "archive", "junk", "delete"} {
				if k != "delete" && role == k {
					continue // never show current folder as target
				}
				why := p.cannot(on, k)
				if m.Item(i18n.T("mail.action." + k)).Icon(menuIcon(mailActionIcons[k])).Disabled(why != "").Subtitle(why).Chosen() {
					act, target = k, on
				}
			}
			m.Separator()
			labelSubmenu(m, theme.Of(c), p.a.labels, func(id int64) (has, auto bool) {
				for _, item := range on {
					if al, ok := appliedLabel(item.Labels, id); ok {
						has, auto = true, al.Auto && len(on) == 1
					}
				}
				return has, auto
			}, func(id int64, set bool) { p.toggleLabel(on, id, set) })
			securitySubmenu(m, func(reason string) bool { return len(on) == 1 && on[0].Security.Reason == reason }, func(reason string) { p.applySecurity(on, reason) })
			m.Separator()
			p.viewMenu(m, on)
			if len(on) == 1 { // for mail like this one: a rule, or blocking its sender
				if role := p.currentFolderRole(); role != "sent" && role != "drafts" {
					m.Separator()
					if m.Item(i18n.T("mail.menu.rule")).Icon(menuIcon("filter")).Chosen() {
						p.ruleFrom(on[0])
					}
					if from := on[0].Msg.From; gomail.AddrKey(from) != "" && !slices.ContainsFunc(p.a.accounts, func(acc core.Account) bool {
						return gomail.AddrKey(from) == gomail.AddrKey(acc.Email)
					}) {
						if slices.Contains(p.a.blocked, gomail.AddrKey(from)) {
							if m.Item(i18n.T("reader.unblock_sender")).Icon(menuIcon("shield")).Chosen() {
								p.a.setBlocked(from, false, func() { p.a.toast(i18n.T("reader.sender_unblocked")) })
							}
						} else if m.Item(i18n.T("reader.block_sender")).Icon(menuIcon("spam")).Chosen() {
							p.a.setBlocked(from, true, func() { p.a.toast(i18n.T("reader.sender_blocked")) })
						}
					}
				}
			}
			if p.a.dev { // dev builds: the message's original HTML, for debugging the renderer
				m.Separator()
				if m.Item("复制 HTML 路径（dev）").Chosen() {
					p.devHTML(it)
				}
				if m.Item("预览验证码浮窗（dev）").Chosen() {
					p.devSignIn(it, false)
				}
				if m.Item("预览登录链接浮窗（dev）").Chosen() {
					p.devSignIn(it, true)
				}
			}
		})
	}).Grow(1).Padding(0, th.Space(2)).Gap(theme.Stroke) // rows a hairline apart: two chosen ones stay two
	if p.devPath != "" { // written off the UI thread; the clipboard needs a frame
		c.WriteClipboard(p.devPath)
		p.a.toast(p.devPath)
		p.devPath = ""
	}
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
			} else if p.savedViewID() != 0 {
				p.viewEmpty(c)
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
	}{{0, ui.KeyE, "archive"}, {ui.Shift, ui.Key3, "delete"}, {0, ui.KeyBackspace, "delete"}, {0, ui.KeyDelete, "delete"}, {0, ui.KeyU, "toggle_read"}, {0, ui.KeyS, "toggle_star"}} {
		if ls.list.Shortcut(c, k.mods, k.key) {
			act, target = k.act, p.chosen(rows)
		}
	}
	if act != "" {
		p.batch(act, target)
	}
	if p.older.busy || p.older.err != nil {
		p.olderRow(c)
	}
	first, last := ls.list.Visible()
	if last >= len(es)-5 {
		p.loadMore()
		p.loadOlder()
	}
	if p.savedViewID() != 0 && !p.searched {
		p.viewLoadVisible(rows, es, first, last)
	}
}

// olderLoad is a folder list's fetch of the older mail past the sync range,
// once the list shows all it stores: one at a time, stopped when the view
// changes. The unified views and search don't fetch: their rows mix folders.
type olderLoad struct {
	gen    uint64 // bumped per fetch and on leaving; older answers are dropped
	folder int64
	busy   bool
	err    error
	cancel context.CancelFunc
	done   map[int64]bool // folders whose server has nothing older, this session
	notYet int64          // a folder whose first sync is under way: its next SyncFinished asks again
}

// olderFolder is the folder whose older mail the list may fetch, or 0.
func (p *mailPage) olderFolder() int64 {
	if _, drafts := draftsOf(p.folders, p.view); drafts || p.view.FolderID == 0 {
		return 0
	}
	return p.view.FolderID
}

// loadOlder fetches the next page of older mail from the server when the
// list reached its end locally, unless one is under way, failed (重试 asks
// again) or the server has nothing older.
func (p *mailPage) loadOlder() {
	f, o := p.olderFolder(), &p.older
	if f == 0 || p.loading || p.stalled || p.cursor != "" || p.searched || p.filter != 0 || o.busy || o.err != nil || o.done[f] || o.notYet == f {
		return
	}
	a := p.a
	ctx, cancel := context.WithCancel(a.ctx)
	o.gen++
	gen := o.gen
	o.folder, o.busy, o.cancel = f, true, cancel
	var more bool
	a.do(func(context.Context) (err error) {
		more, err = a.svc.LoadOlder(ctx, f)
		return err
	}, func(err error) {
		cancel()
		if gen != o.gen {
			return
		}
		o.busy, o.cancel = false, nil
		switch {
		case errors.Is(err, core.ErrNotSynced): // not an error: the sync fills it first
			o.notYet = f
			return
		case err != nil:
			o.err = err
		case !more:
			if o.done == nil {
				o.done = map[int64]bool{}
			}
			o.done[f] = true
		}
		p.reload() // the list shows what came in
	})
}

// stopOlder cancels the fetch of older mail; leaving the folder drops its error.
func (p *mailPage) stopOlder() {
	o := &p.older
	if o.cancel != nil {
		o.cancel()
	}
	o.gen++
	o.busy, o.err, o.cancel = false, nil, nil
}

// olderRow is the row under the list while older mail loads, or the error
// with 重试.
func (p *mailPage) olderRow(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Height(theme.OlderRowHeight).Padding(0, th.Space(4)).Gap(th.Space(2)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		if p.older.busy {
			kit.Spinner(c)
			kit.Text(c, i18n.T("mail.older.loading"), t.Text.Sm).TextColor(t.Muted)
			return
		}
		kit.Text(c, i18n.Tf("mail.older.failed", humanize(p.older.err)), t.Text.Sm).TextColor(t.Muted).Shrink(1).SingleLine()
		kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() {
			p.older.err = nil
			p.loadOlder()
		})
	})
}

// listEntry is a line of the list: a date heading (head), or the row
// rows[row].
type listEntry struct {
	head string
	row  int

	group string // a view group's heading: its id, and its total
	count int
}

// entries are the list's lines: each group of rows under its date heading
// (but the first, whose heading the list header shows), a search's results
// as they are. The list's own choice (cur) counts these.
func (p *mailPage) entries(rows []mailItem) []listEntry {
	if p.viewGrouped() {
		return p.viewEntries(rows)
	}
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

// topGroup is the date group of entry first, the list's top: its heading's,
// or its row's; the first row's when first is out of range.
func topGroup(now time.Time, rows []mailItem, es []listEntry, first int) string {
	e := listEntry{row: 0}
	if first > 0 && first < len(es) {
		e = es[first]
	}
	if e.head != "" {
		return e.head
	}
	return dateGroup(now, rows[e.row].Date)
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
	col := ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(2), th.Space(3))
	return col.Children(func() {
		ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, p.resultWho(it)).SingleLine().Shrink(1)
			securityIcon(c, p.a.settings, it.Security)
			ui.Spacer(c)
			p.starButton(c, it, col.Hovered())
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
		p.a.rowChips(c, it.Labels)
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
	title := i18n.Tf("mail.search.empty_title", q)
	if q == "" { // filters alone: nothing to hand Wit
		title = i18n.T("mail.search.empty_filtered")
	}
	kit.Empty(c, kit.EmptyOpts{Icon: "search", Title: title, Desc: p.searchedDesc(), Actions: func() {
		if q != "" {
			kit.EmptyAction(c, i18n.T("mail.search.ask_wit"), kit.ButtonOpts{}).OnClick(func() { p.a.askWit(q) })
		}
		kit.EmptyAction(c, i18n.T("mail.search.expand"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(p.a.openAccountSettings(p.searchAccount()))
	}}).Margin(th.Space(8), th.Space(1))
}

// searchedDesc says what a search covered: the searched accounts' sync range
// when they share one that is not all mail.
func (p *mailPage) searchedDesc() string {
	acc, ranges := p.searchAccount(), map[core.SyncRange]bool{}
	for _, a := range p.a.accounts {
		if acc == 0 || a.ID == acc {
			ranges[a.Sync] = true
		}
	}
	if len(ranges) == 1 {
		for r := range ranges {
			if r != (core.SyncRange{}) {
				return i18n.Tf("mail.search.empty_desc_range", rangeLabel(r))
			}
		}
	}
	return i18n.T("mail.search.empty_desc")
}

// starButton is a direct star/unstar toggle button on a mail row. As S, it
// unstars only a row all starred; an unstarred row's shows on hover or focus,
// always there for the keyboard and assistive technology.
func (p *mailPage) starButton(c *ui.Context, it mailItem, hovered bool) {
	t := theme.Of(c)
	starred := it.Flagged == it.Count
	icon, label := "star", i18n.T("mail.action.star")
	if starred {
		icon, label = "star-fill", i18n.T("mail.action.unstar")
	}
	b := kit.IconButton(c, icon, label, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs})
	if it.Flagged > 0 {
		b.TextColor(t.Attention)
	} else if !hovered && !b.FocusVisible() {
		b.Opacity(0)
	}
	b.OnClick(func() {
		p.batch("toggle_star", []mailItem{it})
	})
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
	col := ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(2.5), th.Space(2.5), th.Space(2.5), th.Space(5))
	return col.Children(func() {
		if it.Unread > 0 {
			ui.Box(c).Absolute().Left(th.Space(1.5)).Top(th.Space(4)).Size(th.Space(2), th.Space(2)).Radius(th.Space(1)).Background(t.Unread).
				Label(i18n.Tf("mail.row.unread", it.Unread))
		}
		subject := it.Subject
		if subject == "" {
			subject = i18n.T("mail.no_subject")
		}
		subjectFirst := p.a.settings.ListRowOrder == core.ListRowSubject
		// who is who the mail is with, its count and security mark: the line's lead, or its second line.
		who := func() {
			ui.Text(c, it.Who).FontWeight(weight).SingleLine().Shrink(1)
			if it.Thread && it.Count > 1 { // how many messages the conversation holds, as the reader's header says
				ui.Badge(c, strconv.Itoa(it.Count)).Label(i18n.Tf("reader.thread_count", it.Count))
			}
			securityIcon(c, p.a.settings, it.Security)
		}
		ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			if subjectFirst {
				ui.Text(c, subject).FontWeight(weight).SingleLine().Shrink(1)
			} else {
				who()
			}
			ui.Spacer(c)
			p.starButton(c, it, col.Hovered())
			if it.Attach {
				ui.Icon(c, icons.Get("paperclip")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted).Label(i18n.T("mail.attachment"))
			}
			kit.Text(c, listTime(p.a.clock.Now(), it.Date), small).TextColor(t.Muted)
		})
		if subjectFirst {
			ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(who)
		} else {
			ui.Text(c, subject).FontWeight(weight).SingleLine()
		}
		if icon, line := p.viewLine(it); line != "" {
			ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).TextColor(t.Muted).Children(func() {
				ui.Icon(c, icons.Get(icon)).Size(theme.IconXs, theme.IconXs).Shrink(0)
				kit.Text(c, line, t.Text.Base).SingleLine()
			})
		} else if it.Snippet != "" {
			kit.Text(c, it.Snippet, t.Text.Base).TextColor(t.Muted).SingleLine()
		}
		p.a.rowChips(c, it.Labels)
	})
}

// runSearch searches the synced mail of the view's account (every
// account's in a unified view) or the account chip's, with the filters.
func (p *mailPage) runSearch() {
	p.found.reset() // new results, a new choice
	p.search()
}

// search runs the query; only the newest request's answer shows, and none
// once the search is left.
func (p *mailPage) search() {
	p.searchGen++
	sq := p.searchQuery()
	if sq.Text == "" && !sq.Filtered() {
		p.searched, p.results = false, nil
		return
	}
	a := p.a
	q, gen := sq.Text, p.searchGen
	var found []core.MessageSummary
	labels := map[int64][]core.AppliedLabel{}
	a.do(func(ctx context.Context) (err error) {
		if found, err = a.svc.SearchWith(ctx, sq, searchLimit); err != nil {
			return err
		}
		items := make([]mailItem, len(found))
		for i, m := range found {
			items[i] = messageItem(m)
		}
		if err = withLabels(ctx, a.svc, items); err != nil {
			return err
		}
		for i := range found {
			labels[found[i].ID] = items[i].Labels
		}
		return nil
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
			it.Labels = labels[m.ID]
			if p.found.cur < 0 && (m.ID == r.MessageID || r.MessageID == 0 && m.ThreadID == r.ThreadID && r.ThreadID != 0) {
				p.found.cur = i // the mail open stays chosen among the results
				p.found.sel.Add(it.key())
			}
			p.results = append(p.results, it)
		}
		p.searched = true
	})
}

// clearSearch empties the box; the search stays open, the filters set
// still apply.
func (p *mailPage) clearSearch() {
	p.query = ""
	p.runSearch()
}

func (p *mailPage) closeSearch() {
	p.searchGen++
	p.searchOpen, p.searched, p.query, p.results = false, false, "", nil
	p.sf = searchFilters{}
	p.found.reset()
}

// canReply reports whether replying to it is sensible: never in junk or trash.
func (p *mailPage) canReply(it mailItem) bool {
	if role := p.currentFolderRole(); role == "junk" || role == "trash" {
		return false
	}
	if it.Msg.FolderID != 0 {
		if f, ok := p.folder(it.Msg.FolderID); ok && (f.Role == "junk" || f.Role == "trash") {
			return false
		}
	}
	return true
}

// draft starts a reply, reply-all or forward of it and opens it in a compose window.
// ruleFrom opens the rules editor on a rule for mail like it (a thread's
// first message).
func (p *mailPage) ruleFrom(it mailItem) {
	a := p.a
	open := func(m core.MessageSummary) {
		a.pages[PageSettings].(*settingsPage).rules.pending = &m
		a.navigate(Route{Page: PageSettings, Settings: "rules"})
	}
	if !it.Thread {
		open(it.Msg)
		return
	}
	var m core.MessageSummary
	a.do(func(ctx context.Context) error {
		th, err := a.svc.GetThread(ctx, it.ID)
		if err == nil && len(th.Messages) == 0 {
			err = fmt.Errorf("thread %d has no messages", it.ID)
		}
		if err == nil {
			m = th.Messages[0]
		}
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		open(m)
	})
}

func (p *mailPage) draft(it mailItem, mode string) {
	if !it.Thread {
		p.a.draft(it.Msg, p.accountOf(it.Msg.FolderID), mode)
		return
	}
	if p.reader.key[0] == it.ID && p.reader.key[1] == 0 && p.reader.loaded {
		if _, ok := p.reader.last(); ok {
			p.reader.draft(mode)
			return
		}
	}
	var id int64
	p.a.do(func(ctx context.Context) (err error) {
		th, err := p.a.svc.GetThread(ctx, it.ID)
		if err != nil {
			return err
		}
		m, ok := draftSource(th.Messages, th.ReplyTarget, mode)
		if !ok {
			return fmt.Errorf("thread %d has no messages", it.ID)
		}
		acc, err := folderAccount(ctx, p.a.svc, m.FolderID)
		if err != nil {
			return err
		}
		d, err := p.a.svc.NewDraft(ctx, acc, mode, m.ID)
		if err != nil {
			return err
		}
		id = d.ID
		return nil
	}, func(err error) {
		if err != nil {
			p.a.toastErr(err)
			return
		}
		p.a.plat.OpenCompose(id)
	})
}

// rowMessageIDs resolves rows into their underlying message IDs, expanding threads.
func (p *mailPage) rowMessageIDs(ctx context.Context, rows []mailItem) ([]int64, error) {
	var ids []int64
	for _, it := range rows {
		if !it.Thread {
			ids = append(ids, it.ID)
			continue
		}
		th, err := p.a.svc.GetThread(ctx, it.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range th.Messages {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// toggleLabel adds or removes a label on the given rows.
func (p *mailPage) toggleLabel(rows []mailItem, labelID int64, on bool) {
	if len(rows) == 0 {
		return
	}
	for i := range p.items {
		for _, it := range rows {
			if p.items[i].ID == it.ID && p.items[i].Thread == it.Thread {
				if on {
					if !slices.ContainsFunc(p.items[i].Labels, func(l core.AppliedLabel) bool { return l.LabelID == labelID }) {
						p.items[i].Labels = append(p.items[i].Labels, core.AppliedLabel{LabelID: labelID})
					}
				} else {
					p.items[i].Labels = slices.DeleteFunc(p.items[i].Labels, func(l core.AppliedLabel) bool { return l.LabelID == labelID })
				}
			}
		}
	}
	p.a.writes.Chain("labels", func(ctx context.Context) error {
		ids, err := p.rowMessageIDs(ctx, rows)
		if err != nil {
			return err
		}
		return p.a.svc.SetLabel(ctx, ids, labelID, on)
	}, p.a.toastErr)
}

// applySecurity sets or resets the manual security classification on the given rows.
func (p *mailPage) applySecurity(rows []mailItem, reason string) {
	if len(rows) == 0 {
		return
	}
	status, _ := gomail.ManualSecurityStatus(reason)
	sec := gomail.DBToSecurityReport("", "", status, reason)
	for i := range p.items {
		for _, it := range rows {
			if p.items[i].ID == it.ID && p.items[i].Thread == it.Thread {
				p.items[i].Security = sec
			}
		}
	}
	p.a.writes.Chain("security", func(ctx context.Context) error {
		ids, err := p.rowMessageIDs(ctx, rows)
		if err != nil {
			return err
		}
		return p.a.svc.SetMessageSecurity(ctx, ids, reason)
	}, func(err error) {
		if err != nil {
			p.a.toastErr(err)
		} else {
			p.a.toast(i18n.T("reader.security.updated"))
		}
	})
}

// batch applies act to rows: threads become their messages (GetThread),
// grouped by account, one call per account. Archiving and deleting take
// only the messages in the view; an account without an archive (junk)
// folder is skipped and named.
func (p *mailPage) batch(act string, rows []mailItem) {
	saved := p.savedViewID() != 0 && !p.searched // a view's rows are whole conversations, wherever their mail is
	if act == "archive" && saved {
		p.archiveThreads(rows)
		return
	}
	p.batchScoped(act, rows, p.searched || saved) // a search's results are in view wherever they are
}

// batchScoped is batch whatever the list shows: with anywhere every message
// counts as in view, as the palette's hits, which are in no view, need.
func (p *mailPage) batchScoped(act string, rows []mailItem, anywhere bool) {
	if len(rows) == 0 {
		return
	}
	if act == "delete" && p.currentFolderRole() == "trash" {
		p.purgeAsk, p.purgeRows = true, rows
		return
	}
	a := p.a
	acctOf, role, archive, inbox, junk := map[int64]int64{}, map[int64]string{}, map[int64]int64{}, map[int64]int64{}, map[int64]bool{}
	for acc, fs := range p.folders {
		for _, f := range fs {
			acctOf[f.ID], role[f.ID] = acc, f.Role
			if f.Role == "archive" && archive[acc] == 0 {
				archive[acc] = f.ID
			}
			if f.Role == "inbox" && inbox[acc] == 0 {
				inbox[acc] = f.ID
			}
			junk[acc] = junk[acc] || f.Role == "junk"
		}
	}
	names := map[int64]string{}
	for _, acc := range a.accounts {
		names[acc.ID] = acc.Email
	}
	v, search := p.view, anywhere
	saved := p.savedViewID() != 0
	var labeled map[int64][]core.AppliedLabel // a label view's messages' labels
	inView := func(m core.MessageSummary) bool {
		switch {
		case search:
			return true
		case v.LabelID != 0:
			if v.AccountID != 0 && acctOf[m.FolderID] != v.AccountID {
				return false
			}
			return slices.ContainsFunc(labeled[m.ID], func(l core.AppliedLabel) bool { return l.LabelID == v.LabelID })
		case v.Unified == "inbox":
			return slices.ContainsFunc(m.FolderIDs, func(f int64) bool { return role[f] == "inbox" })
		case v.Unified == "starred":
			return slices.Contains(m.Flags, `\Flagged`)
		case strings.HasPrefix(v.Unified, "starred:"):
			acc, ok := starredOf(v)
			return ok && acctOf[m.FolderID] == acc && slices.Contains(m.Flags, `\Flagged`)
		}
		return slices.Contains(m.FolderIDs, v.FolderID)
	}
	l, exec := p.ls(), p.exec
	var op string
	failed, byAcc := 0, map[int64][]int64{}
	start := a.clock.Now() // a purge's undo window starts after this
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
		if v.LabelID != 0 && !search {
			ids := make([]int64, len(msgs))
			for i, m := range msgs {
				ids[i] = m.ID
			}
			var err error
			if labeled, err = a.svc.MessageLabels(ctx, ids); err != nil {
				return err
			}
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
			if removes(op) && !inView(m) {
				continue
			}
			if saved && (op == "junk" || op == "inbox") && role[m.FolderID] == "sent" {
				continue // a whole conversation moves, but what the user sent stays sent
			}
			if m.FolderID == 0 {
				continue // Gmail kept it with no visible label; nothing on the server to change
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
		var purged []int64
		var archived []core.ArchiveUndo
		finish := func() {
			if failed > 0 {
				a.toastAs(kit.ToastError, i18n.Tf("mail.failed", failed))
			}
			if len(skipped) > 0 {
				a.toastAs(kit.ToastWarning, i18n.Tf("mail.no_"+op, strings.Join(skipped, i18n.T("mail.list_sep"))))
			}
			if removes(op) {
				l.sel.Clear()
			}
			if op == "purge" && len(purged) > 0 {
				p.offerUndoPurge(i18n.Tf("mail.purge.toast", len(purged)), purged, start)
			}
			if op == "archive" {
				p.offerUndoArchive(archived, start)
			}
		}
		for _, acc := range slices.Sorted(maps.Keys(byAcc)) {
			ids := byAcc[acc]
			if op == "junk" && !junk[acc] {
				skipped = append(skipped, names[acc])
				continue
			}
			if why := a.unsupported(acc, core.ActionFeatures[op]); why != "" {
				a.toastAs(kit.ToastWarning, names[acc]+": "+why)
				continue
			}
			pending++
			folder, from := archive[acc], int64(0)
			if op == "inbox" {
				folder = inbox[acc]
			}
			if !search {
				from = v.FolderID
			}
			var undo core.ArchiveUndo
			a.writes.Chain(fmt.Sprintf("account:%d", acc), func(ctx context.Context) (err error) {
				undo, err = exec(ctx, op, ids, from, folder)
				return err
			}, func(err error) {
				if _, noFolder := errors.AsType[*core.ErrNoRoleFolder](err); noFolder && op == "archive" {
					skipped = append(skipped, names[acc])
				} else if err != nil {
					failed += len(ids)
				} else if op == "purge" && !a.isArchiveID(acc) { // an archive's are gone at once: no undo
					purged = append(purged, ids...)
				} else if op == "archive" {
					archived = append(archived, undo)
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

// offerUndoPurge follows a permanent delete of ids queued after start: the
// reader closes if it showed only them, and a toast titled title offers to undo
// it for what is left of its window.
func (p *mailPage) offerUndoPurge(title string, ids []int64, start time.Time) {
	a := p.a
	p.closeReaderOf(ids)
	left := start.Add(core.UndoPurgeWindow).Sub(a.clock.Now())
	if left <= 0 {
		a.toast(title)
		return
	}
	a.toasts = append(a.toasts, ui.Toast{ID: fmt.Sprintf("purge-%d-%d", start.UnixNano(), ids[0]), Title: title,
		Action: i18n.T("compose.undo"), OnAction: func() { p.undoPurge(ids) }, Timeout: left})
}

// closeReaderOf closes the reader when what it shows is all among ids: the
// message it opened, else every message of its thread.
func (p *mailPage) closeReaderOf(ids []int64) {
	a := p.a
	if a.route.Page != PageMail || a.route.ThreadID == 0 {
		return
	}
	shown := p.reader.threadIDs
	if a.route.MessageID != 0 {
		shown = []int64{a.route.MessageID}
	}
	if len(shown) == 0 || slices.ContainsFunc(shown, func(id int64) bool { return !slices.Contains(ids, id) }) {
		return
	}
	a.setRoute(Route{Page: PageMail, View: a.route.View}) // in place: back must not reopen it
}

// undoPurge takes back the permanent delete of ids.
func (p *mailPage) undoPurge(ids []int64) {
	a := p.a
	var gone int
	a.do(func(ctx context.Context) (err error) {
		gone, err = a.svc.UndoPurge(ctx, ids)
		return err
	}, func(err error) {
		switch {
		case errors.Is(err, core.ErrPurgeTooLate):
			a.toastAs(kit.ToastWarning, i18n.T("mail.purge.too_late"))
		case err != nil:
			a.toastErr(err)
		case gone > 0:
			a.toastAs(kit.ToastWarning, i18n.Tf("mail.purge.partly_undone", gone))
		default:
			a.toast(i18n.T("mail.purge.undone"))
		}
		p.reload()
	})
}

// devHTML writes the original HTML, as received and unsanitized, of it (a
// thread's newest message) to a temporary file, for the list to copy its path.
// devSignIn shows the sign-in banner for the message, with its own code or
// link when it has one, else a sample, so the banner can be seen without
// waiting for real sign-in mail. Dev builds only.
func (p *mailPage) devSignIn(it mailItem, link bool) {
	a := p.a
	var code core.SignInCode
	a.do(func(ctx context.Context) error {
		id := it.ID
		if it.Thread {
			t, err := a.svc.GetThread(ctx, it.ID)
			if err != nil || len(t.Messages) == 0 {
				return err
			}
			id = t.Messages[len(t.Messages)-1].ID
		}
		m, err := a.svc.GetMessage(ctx, id, false)
		if err != nil {
			return err
		}
		code = core.SignInCode{MessageID: id, From: m.From, ReceivedAt: a.clock.Now()}
		if link {
			code.Link, code.LinkKind = gomail.ActionLink(m.Subject, m.Text, m.HTML)
			if code.Link == "" {
				code.Link, code.LinkKind = "https://example.com/magic-link", gomail.LinkSignIn
			}
		} else {
			code.Code = cmp.Or(gomail.SignInCode(m.Subject, m.Text, m.HTML), "482913")
		}
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else if code.MessageID != 0 {
			a.plat.ShowSignInCode(&code)
		}
	})
}

func (p *mailPage) devHTML(it mailItem) {
	a := p.a
	var path string
	a.do(func(ctx context.Context) error {
		id := it.ID
		if it.Thread {
			t, err := a.svc.GetThread(ctx, it.ID)
			if err != nil {
				return err
			}
			if len(t.Messages) == 0 {
				return nil
			}
			id = t.Messages[len(t.Messages)-1].ID
		}
		var html string
		if raw, err := rawMessage(ctx, a.svc, id); err == nil {
			parsed, err := gomail.Parse(raw)
			if err != nil {
				return err
			}
			html = parsed.HTML
		} else if html, err = a.svc.StoredHTML(ctx, id); err != nil { // offline, or no credentials (a synced dev database)
			return err
		}
		dir, err := a.tempDir(a.plat.TempDir())
		if err != nil {
			return err
		}
		path = filepath.Join(dir, fmt.Sprintf("message-%d.html", id))
		return os.WriteFile(path, []byte(html), 0o600)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		p.devPath = path
	})
}

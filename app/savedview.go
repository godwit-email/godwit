package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// viewUndoWindow is how long the toast after an archive or 移出 offers to undo it.
const viewUndoWindow = core.UndoArchiveWindow

// viewRefreshEvery rereads an open view with nothing else changing,
// so that 等待回复 shows on time.
const viewRefreshEvery = time.Hour

// savedRoute is the mail page's view of a view.
func savedRoute(id int64) core.View {
	return core.View{Unified: "view:" + strconv.FormatInt(id, 10)}
}

// savedViewOf returns the view v lists.
func savedViewOf(v core.View) (int64, bool) {
	if id, ok := strings.CutPrefix(v.Unified, "view:"); ok {
		n, err := strconv.ParseInt(id, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// savedViewID is the view the list shows, 0 for none.
func (p *mailPage) savedViewID() int64 {
	id, _ := savedViewOf(p.view)
	return id
}

func (p *mailPage) savedView(id int64) (core.SavedView, bool) {
	i := slices.IndexFunc(p.views, func(b core.SavedView) bool { return b.ID == id })
	if i < 0 {
		return core.SavedView{}, false
	}
	return p.views[i], true
}

// viewGrouped is whether the list shows a view's groups under their
// headings; without groups it is a flat list, under date headings like any other.
func (p *mailPage) viewGrouped() bool {
	if p.searched {
		return false
	}
	b, ok := p.savedView(p.savedViewID())
	return ok && len(b.Doc.Groups) > 0
}

// goneView reports a route to a view that no longer exists.
func (a *App) goneView(r Route) bool {
	id, ok := savedViewOf(r.View)
	if !ok || r.Page != PageMail || !a.mail.viewsKnown {
		return false
	}
	_, ok = a.mail.savedView(id)
	return !ok
}

// viewsLoaded leaves a view that was deleted, as labelsLoaded does a label.
func (p *mailPage) viewsLoaded() {
	a := p.a
	a.history, a.ahead = slices.DeleteFunc(a.history, a.goneView), slices.DeleteFunc(a.ahead, a.goneView)
	if a.goneView(a.route) {
		a.setRoute(a.route) // to the inbox
	}
}

// viewGroupState is a group of the list: how far it has loaded.
type viewGroupState struct {
	id, name string
	total    int
	cursor   string
}

// viewItem is a view's conversation as a row of group.
func viewItem(r core.ViewRow, group string) mailItem {
	it := threadItem(r.ThreadSummary)
	it.Group, it.Status, it.NewReply, it.WaitingDays, it.Manual = group, r.Status, r.NewReply, r.WaitingDays, r.Manual
	return it
}

// fetchView reads a view's groups, each up to the rows want has for it.
func fetchView(ctx context.Context, svc Service, id int64, want map[string]int) ([]mailItem, []viewGroupState, error) {
	cs, err := svc.ViewContents(ctx, id, pageSize)
	if err != nil {
		return nil, nil, err
	}
	var items []mailItem
	groups := make([]viewGroupState, 0, len(cs.Groups))
	for _, g := range cs.Groups {
		rows, cursor := g.Items, g.NextCursor
		for cursor != "" && len(rows) < want[g.ID] {
			next, err := svc.ViewGroupPage(ctx, id, g.ID, cursor, pageSize)
			if err != nil {
				return nil, nil, err
			}
			rows, cursor = append(rows, next.Items...), next.NextCursor
		}
		for _, r := range rows {
			items = append(items, viewItem(r, g.ID))
		}
		groups = append(groups, viewGroupState{id: g.ID, name: g.Name, total: g.Total, cursor: cursor})
	}
	return items, groups, withLabels(ctx, svc, items)
}

// viewsKnownCounts is how many rows each group has loaded, for a reread to keep.
func (p *mailPage) viewsKnownCounts() map[string]int {
	out := map[string]int{}
	for _, it := range p.items {
		out[it.Group]++
	}
	return out
}

// viewMore loads the next page of group, unless a page is loading already.
func (p *mailPage) viewMore(group string) {
	i := slices.IndexFunc(p.viewGroups, func(g viewGroupState) bool { return g.id == group })
	if i < 0 || p.viewGroups[i].cursor == "" || p.list.loading || p.stalled {
		return
	}
	a := p.a
	id, cursor := p.savedViewID(), p.viewGroups[i].cursor
	var page core.ViewGroupPage
	var got []mailItem
	p.list.Run(a, func(ctx context.Context) (err error) {
		if page, err = a.svc.ViewGroupPage(ctx, id, group, cursor, pageSize); err != nil {
			return err
		}
		for _, r := range page.Items {
			got = append(got, viewItem(r, group))
		}
		return withLabels(ctx, a.svc, got)
	}, func(err error) {
		if err != nil {
			p.stalled = true
			a.toastErr(err)
			return
		}
		at, have := 0, map[int64]bool{}
		for j, it := range p.items {
			have[it.ID] = true
			if it.Group == group {
				at = j + 1
			}
		}
		got = slices.DeleteFunc(got, func(it mailItem) bool { return have[it.ID] })
		p.items = slices.Insert(p.items, at, got...)
		if j := slices.IndexFunc(p.viewGroups, func(g viewGroupState) bool { return g.id == group }); j >= 0 {
			p.viewGroups[j].cursor = page.NextCursor
		}
	})
}

// viewLoadVisible loads more of a group whose last loaded row is in or near view.
func (p *mailPage) viewLoadVisible(rows []mailItem, es []listEntry, first, last int) {
	for i := max(first, 0); i <= min(last+5, len(es)-1); i++ {
		if es[i].head != "" {
			continue
		}
		g := rows[es[i].row].Group
		next := slices.IndexFunc(es[i+1:], func(e listEntry) bool { return e.head == "" })
		if next >= 0 && rows[es[i+1+next].row].Group == g {
			continue
		}
		if j := slices.IndexFunc(p.viewGroups, func(s viewGroupState) bool { return s.id == g }); j >= 0 && p.viewGroups[j].cursor != "" {
			p.viewMore(g)
			return
		}
	}
}

// viewEntries are the list's lines of a view with groups: each group under
// its heading and count.
func (p *mailPage) viewEntries(rows []mailItem) []listEntry {
	var out []listEntry
	for _, g := range p.viewGroups {
		out = append(out, listEntry{head: cmp.Or(g.name, i18n.T("view.other")), group: g.id, count: g.total})
		for i, it := range rows {
			if it.Group == g.id {
				out = append(out, listEntry{row: i})
			}
		}
	}
	return out
}

// viewHeading is a group's heading in the list, pinned as a date heading is.
func viewHeading(c *ui.Context, e listEntry) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(2.5), th.Space(1), th.Space(2)).
		Background(t.Background).Children(func() {
		kit.Text(c, e.head, t.Text.Base).FontWeight(500).TextColor(t.Muted).SingleLine().Grow(1).Shrink(1)
		kit.Text(c, strconv.Itoa(e.count), t.Text.Sm).TextColor(t.Muted).Shrink(0)
	})
}

// viewDaysAgo is how many calendar days t is before now.
func viewDaysAgo(now, t time.Time) int {
	day := func(x time.Time) time.Time {
		y, m, d := x.In(now.Location()).Date()
		return time.Date(y, m, d, 12, 0, 0, 0, now.Location()) // noon: a DST change stays under half a day
	}
	return int(day(now).Sub(day(t)).Hours()/24 + 0.5)
}

// viewLine is a row's status for its third line, "" for none: the row shows
// its preview instead. Only a view's rows have one.
func (p *mailPage) viewLine(it mailItem) (icon, text string) {
	switch {
	case it.Status == core.ReplyStatusUnreplied && it.NewReply:
		return "reply", i18n.T("view.status.new_reply")
	case it.Status == core.ReplyStatusUnreplied:
		switch n := viewDaysAgo(p.a.clock.Now(), it.Date); {
		case n <= 0:
			return "reply", i18n.T("view.status.unreplied.today")
		case n == 1:
			return "reply", i18n.T("view.status.unreplied.yesterday")
		default:
			return "reply", i18n.Tf("view.status.unreplied.days", n)
		}
	case it.Status == core.ReplyStatusWaiting:
		return "time", i18n.Tf("view.status.waiting", it.WaitingDays)
	case it.Manual:
		return "plus", i18n.T("view.status.manual")
	}
	return "", ""
}

// viewEmpty is an empty view's list.
func (p *mailPage) viewEmpty(c *ui.Context) {
	kit.Empty(c, kit.EmptyOpts{Icon: "inbox", Title: i18n.T("view.empty"), Desc: i18n.T("view.empty.desc")}).Margin(c.Theme().Space(8), c.Theme().Space(1))
}

// viewNav is the sidebar's 视图: each view with its icon, name, unread
// count and menu; its header's menu makes one (viewsMenu). It returns the header.
func (p *mailPage) viewNav(c *ui.Context) ui.Element {
	th := c.Theme()
	head := kit.SidebarCollapsible(c, &p.viewsOpen, true, func() {
		ui.Text(c, i18n.T("view.title")).SingleLine().Grow(1).Shrink(1)
	}, func() {
		for _, b := range p.views {
			p.viewRow(c.Key(b.ID), b)
		}
	})
	ui.Box(c).Height(th.Space(2))
	return head
}

// viewsMenu is the views header's own menu item: make a view.
func (p *mailPage) viewsMenu(m *ui.Menu) {
	if m.Item(i18n.T("view.new")).Icon(menuIcon("plus")).Chosen() {
		p.openViewEditor(newViewEditor(p))
	}
}

func (p *mailPage) viewRow(c *ui.Context, b core.SavedView) {
	t := theme.Of(c)
	v := savedRoute(b.ID)
	on := p.viewShown(v)
	item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, Covered: p.viewEdit != nil, OnClick: func() { p.a.navigate(Route{Page: PageMail, View: v}) }}).Label(b.Name)
	item.Children(func() {
		ui.Icon(c, icons.Get(labelIcon(b.Icon))).Size(theme.Icon, theme.Icon).Shrink(0).TextColor(t.Muted)
		ui.Text(c, b.Name).SingleLine().Grow(1).Shrink(1)
		if b.Unread > 0 {
			kit.Text(c, strconv.Itoa(b.Unread), t.Text.Sm).TextColor(t.Muted)
		}
	})
	item.ContextMenu(func(m *ui.Menu) { p.savedViewMenu(m, b) })
}

// savedViewMenu is a view's own menu, from the sidebar or the title bar.
func (p *mailPage) savedViewMenu(m *ui.Menu, b core.SavedView) {
	if m.Item(i18n.T("view.menu.edit")).Icon(menuIcon("pen-line")).Chosen() {
		p.editView(b)
	}
	if m.Item(i18n.T("mail.mark_all_read")).Disabled(b.Unread == 0).Chosen() {
		p.markViewAsRead(savedRoute(b.ID))
	}
	m.Separator()
	if m.Item(i18n.T("view.menu.delete")).Icon(menuIcon("trash-2")).Chosen() {
		p.viewDelete = &b
	}
}

// titleMenu is the view's menu beside its name in the title bar.
func (p *mailPage) titleMenu(c *ui.Context) {
	b, ok := p.savedView(p.savedViewID())
	if !ok || p.a.route.Page != PageMail || p.viewEdit != nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	kit.IconButton(c, "ellipsis", i18n.T("view.menu"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
		Size(th.Space(5), th.Space(5)).TextColor(t.Muted).Menu(func(m *ui.Menu) { p.savedViewMenu(m, b) })
}

// viewDeleteDialog asks before deleting a view.
func (p *mailPage) viewDeleteDialog(c *ui.Context) {
	a, b := p.a, core.SavedView{}
	if p.viewDelete != nil {
		b = *p.viewDelete
	}
	p.viewDeleteOpen = p.viewDelete != nil
	kit.Dialog(c, &p.viewDeleteOpen, i18n.Tf("view.delete.title", b.Name), kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("view.delete.desc"), Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { p.viewDelete = nil })
		kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
			p.viewDelete = nil
			if p.viewEdit != nil && p.viewEdit.box.ID == b.ID {
				p.closeViewEditor()
			}
			a.writes.Chain("saved", func(ctx context.Context) error { return a.svc.DeleteView(ctx, b.ID) }, a.toastErr)
		})
	})
	if !p.viewDeleteOpen {
		p.viewDelete = nil
	}
}

// threadOf is the conversation a row belongs to: exceptions and archiving go by it.
func threadOf(it mailItem) int64 {
	if it.Thread {
		return it.ID
	}
	return it.Msg.ThreadID
}

// viewMenu is a row's view entries: 移出, 添加到视图 and 存为视图.
func (p *mailPage) viewMenu(m *ui.Menu, on []mailItem) {
	id := p.savedViewID()
	if b, ok := p.savedView(id); ok && !p.searched {
		if m.Item(i18n.T("view.remove")).Icon(menuIcon("x")).Chosen() {
			p.removeFromView(b, on)
		}
	}
	into := slices.DeleteFunc(slices.Clone(p.views), func(b core.SavedView) bool { return b.ID == id })
	if len(into) > 0 {
		m.Submenu(i18n.T("view.add_to"), func(sm *ui.Menu) {
			for _, b := range into {
				if sm.Item(b.Name).Icon(menuIcon(labelIcon(b.Icon))).Chosen() {
					p.addToView(b, on)
				}
			}
		}).Icon(menuIcon("plus"))
	}
	if len(on) == 1 && m.Item(i18n.T("view.save_as")).Icon(menuIcon("square-pen")).Chosen() {
		p.viewFromMail(on[0])
	}
}

// setViewExceptions sets kind on conversations of view id, then done.
func (p *mailPage) setViewExceptions(id int64, threads []int64, kind string, done func()) {
	a := p.a
	a.writes.Chain("saved", func(ctx context.Context) error {
		for _, t := range threads {
			if err := a.svc.SetViewException(ctx, id, t, kind); err != nil {
				return err
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else if done != nil {
			done()
		}
	})
}

func threadsOf(rows []mailItem) []int64 {
	out := make([]int64, len(rows))
	for i, it := range rows {
		out[i] = threadOf(it)
	}
	return out
}

func (p *mailPage) addToView(b core.SavedView, rows []mailItem) {
	p.setViewExceptions(b.ID, threadsOf(rows), core.ViewExceptionAdded, func() { p.a.toast(i18n.Tf("view.added", b.Name)) })
}

// removeFromView takes conversations out of b, with a toast to take that back.
func (p *mailPage) removeFromView(b core.SavedView, rows []mailItem) {
	a, threads := p.a, threadsOf(rows)
	p.setViewExceptions(b.ID, threads, core.ViewExceptionRemoved, func() {
		a.toasts = append(a.toasts, ui.Toast{ID: fmt.Sprintf("view-out-%d-%d", b.ID, threads[0]), Title: i18n.Tf("view.removed", b.Name),
			Action: i18n.T("compose.undo"), Timeout: viewUndoWindow, OnAction: func() { p.setViewExceptions(b.ID, threads, "", nil) }})
	})
}

// archiveThreads archives conversations whole, as a view does, with a
// toast to undo it.
func (p *mailPage) archiveThreads(rows []mailItem) {
	a, l := p.a, p.ls()
	threads := threadsOf(rows)
	unchoose := p.choiceGoes(l, rows)
	start := a.clock.Now()
	var undo core.ArchiveUndo
	a.writes.Chain("mail:expand", func(ctx context.Context) (err error) {
		undo, err = a.svc.ArchiveThreads(ctx, threads)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		l.sel.Clear()
		if r := a.route; r.Page == PageMail && slices.Contains(threads, r.ThreadID) { // whole conversations go
			a.setRoute(Route{Page: PageMail, View: r.View})
		}
		unchoose()
		p.offerUndoArchive([]core.ArchiveUndo{undo}, start)
	})
}

// offerUndoArchive is the toast after an archive: it says so, and takes it back.
func (p *mailPage) offerUndoArchive(undos []core.ArchiveUndo, start time.Time) {
	a := p.a
	var all core.ArchiveUndo
	for _, u := range undos {
		all.Moved, all.Marks = append(all.Moved, u.Moved...), append(all.Marks, u.Marks...)
	}
	if len(all.Moved) == 0 && len(all.Marks) == 0 {
		return
	}
	// Start before the Service call so the toast cannot outlive its durable deadline.
	left := start.Add(core.UndoArchiveWindow).Sub(a.clock.Now())
	if left <= 0 {
		a.toast(i18n.T("view.archived"))
		return
	}
	a.toasts = append(a.toasts, ui.Toast{ID: fmt.Sprintf("archive-%d", a.clock.Now().UnixNano()), Title: i18n.T("view.archived"),
		Action: i18n.T("compose.undo"), Timeout: left, OnAction: func() {
			a.writes.Chain("mail:expand", func(ctx context.Context) error { return a.svc.UndoArchiveThreads(ctx, all) }, a.toastErr)
		}})
}

// viewFromMail opens the editor on a new view for mail like it: from the
// newest message's sender that isn't the user, else to the first's recipient.
func (p *mailPage) viewFromMail(it mailItem) {
	a := p.a
	own := map[string]bool{}
	for _, acc := range a.accounts {
		own[gomail.AddrKey(acc.Email)] = true
	}
	var addr string
	a.do(func(ctx context.Context) error {
		th, err := a.svc.GetThread(ctx, threadOf(it))
		if err != nil {
			return err
		}
		ms := th.Messages
		for i := len(ms) - 1; i >= 0 && addr == ""; i-- {
			if k := gomail.AddrKey(ms[i].From); !own[k] {
				addr = ms[i].From
			}
		}
		if addr == "" && len(ms) > 0 && len(ms[0].To) > 0 {
			addr = ms[0].To[0]
		}
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		e := newViewEditor(p)
		if k := gomail.AddrKey(addr); k != "" {
			e.box.Name = displayName(addr)
			e.box.Doc.Conditions = []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: k}}
			e.note = i18n.T("view.editor.from_mail")
		}
		p.openViewEditor(e)
	})
}

// viewlessSpan is the search span with no date condition: this year.
const viewlessSpan = 4

// savableSearch is whether a search can be a view with the same results: 未读
// is the conversation's, where a search's is the message's.
func (p *mailPage) savableSearch() bool {
	return p.searched && p.sf.span != viewlessSpan && !p.sf.unread
}

// viewFromSearch opens the editor on a new view for the search shown.
func (p *mailPage) viewFromSearch() {
	sq := p.searchQuery()
	e := newViewEditor(p)
	e.box.Doc.Account = sq.AccountID
	var conds []core.RuleCondition
	if sq.Text != "" {
		conds = append(conds, core.RuleCondition{Field: core.FieldAny, Op: core.OpContains, Value: sq.Text})
	}
	if sq.From != "" {
		conds = append(conds, core.RuleCondition{Field: core.FieldFrom, Op: core.OpContains, Value: sq.From})
	}
	if sq.FromAddr != "" {
		conds = append(conds, core.RuleCondition{Field: core.FieldFrom, Op: core.OpIs, Value: sq.FromAddr})
	}
	if l, ok := p.a.label(sq.LabelID); ok && sq.LabelID != 0 {
		conds = append(conds, core.RuleCondition{Field: core.FieldLabel, Op: core.OpIs, Value: l.Slug})
	}
	if sq.HasAttachments {
		conds = append(conds, core.RuleCondition{Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes})
	}
	if d := map[int]string{1: core.ViewDateToday, 2: "7", 3: "30"}[p.sf.span]; d != "" {
		conds = append(conds, core.RuleCondition{Field: core.FieldDate, Op: core.OpIs, Value: d})
	}
	if p.sf.starred {
		conds = append(conds, core.RuleCondition{Field: core.FieldStarred, Op: core.OpIs, Value: core.RuleYes})
	}
	e.box.Doc.Conditions = conds
	e.box.Name = cmp.Or(sq.Text, sq.From, sq.FromAddr)
	e.note = i18n.T("view.editor.from_search")
	p.openViewEditor(e)
}

// viewCondText says a condition of a view or group.
func (a *App) viewCondText(cd core.RuleCondition) string {
	switch cd.Field {
	case core.FieldReply:
		if cd.Op == core.OpOlderThan {
			n, _ := strconv.Atoi(cd.Value)
			return i18n.Tf("view.cond.waiting", n)
		}
		return i18n.T("view.cond.unreplied")
	case core.FieldRead:
		return i18n.T("view.cond." + cd.Value)
	case core.FieldStarred:
		return i18n.T("view.starred." + cd.Value)
	case core.FieldWrote:
		return i18n.T("view.wrote." + cd.Value)
	case core.FieldDate:
		op, val := i18n.T("settings.rules.op.is"), ""
		if cd.Op == core.OpNotContains {
			op = i18n.T("view.op.not")
		}
		if n, err := strconv.Atoi(cd.Value); err == nil {
			val = i18n.Tf("view.date.last", n)
		} else {
			val = i18n.T("view.date." + cd.Value)
		}
		return i18n.Tf("settings.rules.cond", i18n.T("settings.rules.field.date"), op, val)
	}
	return a.pages[PageSettings].(*settingsPage).rules.condText(cd)
}

// loadWhy reads why the open conversation is in the view shown.
func (r *reader) loadWhy() {
	a, k := r.p.a, r.key
	id, ok := savedViewOf(a.route.View)
	if !ok || k[0] == 0 {
		r.why = core.ViewReason{}
		return
	}
	var why core.ViewReason
	a.query(func(ctx context.Context) (err error) {
		why, err = a.svc.ViewWhy(ctx, id, k[0])
		return err
	}, func(err error) {
		if err == nil && r.key == k {
			r.why = why
		}
	})
}

// viewBand is the status under the subject, in a view, with 写催促信
// while it waits on someone.
func (r *reader) viewBand(c *ui.Context) {
	w := r.why
	if !w.Member {
		return
	}
	var icon, text string
	switch {
	case w.Status == core.ReplyStatusUnreplied && w.NewReply:
		icon, text = "reply", i18n.T("view.band.new_reply")
	case w.Status == core.ReplyStatusUnreplied:
		icon, text = "reply", i18n.T("view.band.unreplied")
		if m, ok := r.last(); ok {
			if n := viewDaysAgo(r.p.a.clock.Now(), m.Date); n > 1 {
				text = i18n.Tf("view.band.unreplied.days", n)
			}
		}
	case w.Status == core.ReplyStatusWaiting:
		icon, text = "time", i18n.Tf("view.band.waiting", w.Days)
	default:
		return
	}
	t, th := theme.Of(c), c.Theme()
	r.column(c).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(2), th.Space(3)).MinHeight(th.Space(10)).
			Radius(t.Radius.Lg).Background(t.Hover).Border(theme.Hairline, t.Border).Children(func() {
			ui.Icon(c, icons.Get(icon)).Size(theme.IconSm, theme.IconSm).Shrink(0).TextColor(t.Muted)
			kit.Text(c, text, t.Text.Base).Grow(1).Shrink(1)
			if w.Status == core.ReplyStatusWaiting {
				kit.Button(c, i18n.T("view.follow_up"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Shrink(0).OnClick(func() { r.draft("reply") })
			}
		})
	})
}

// viewWhyLine says, under the messages, which view and group the
// conversation is in and which condition put it there.
func (r *reader) viewWhyLine(c *ui.Context) {
	p, w := r.p, r.why
	b, ok := p.savedView(p.savedViewID())
	if !w.Member || !ok {
		return
	}
	t, th := theme.Of(c), c.Theme()
	place := b.Name
	if w.GroupName != "" {
		place = i18n.Tf("view.why.group", b.Name, w.GroupName)
	}
	text := place // no condition to name: it is there because all mail is
	switch {
	case w.Condition.Field != "":
		text = i18n.Tf("view.why", place, p.a.viewCondText(w.Condition))
	case w.Manual:
		text = i18n.Tf("view.why", place, i18n.T("view.status.manual"))
	}
	thread := []mailItem{{ID: r.key[0], Thread: true}}
	r.column(c).Children(func() {
		kit.Separator(c, false)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(2), 0, 0, 0).Children(func() {
			kit.Text(c, text, t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
			if w.Manual {
				kit.TextLink(c, i18n.T("compose.undo"), t.Foreground, t.Text.Sm).Shrink(0).OnClick(func() { p.setViewExceptions(b.ID, []int64{r.key[0]}, "", nil) })
			} else {
				kit.TextLink(c, i18n.T("view.remove"), t.Foreground, t.Text.Sm).Shrink(0).OnClick(func() { p.removeFromView(b, thread) })
			}
		})
	})
}

// notMail are the events after which nothing a view shows has changed.
var notMail = []string{events.SyncStarted, events.SyncFailed, events.SettingsChanged, events.ApprovalsChanged, events.RunsChanged,
	events.PluginsChanged, events.MCPChanged, events.SignaturesChanged, events.ModelsChanged, events.MCPSendsChanged, events.SkillsChanged}

// viewUnchanged reports whether dirty leaves what the views show as
// it was: their counts stay cached, as reading them walks every view.
func viewUnchanged(dirty []events.Event) bool {
	return len(dirty) > 0 && !slices.ContainsFunc(dirty, func(ev events.Event) bool { return !slices.Contains(notMail, ev.Type) })
}

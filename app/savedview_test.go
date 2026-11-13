package app

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/theme"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

func (h *harness) savedViewNamed(name string) core.SavedView {
	h.t.Helper()
	var out core.SavedView
	h.waitMail("view "+name, func() bool {
		i := slices.IndexFunc(h.app.mail.views, func(b core.SavedView) bool { return b.Name == name })
		if i >= 0 {
			out = h.app.mail.views[i]
		}
		return i >= 0
	})
	return out
}

// groupOf is the group a row with subject is in, "" if it isn't listed.
func (h *harness) groupOf(subject string) string {
	for _, it := range h.items() {
		if it.Subject == subject {
			return it.Group
		}
	}
	return ""
}

// TestViewCreateFromSidebar: the views header's 新建视图 opens the editor, the preview
// follows the conditions, and 创建 shows the view with its mail grouped.
func TestViewCreateFromSidebar(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.put(accs[0], "INBOX", coretest.Msg("news@acme.example", "alice@example.com", "newsletter", "y", coretest.Header("List-Id: <l.example>")))
	h.put(accs[0], "INBOX", coretest.Msg("ben@other.example", "alice@example.com", "unrelated", "z"))
	h.syncNow(accs[0])

	h.headerMenu(i18n.T("view.title"), i18n.T("view.new"))
	h.see(i18n.T("view.problem.name"))
	if !h.tt.HasText(i18n.T("view.editor.create")) {
		t.Fatal("no create button")
	}
	h.click(i18n.T("settings.rules.add"))
	h.click(i18n.T("settings.rules.condition.add"))
	h.app.mail.viewEdit.box.Doc.Conditions[0].Field = core.FieldFrom
	h.fill(i18n.T("settings.rules.value"), "acme.example")
	h.until("the preview", func() bool {
		h.advance(debounce)
		return h.tt.HasText("newsletter") && !h.tt.HasText("unrelated")
	})

	h.click(i18n.T("view.editor.group.add"))
	h.fill(i18n.T("view.editor.group.name"), "To reply")
	h.app.mail.viewEdit.box.Name = "Acme"
	h.click(i18n.T("view.editor.create"))

	b := h.savedViewNamed("Acme")
	h.waitMail("the view's list", func() bool {
		return h.app.mail.viewEdit == nil && h.app.route.View == savedRoute(b.ID) && len(h.items()) == 2
	})
	if got := h.groupOf("needs a reply"); got == "" || got == core.ViewGroupOther {
		t.Fatalf("needs a reply is in %q", got)
	}
	if got := h.groupOf("newsletter"); got != core.ViewGroupOther {
		t.Fatalf("newsletter is in %q", got)
	}
	h.see("To reply")
	h.see(i18n.T("view.other"))
	h.see(i18n.T("view.status.unreplied.today"))
}

// TestViewArchiveAndUndo: archiving takes a conversation out of a group by
// reply status, into 其他, and the toast's undo brings it back.
func TestViewArchiveAndUndo(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.syncNow(accs[0])
	id, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme"}},
		Groups: []core.ViewGroup{{ID: "r", Name: "To reply", Match: core.MatchAll,
			Conditions: []core.RuleCondition{{Field: core.FieldReply, Op: core.OpIs, Value: core.ViewReplyUnreplied}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h.savedViewNamed("Acme")
	h.click("Acme")
	h.waitMail("the group", func() bool { return h.groupOf("needs a reply") == "r" })
	if h.app.route.View != savedRoute(id) {
		t.Fatalf("route %+v", h.app.route)
	}

	h.app.mail.batch("archive", h.items())
	h.waitMail("it in 其他", func() bool { return h.groupOf("needs a reply") == core.ViewGroupOther })
	f := h.folderOf(accs[0], "archive")
	if n := h.folderOf(accs[0], "archive").Total; n != 1 {
		t.Fatalf("archive %s holds %d", f.Name, n)
	}

	h.click(i18n.T("compose.undo"))
	h.waitMail("it back", func() bool { return h.groupOf("needs a reply") == "r" })
	if n := h.folderOf(accs[0], "inbox").Total; n != 1 {
		t.Fatalf("inbox holds %d", n)
	}
}

// TestViewToHandleFirstRun: 待处理 is there from the first run, and a reply takes
// a mail out of it as soon as it is queued.
func TestViewToHandleFirstRun(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.syncNow(accs[0])
	h.waitMail("待处理", func() bool { return len(h.app.mail.views) == 1 })
	toHandle := h.app.mail.views[0]
	h.click(toHandle.Name)
	h.waitMail("the mail in it", func() bool { return h.groupOf("needs a reply") == "reply" })
	h.waitMail("its count", func() bool { b, _ := h.app.mail.savedView(toHandle.ID); return b.Unread == 1 })
	h.see(i18n.T("view.status.unreplied.today"))

	it := h.items()[0]
	th, err := h.env.Svc.GetThread(context.Background(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := h.env.Svc.NewDraft(context.Background(), accs[0].ID, "reply", th.Messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.Send(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	h.waitMail("it gone once queued", func() bool { return len(h.items()) == 0 })
	h.see(i18n.T("view.empty"))
}

// TestViewRefreshNotLostToCached: a refresh that reuses the views must not
// replace one still reading them: its result is dropped, so nothing would.
func TestViewRefreshNotLostToCached(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.syncNow(accs[0])
	p := h.app.mail
	h.waitMail("待处理", func() bool { return len(p.views) == 1 })
	h.click(p.views[0].Name)
	h.waitMail("the mail in it", func() bool { return len(h.items()) == 1 })

	p.items, p.viewGroups = nil, nil // what the list looks like just before a change shows
	p.refresh([]events.Event{{Type: events.OutboxChanged}})
	p.refresh([]events.Event{{Type: events.SyncStarted}})
	h.waitMail("the list read again", func() bool { return len(h.items()) == 1 })
}

// TestViewWhyAndRemove: in a view the reader says the status and why
// the conversation is there, and 移出 takes it out with a toast to undo.
func TestViewWhyAndRemove(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.syncNow(accs[0])
	if _, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme"}}}}); err != nil {
		t.Fatal(err)
	}
	h.savedViewNamed("Acme")
	h.click("Acme")
	h.waitMail("the row", func() bool { return !h.app.mail.list.loading && len(h.items()) == 1 })
	h.app.mail.open(h.items(), 0)
	h.see(i18n.T("view.band.unreplied"))
	h.see("Acme" + "：")

	h.click(i18n.T("view.remove"))
	h.waitMail("it gone", func() bool { return len(h.items()) == 0 })
	h.click(i18n.T("compose.undo"))
	h.waitMail("it back", func() bool { return len(h.items()) == 1 })
}

// TestViewSaveSearch: a search becomes a view with the same results, and a
// time filter hides the button.
func TestViewSaveSearch(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "quarterly report", "x"))
	h.put(accs[0], "INBOX", coretest.Msg("ben@other.example", "alice@example.com", "lunch", "y"))
	h.syncNow(accs[0])
	p := h.app.mail
	h.waitMail("the mail", func() bool { return len(h.items()) == 2 })
	p.searchOpen, p.query = true, "quarterly"
	p.runSearch()
	h.until("results", func() bool { h.drain(); return p.searched && len(p.results) == 1 })
	h.see(i18n.T("view.save_search"))

	p.sf.unread = true
	h.until("no button with unread", func() bool { return !p.savableSearch() })
	p.sf.unread = false
	p.sf.span = viewlessSpan
	h.until("no button with this year", func() bool { return !p.savableSearch() })
	p.sf.span = 3 // the last 30 days

	h.click(i18n.T("view.save_search"))
	h.see(i18n.T("view.editor.from_search"))
	h.until("the preview", func() bool {
		h.advance(debounce)
		return h.tt.HasText("quarterly report") && p.viewEdit.prev != nil
	})
	if got := p.viewEdit.box.Doc.Conditions; len(got) != 2 || got[0].Field != core.FieldAny || got[1].Field != core.FieldDate || got[1].Value != "30" {
		t.Fatalf("conditions %+v", got)
	}
	h.click(i18n.T("view.editor.create"))
	h.waitMail("the view", func() bool { return p.viewEdit == nil && p.savedViewID() != 0 && len(h.items()) == 1 })
}

// TestViewDeletedViewGoesToInbox: deleting the view shown leaves for the inbox.
func TestViewDeletedViewGoesToInbox(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "kept", "x"))
	h.syncNow(accs[0])
	id, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme"}}}})
	if err != nil {
		t.Fatal(err)
	}
	h.savedViewNamed("Acme")
	h.click("Acme")
	if err := h.env.Svc.DeleteView(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the inbox", func() bool { return h.app.route.View == core.View{Unified: "inbox"} })
	h.waitMail("its mail still there", func() bool { return len(h.items()) == 1 })
}

// TestViewReopenLeavesHourly: an open view rereads with no event, so
// 等待回复 shows on time.
func TestViewRefreshesWithTime(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "kept", "x"))
	h.syncNow(accs[0])
	if _, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme"}}}}); err != nil {
		t.Fatal(err)
	}
	h.savedViewNamed("Acme")
	h.click("Acme")
	h.waitMail("the row", func() bool { return !h.app.mail.list.loading && len(h.items()) == 1 })
	before := h.app.mail.list.Gen()
	h.advance(time.Minute)
	if h.app.mail.list.Gen() != before {
		t.Fatalf("reread early")
	}
	h.advance(viewRefreshEvery)
	if h.app.mail.list.Gen() == before {
		t.Fatalf("no reread after %v", viewRefreshEvery)
	}
}

// TestViewFromMailAndAdd: 存为视图 fills in the sender, 添加到视图 adds a
// conversation the conditions miss, and it is listed in the editor to take back.
func TestViewFromMailAndAdd(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("Anna Park <anna@acme.example>", "alice@example.com", "from anna", "x"))
	h.put(accs[0], "INBOX", coretest.Msg("ben@other.example", "alice@example.com", "from ben", "y"))
	h.syncNow(accs[0])
	p := h.app.mail
	h.waitMail("the mail", func() bool { return len(h.items()) == 2 })
	row := func(subject string) mailItem {
		return h.items()[slices.IndexFunc(h.items(), func(it mailItem) bool { return it.Subject == subject })]
	}

	p.viewFromMail(row("from anna"))
	h.see(i18n.T("view.editor.from_mail"))
	if got := p.viewEdit.box; got.Name != "Anna Park" || len(got.Doc.Conditions) != 1 || got.Doc.Conditions[0].Value != "anna@acme.example" {
		t.Fatalf("filled in %+v", got)
	}
	h.click(i18n.T("view.editor.create"))
	b := h.savedViewNamed("Anna Park")
	h.waitMail("its list", func() bool { return p.viewEdit == nil && len(h.items()) == 1 })

	h.click(i18n.T("mail.unified.inbox"))
	h.waitMail("the inbox", func() bool { return len(h.items()) == 2 })
	p.addToView(b, []mailItem{row("from ben")})
	h.see(i18n.Tf("view.added", b.Name))

	h.click(b.Name)
	h.waitMail("both", func() bool { return p.savedViewID() == b.ID && len(h.items()) == 2 })
	if !h.items()[slices.IndexFunc(h.items(), func(it mailItem) bool { return it.Subject == "from ben" })].Manual {
		t.Fatal("from ben is not marked as added by hand")
	}
	b = h.savedViewNamed("Anna Park")
	p.editView(b)
	h.see(i18n.T("view.editor.exceptions"))
	h.see(i18n.T("view.exception.added"))
	h.click(i18n.T("compose.undo"))
	h.waitMail("it listed no more", func() bool { return p.viewEdit != nil && len(p.viewEdit.exceptions) == 0 })
	p.closeViewEditor()
	h.waitMail("one again", func() bool { return len(h.items()) == 1 })
}

// TestViewEditorLeavesToSidebarRoute: the sidebar leaves the editor even for
// the route already showing behind it.
func TestViewEditorLeavesToSidebarRoute(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.syncNow(accs[0])
	p := h.app.mail
	h.waitMail("the default view", func() bool { return len(p.views) == 1 })
	b := p.views[0]
	p.editView(b)
	h.see(i18n.T("common.save"))
	clickRow := func(name string) { // the title bar shows the name too
		t.Helper()
		row, ok := h.tt.AccessByRole(ui.RoleButton, name)
		if !ok {
			t.Fatalf("no sidebar row %q", name)
		}
		h.tt.ClickAt(row.Bounds.X+row.Bounds.W/2, row.Bounds.Y+row.Bounds.H/2)
	}
	clickRow(b.Name)
	h.waitMail("its list", func() bool { return p.viewEdit == nil && p.savedViewID() == b.ID })

	// The same for the mailbox behind a new view's editor.
	starred, v := i18n.T("mail.unified.starred"), core.View{Unified: "starred"}
	clickRow(starred)
	h.waitMail("the mailbox", func() bool { return h.app.route.View == v })
	p.openViewEditor(newViewEditor(p))
	h.see(i18n.T("view.editor.create"))
	clickRow(starred)
	h.waitMail("the mailbox again", func() bool { return p.viewEdit == nil && h.app.route.View == v })
}

// TestViewEditorAsksBeforeLosingEdits: leaving with changes asks, whichever
// way the user leaves; leaving with none does not.
func TestViewEditorAsksBeforeLosingEdits(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "needs a reply", "x"))
	h.syncNow(accs[0])
	p := h.app.mail
	h.waitMail("the default view", func() bool { return len(p.views) == 1 })
	b := p.views[0]
	starred, there := i18n.T("mail.unified.starred"), core.View{Unified: "starred"}
	clickRow := func() {
		t.Helper()
		row, ok := h.tt.AccessByRole(ui.RoleButton, starred)
		if !ok {
			t.Fatal("no sidebar row")
		}
		h.tt.ClickAt(row.Bounds.X+row.Bounds.W/2, row.Bounds.Y+row.Bounds.H/2)
		h.drain()
		h.tt.Frame()
	}

	p.editView(b)
	h.see(i18n.T("common.save"))
	h.tt.Frame() // drawing the form must not count as a change
	clickRow()
	if p.viewEdit != nil || p.viewLeave != nil || h.app.route.View != there {
		t.Fatalf("no changes, yet edit=%v asked=%v route=%+v", p.viewEdit != nil, p.viewLeave != nil, h.app.route.View)
	}

	p.editView(b)
	h.see(i18n.T("common.save"))
	p.viewEdit.box.Name = "Renamed"
	h.tt.Frame()
	drawn := func(button string) { // a dialog is modal even while it draws nothing
		t.Helper()
		h.see(button)
		h.tt.Advance(2 * theme.MotionBase)
		h.tt.Frame()
		if px := h.tt.Image().RGBAAt(640, 800); px.R > 235 { // the scrim over the white panel
			t.Fatalf("%s is not drawn: %v", button, px)
		}
	}
	clickRow()
	drawn(i18n.T("view.leave.discard"))
	if h.app.route.View != savedRoute(b.ID) {
		t.Fatalf("left while asking: %+v", h.app.route.View)
	}
	h.click(i18n.T("view.leave.keep"))
	if p.viewEdit == nil || p.viewEdit.box.Name != "Renamed" || p.viewLeave != nil {
		t.Fatal("继续编辑 lost the form")
	}

	h.app.back() // not only the sidebar
	h.see(i18n.T("view.leave.discard"))
	h.click(i18n.T("view.leave.keep"))

	clickRow()
	h.click(i18n.T("view.leave.discard"))
	if p.viewEdit != nil || h.app.route.View != there {
		t.Fatalf("放弃修改 stayed: edit=%v route=%+v", p.viewEdit != nil, h.app.route.View)
	}

	p.viewDelete = &b
	drawn(i18n.T("common.delete"))
	h.click(i18n.T("common.cancel"))

	// Closing the window asks too, with the close's own dialog.
	p.editView(b)
	h.see(i18n.T("common.save"))
	p.viewEdit.box.Name = "Renamed"
	closed := false
	h.app.BeforeClose(func() { closed = true })
	h.until("the question", func() bool { return h.app.closeAsk })
	if closed {
		t.Fatal("closed without asking")
	}
	h.see(i18n.T("view.leave.desc"))
	h.click(i18n.T("close.unsaved.quit"))
	h.until("closed", func() bool { return closed })
	if p.viewEdit != nil {
		t.Fatal("the editor outlived the close")
	}
}

// TestViewRefreshesAtMidnight: 今天 turns over on time, not up to an hour late.
func TestViewRefreshesAtMidnight(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "kept", "x"))
	h.syncNow(accs[0])
	if _, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme"}}}}); err != nil {
		t.Fatal(err)
	}
	h.savedViewNamed("Acme")
	h.click("Acme")
	h.waitMail("the row", func() bool { return !h.app.mail.list.loading && len(h.items()) == 1 })
	now := h.clock.Now()
	y, m, d := now.Date()
	for now.Add(viewRefreshEvery).Before(time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())) {
		h.advance(viewRefreshEvery)
		now = h.clock.Now()
	}
	h.waitMail("settled", func() bool { return !h.app.mail.list.loading })
	before := h.app.mail.list.Gen()
	h.advance(time.Date(y, m, d+1, 0, 0, 2, 0, now.Location()).Sub(now))
	if h.app.mail.list.Gen() == before {
		t.Fatal("no reread just after midnight")
	}
}

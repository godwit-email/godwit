package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// titleActionLabels are the title bar's actions on the open message, as an inbox shows them.
func titleActionLabels() []string {
	return []string{i18n.T("reader.reply"), i18n.T("reader.reply_all"), i18n.T("reader.forward"),
		i18n.T("mail.action.star"), i18n.T("mail.action.archive"), i18n.T("mail.action.junk"), i18n.T("mail.action.delete"), i18n.T("reader.more")}
}

// TestTitleActionsNeedAnOpenMessage: the reply and organise groups are in the
// title bar while a message is open, and gone with none: an empty list, or
// the scheduled list, which shows no reader. The sidebar's own 归档 row is not one.
func TestTitleActionsNeedAnOpenMessage(t *testing.T) {
	shown := func(h *harness, want bool) {
		t.Helper()
		for _, label := range titleActionLabels() {
			r, ok := h.tt.Find(label)
			if ok = ok && r.Y+r.H <= theme.TopBarHeight; ok != want {
				t.Errorf("%q shown = %v, want %v", label, ok, want)
			}
		}
	}
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.settle()
	shown(h, false)
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Plan", "body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	shown(h, true)
	if r, _ := h.tt.Find(i18n.T("reader.reply")); r.Y+r.H > theme.TopBarHeight {
		t.Errorf("the reply button at %+v is not in the title bar", r)
	}
	h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{Unified: "scheduled"}}) })
	h.until("the scheduled list", func() bool { return h.app.route.View.Unified == "scheduled" })
	h.tt.Frame()
	shown(h, false)
}

// TestTitleBarAt960x600: in the least window, with the sidebar open or folded,
// the search field and the actions fit it, and the actions can be pressed.
func TestTitleBarAt960x600(t *testing.T) {
	for _, folded := range []bool{false, true} {
		t.Run(fmt.Sprintf("folded=%v", folded), func(t *testing.T) {
			h, accs := mailHarness(t, nil, "alice@example.com")
			m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Plan", "body"))
			h.resize(960, 600)
			h.post(func() { h.app.sidebarHidden = folded })
			h.openThread(m.ThreadID)
			h.seeBody(m.ID)
			h.tt.Advance(2 * theme.MotionBase)
			var prev ui.Rect
			for _, label := range titleActionLabels() {
				r, ok := h.tt.Find(label)
				if !ok {
					t.Fatalf("no %q", label)
				}
				if r.X < 0 || r.X+r.W > 960 || r.Y < 0 || r.Y+r.H > theme.TopBarHeight || r.X < prev.X+prev.W {
					t.Errorf("%q at %+v is clipped or overlaps %+v", label, r, prev)
				}
				prev = r
			}
			if r, ok := h.tt.Find(i18n.T("mail.search")); !ok || r.X+r.W > prev.X {
				t.Errorf("the search field at %+v (found %v) runs into the actions", r, ok)
			}
			h.click(i18n.T("reader.reply"))
			h.click(i18n.T("mail.action.archive"))
			h.until("archived", func() bool { return h.countIn(h.folderOf(accs[0], "archive")) == 1 })
		})
	}
}

// TestSyncStatusRowUnderSettings: the sync status is a row under Settings at
// the sidebar's foot, saying what each state says.
func TestSyncStatusRowUnderSettings(t *testing.T) {
	failed := func(kind string) func(*App, core.Account) {
		return func(a *App, acc core.Account) {
			a.onEvent(events.Event{Type: events.SyncFailed, AccountID: acc.ID, Kind: kind, Error: "x"})
		}
	}
	for _, c := range []struct {
		name string
		do   func(*App, core.Account)
		want string
	}{
		{"ok", func(a *App, acc core.Account) { a.onEvent(events.Event{Type: events.SyncFinished, AccountID: acc.ID}) }, "已同步 · 刚刚"},
		{"syncing", func(a *App, acc core.Account) { a.onEvent(events.Event{Type: events.SyncStarted, AccountID: acc.ID}) }, i18n.T("shell.sync.syncing")},
		{"retrying", failed("connect"), "alice@example.com · 重试中"},
		{"error", failed("tls"), "alice@example.com · 同步失败"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var acc core.Account
			h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
			h.until("initial load", func() bool { return h.app.loaded })
			h.post(func() { c.do(h.app, acc) })
			h.click(c.want) // pressing it opens the list, as the title bar's did
			h.see(i18n.T("shell.sync.title"))
			settings, ok := h.tt.Find(i18n.T("shell.nav.settings"))
			row, found := h.tt.Find(c.want)
			if !ok || !found {
				t.Fatalf("settings found %v, row %q found %v", ok, c.want, found)
			}
			if row.Y < settings.Y+settings.H || row.H != 28 || row.X+row.W > theme.SidebarWidth {
				t.Errorf("the row at %+v is not 28 high under Settings %+v in the sidebar", row, settings)
			}
			title, _ := h.tt.Find(i18n.T("shell.sync.title"))
			if title.Y+title.H > row.Y {
				t.Errorf("the list at %+v does not open above the row %+v", title, row)
			}
		})
	}
}

type crumbPage struct {
	sidebarMotionPage
	parent, current string
	ok              bool
	ups             *int
}

func (p crumbPage) Breadcrumb() (string, string, bool) { return p.parent, p.current, p.ok }
func (p crumbPage) BreadcrumbUp()                      { *p.ups++ }

// TestBreadcrumb: a page that has one shows it from x = 280 in place of a
// title and the search, its parent taking the page up; a page that has none
// shows nothing.
func TestBreadcrumb(t *testing.T) {
	for _, ok := range []bool{true, false} {
		t.Run(fmt.Sprintf("ok=%v", ok), func(t *testing.T) {
			ups := 0
			a := &App{route: Route{Page: PageSettings}, pages: map[Page]pageView{PageSettings: crumbPage{parent: "Parent", current: "Current", ok: ok, ups: &ups}}}
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				a.titleBar(c, theme.TopBarHeight)
			}, 960, 100)
			tt.Frame()
			parent, shown := tt.Find("Parent")
			_, current := tt.Find("Current")
			if shown != ok || current != ok {
				t.Fatalf("breadcrumb shown = %v, %v; want %v", shown, current, ok)
			}
			if ok && !near(parent.X, 280) {
				t.Errorf("the breadcrumb starts at x = %v, want 280", parent.X)
			}
			if _, found := tt.Find(i18n.T("mail.search")); found {
				t.Error("settings shows the search field")
			}
			if ok {
				if err := tt.Click("Parent"); err != nil || ups != 1 {
					t.Errorf("clicking the parent: err %v, %d ups, want 1", err, ups)
				}
			}
		})
	}
}

// TestBackLeavesSettingsWithoutHistory: settings has no row back to mail, so
// with no history (settings restored at launch) the title bar's back still
// leaves it, for the page before settings or else the inbox.
func TestBackLeavesSettingsWithoutHistory(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.post(func() { h.app.history, h.app.ahead = nil, nil })
	h.click(i18n.T("shell.back"))
	h.until("back in mail", func() bool { return h.app.route.Page == PageMail })
}

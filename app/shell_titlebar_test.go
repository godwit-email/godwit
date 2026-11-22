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

// asMac poses as macOS for the test: its single-row title bar holds the page's
// actions, where Windows and Linux move them to the context bar below (splitChrome).
func asMac(t *testing.T) {
	old := goos
	goos = "darwin"
	t.Cleanup(func() { goos = old })
}

// titleActionLabels are the title bar's actions on the open message, as an inbox shows them.
func titleActionLabels() []string {
	return []string{i18n.T("reader.reply"), i18n.T("reader.reply_all"), i18n.T("reader.forward"),
		i18n.T("mail.action.star"), i18n.T("mail.action.archive"), i18n.T("mail.action.junk"), i18n.T("mail.action.delete"), i18n.T("reader.more")}
}

// TestTitleActionsNeedAnOpenMessage: the reply and organise groups are in the
// title bar while a message is open, and gone with none: an empty list, or
// the scheduled list, which shows no reader. The sidebar's own 归档 row is not one.
func TestTitleActionsNeedAnOpenMessage(t *testing.T) {
	asMac(t)
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
	asMac(t)
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

// TestSyncStatusRowUnderSettings: healthy/syncing states stay compact beside
// Settings, while retrying and error states elevate to a full-width alert card
// directly above Settings so the full reason and account name fit cleanly.
func TestSyncStatusRowUnderSettings(t *testing.T) {
	failed := func(kind string) func(*App, core.Account) {
		return func(a *App, acc core.Account) {
			a.onEvent(events.Event{Type: events.SyncFailed, AccountID: acc.ID, Kind: kind, Error: "x"})
		}
	}
	for _, c := range []struct {
		name  string
		do    func(*App, core.Account)
		want  string
		alert bool
	}{
		{"ok", func(a *App, acc core.Account) { a.onEvent(events.Event{Type: events.SyncFinished, AccountID: acc.ID}) }, "已同步 · 刚刚", false},
		{"syncing", func(a *App, acc core.Account) { a.onEvent(events.Event{Type: events.SyncStarted, AccountID: acc.ID}) }, i18n.T("shell.sync.syncing"), false},
		{"retrying", failed("connect"), "alice@example.com · 自动重试中", true},
		{"error", failed("tls"), "alice@example.com · 证书有问题", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var acc core.Account
			h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
			h.until("initial load", func() bool { return h.app.loaded })
			h.post(func() { c.do(h.app, acc) })
			h.click(c.want) // pressing it opens the sync status popover
			h.see(i18n.T("shell.sync.all"))
			settings, ok := h.tt.Find(i18n.T("shell.nav.settings"))
			row, found := h.tt.Find(c.want)
			if !ok || !found {
				t.Fatalf("settings found %v, row %q found %v", ok, c.want, found)
			}
			if c.alert {
				if row.Y+row.H > settings.Y || row.X+row.W > theme.SidebarWidth {
					t.Errorf("alert card at %+v should sit above Settings %+v within sidebar width %v", row, settings, theme.SidebarWidth)
				}
			} else {
				if midDiff := (row.Y + row.H/2) - (settings.Y + settings.H/2); midDiff < -1 || midDiff > 1 || row.X < settings.X+settings.W || row.H != 28 || row.X+row.W > theme.SidebarWidth {
					t.Errorf("the sync chip at %+v is not 28 high beside Settings %+v in the sidebar", row, settings)
				}
			}
			allBtn, _ := h.tt.Find(i18n.T("shell.sync.all"))
			if allBtn.Y+allBtn.H > row.Y {
				t.Errorf("the popover at %+v does not open above %+v", allBtn, row)
			}
		})
	}
}

// TestSyncAlertMultiAccountSummary: when multiple accounts need attention, the
// elevated card summarizes the count rather than hiding the others.
func TestSyncAlertMultiAccountSummary(t *testing.T) {
	var a, b core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a, b = e.AddAccount(t, "alice@example.com"), e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.post(func() {
		h.app.onEvent(events.Event{Type: events.SyncFailed, AccountID: a.ID, Kind: "tls", Error: "bad cert"})
		h.app.onEvent(events.Event{Type: events.SyncFailed, AccountID: b.ID, Kind: "auth", Error: "bad pass"})
	})
	want := i18n.Tf("shell.sync.account", i18n.Tf("shell.sync.multi_accounts", "alice@example.com", 2), i18n.Tf("shell.sync.multi_error", 2))
	h.see(want)
	h.click(want)
	h.see(i18n.T("shell.sync.all"))
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
	asMac(t)
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

// TestBackLeavesSettingsWithoutHistory: with no history (settings restored at
// launch) the title bar's back still leaves settings, for the inbox.
func TestBackLeavesSettingsWithoutHistory(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.post(func() { h.app.history, h.app.ahead = nil, nil })
	h.click(i18n.T("shell.back"))
	h.until("back in mail", func() bool { return h.app.route.Page == PageMail })
}

// TestSettingsSidebarFooterLeavesInOneClick: after browsing multiple settings
// pages, the pinned footer row returns straight to the page settings was opened
// from in a single click, without stepping back through settings history.
func TestSettingsSidebarFooterLeavesInOneClick(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.post(func() {
		h.app.navigate(Route{Page: PageSettings, Settings: "models"})
		h.app.navigate(Route{Page: PageSettings, Settings: "about"})
	})
	h.click(i18n.Tf("shell.nav.back_to", i18n.T("shell.nav.mail")))
	h.until("back in mail in one click", func() bool { return h.app.route.Page == PageMail })

	h.post(func() {
		h.app.navigate(Route{Page: PageContacts})
		h.app.navigate(Route{Page: PageSettings, Settings: "general"})
		h.app.navigate(Route{Page: PageSettings, Settings: "labels"})
	})
	h.click(i18n.Tf("shell.nav.back_to", i18n.T("shell.nav.contacts")))
	h.until("back in contacts in one click", func() bool { return h.app.route.Page == PageContacts })
}

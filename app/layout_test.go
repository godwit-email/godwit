package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// setLayout changes the settings the way the settings page does.
func (h *harness) setLayout(f func(*core.Settings)) {
	h.t.Helper()
	st := h.app.settings
	f(&st)
	h.post(func() { h.app.changeSettings(st) })
	h.drain()
	h.tt.Frame()
}

// headerMenu chooses item in the menu of the sidebar section headed header.
func (h *harness) headerMenu(header, item string) {
	h.t.Helper()
	h.until("the header "+header, func() bool { _, ok := h.tt.Find(header); return ok })
	if err := h.tt.RightClick(header); err != nil {
		h.t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatal(err)
	}
	h.drain()
	h.tt.Frame()
}

func (h *harness) top(label string) float32 {
	h.t.Helper()
	h.until("an element "+label, func() bool { _, ok := h.tt.Find(label); return ok })
	r, _ := h.tt.Find(label)
	return r.Y
}

// The sidebar's sections follow Settings.SidebarOrder.
func TestSidebarSectionOrder(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	views, accounts := i18n.T("view.title"), i18n.T("mail.accounts")
	if h.top(views) > h.top(accounts) {
		t.Fatalf("by default the views (%v) come before the accounts (%v)", h.top(views), h.top(accounts))
	}
	h.setLayout(func(st *core.Settings) { st.SidebarOrder = "accounts,labels,views" })
	h.until("the accounts first", func() bool { return h.top(accounts) < h.top(views) })
}

// A list row leads with who it is with, or with the subject, as Settings.ListRowOrder says.
func TestListRowOrder(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Quarterly numbers", "text"))
	h.waitMail("one thread", func() bool { return len(h.items()) == 1 })
	if h.top("bob@example.com") > h.top("Quarterly numbers") {
		t.Fatal("by default the sender leads the row")
	}
	h.setLayout(func(st *core.Settings) { st.ListRowOrder = core.ListRowSubject })
	h.until("the subject first", func() bool { return h.top("Quarterly numbers") < h.top("bob@example.com") })
}

// The reading column's widest size follows Settings.ReaderWidth.
func TestReaderMaxWidth(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	normal := h.app.readerMaxWidth()
	if normal != theme.ReaderMaxWidth {
		t.Fatalf("default width %v", normal)
	}
	h.setLayout(func(st *core.Settings) { st.ReaderWidth = core.ReaderWidthWide })
	h.until("a wider column", func() bool { return h.app.readerMaxWidth() > normal })
	wide := h.app.readerMaxWidth()
	h.setLayout(func(st *core.Settings) { st.ReaderWidth = core.ReaderWidthFull })
	h.until("the widest column", func() bool { return h.app.readerMaxWidth() > wide })
}

// The list row and reading width choices are in Settings › General, and choosing one changes the settings.
func TestLayoutSettingsPage(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("general")
	for _, key := range []string{"settings.list_order", "settings.reader_width"} {
		h.see(i18n.T(key))
	}
	p.st.ReaderWidth = core.ReaderWidthWide
	h.drain()
	h.tt.Frame()
	if got := h.app.settings.ReaderWidth; got != core.ReaderWidthWide {
		t.Fatalf("reader width %q after choosing it", got)
	}
}

// A sidebar section's header dragged onto another's takes its place; the
// fixed rows above stay.
func TestSidebarSectionDrag(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	views, accounts := i18n.T("view.title"), i18n.T("mail.accounts")
	h.top(views)
	from, _ := h.tt.Find(accounts)
	to, _ := h.tt.Find(views)
	x, y := from.X+from.W/2, from.Y+from.H/2
	h.tt.Move(x, y)
	h.tt.Press(x, y)
	h.tt.Move(x, y-10)
	h.tt.Move(x, to.Y+to.H/2)
	h.tt.Frame()
	h.tt.Release(x, to.Y+to.H/2)
	h.until("the accounts first", func() bool { return h.app.settings.SidebarOrder == "accounts,views,labels" })
	h.until("the accounts above the views", func() bool { return h.top(accounts) < h.top(views) })
	if h.top(i18n.T("mail.compose")) > h.top(accounts) || h.top(i18n.T("mail.unified.starred")) > h.top(accounts) {
		t.Fatal("a fixed row moved below the sections")
	}
}

// A section's header menu moves it past the shown section below, skipping
// labels while none is on mail, and back up.
func TestSidebarSectionMenu(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	views, accounts := i18n.T("view.title"), i18n.T("mail.accounts")
	h.top(views)
	h.headerMenu(views, i18n.T("mail.section.down"))
	h.until("the views under the accounts", func() bool { return h.app.settings.SidebarOrder == "labels,accounts,views" })
	h.until("the views drawn under", func() bool { return h.top(accounts) < h.top(views) })
	h.headerMenu(views, i18n.T("mail.section.up"))
	h.until("the views back on top", func() bool { return h.app.settings.SidebarOrder == "labels,views,accounts" })
}

// The labels header's 新建标签 opens Settings › Labels with the new label's dialog.
func TestSidebarLabelsMenuNew(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Invoice", "text"))
	if err := h.env.Svc.SetLabel(context.Background(), []int64{m.ID}, h.labels()[1].ID, true); err != nil {
		t.Fatal(err)
	}
	h.post(h.app.load)
	h.headerMenu(i18n.T("mail.labels"), i18n.T("settings.labels.new"))
	if h.app.route.Settings != "labels" {
		t.Fatalf("route %+v", h.app.route)
	}
	h.see(i18n.T("settings.labels.dialog.new"))
}

// An account's fold in the sidebar is saved in Settings.SidebarAccountsOpen and
// comes back with a fresh page; an account with no saved fold takes the default.
func TestSidebarAccountFoldSaved(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com", "bob@example.com")
	h.until("the first account open", func() bool { return *h.app.mail.accountOpen(accs[0].ID) })
	if *h.app.mail.accountOpen(accs[1].ID) {
		t.Fatal("the second account starts folded")
	}
	h.until("the second account's row", func() bool { _, ok := h.tt.Find("bob@example.com"); return ok })
	if err := h.tt.Click("bob@example.com"); err != nil {
		t.Fatal(err)
	}
	h.until("the unfold saved", func() bool { return h.app.settings.SidebarAccountsOpen[accs[1].ID] })
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil || !st.SidebarAccountsOpen[accs[1].ID] {
		t.Fatalf("stored %v, %v: want the second account unfolded", st.SidebarAccountsOpen, err)
	}
	restarted := newMailPage(h.app)
	if !*restarted.accountOpen(accs[1].ID) || !*restarted.accountOpen(accs[0].ID) {
		t.Fatal("after a restart the accounts the user unfolded and the first are open")
	}
	h.app.accounts = append(h.app.accounts, core.Account{ID: accs[1].ID + 100})
	if *newMailPage(h.app).accountOpen(accs[1].ID + 100) {
		t.Fatal("a new account takes the default: folded")
	}
}

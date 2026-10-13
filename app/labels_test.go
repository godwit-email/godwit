package app

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// labelID is the id of a label by slug.
func (h *harness) labelID(slug string) int64 {
	h.t.Helper()
	ls, err := h.env.Svc.ListLabels(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, l := range ls {
		if l.Slug == slug {
			return l.ID
		}
	}
	h.t.Fatalf("no label %s", slug)
	return 0
}

// autoLabel puts a label on a message as the classifier would, through a
// second connection (core has no API for it), and lets the App refresh.
func (h *harness) autoLabel(msg int64, slug string) {
	h.t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(h.env.DataDir, "godwit.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Execute(context.Background(), `INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, 'auto', 1)
		ON CONFLICT(message_id, label_id) DO UPDATE SET source = 'auto', active = 1`, msg, h.labelID(slug)); err != nil {
		h.t.Fatal(err)
	}
	h.settle()
}

// labeledMail delivers one message to alice's inbox and returns it.
func labeledMail(t *testing.T) (*harness, core.Account, core.MessageSummary) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Check-in open", "your flight"))
	h.syncNow(acc)
	h.waitMail("the mail", func() bool { return h.tt.HasText("Check-in open") })
	page, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 10)
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("no message: %v", err)
	}
	return h, acc, page.Items[0]
}

func TestCoreLabelsHaveTokens(t *testing.T) {
	for _, n := range core.LabelColors {
		if !slices.Contains(theme.LabelNames[:], n) {
			t.Errorf("label color %s has no tokens", n)
		}
	}
	for _, n := range core.LabelIcons {
		icons.Get("label-" + n) // panics when missing
	}
}

func TestListRowChips(t *testing.T) {
	h, _, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	h.autoLabel(m.ID, "travel")
	if err := h.env.Svc.SetLabel(context.Background(), []int64{m.ID}, h.labelID("notice"), true); err != nil {
		t.Fatal(err)
	}
	h.waitMail("two chips and +1", func() bool { return h.tt.HasText("航班与出行") && h.tt.HasText("+1") })
	h.see("验证码")
	if h.tt.HasText("通知") && h.countTexts("通知") > 1 {
		t.Errorf("the third label shows in the row: %q", h.tt.Texts())
	}
}

func TestSearchRowChips(t *testing.T) {
	h, _, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	h.autoLabel(m.ID, "travel")
	h.autoLabel(m.ID, "notice")
	p := h.app.mail
	p.searchOpen = true
	p.query = "Check-in"
	p.runSearch()
	h.until("results with chips", func() bool { h.tt.Frame(); return p.searched && h.tt.HasText("+1") })
}

func TestSidebarLabels(t *testing.T) {
	h, _, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	h.waitMail("the label group", func() bool { return h.tt.HasText("标签") && h.tt.HasText("验证码") })
	if h.tt.HasText("订单与物流") {
		t.Error("a label with no mail shows in the sidebar")
	}
	h.click("验证码")
	h.until("the label view", func() bool { return h.app.route.View == core.View{LabelID: h.labelID("otp")} })
	if title, _ := h.app.mail.Title(); title != "验证码" {
		t.Errorf("title %q, want 验证码", title)
	}
}

func TestLabelsRefreshOnEvent(t *testing.T) {
	h, _, m := labeledMail(t)
	if h.tt.HasText("账单") {
		t.Fatal("a chip before any label")
	}
	if err := h.env.Svc.SetLabel(context.Background(), []int64{m.ID}, h.labelID("bill"), true); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the chip", func() bool { return h.tt.HasText("账单") })
}

func TestSearchLabelFilter(t *testing.T) {
	h, acc, m := labeledMail(t)
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Check-in closed", "too late"))
	h.syncNow(acc)
	h.autoLabel(m.ID, "otp")
	p := h.app.mail
	p.searchOpen = true
	p.query = "Check-in"
	p.runSearch()
	h.until("both results", func() bool { h.tt.Frame(); return p.searched && len(p.results) == 2 })
	h.click("按标签筛选")
	h.click("只看「验证码」")
	h.until("only the labeled mail", func() bool { h.tt.Frame(); return len(p.results) == 1 && p.results[0].ID == m.ID })
	if p.sf.labelID != h.labelID("otp") {
		t.Errorf("filter label %d", p.sf.labelID)
	}
	h.click("按标签筛选")
	h.click("全部标签")
	h.until("both again", func() bool { h.tt.Frame(); return len(p.results) == 2 })
}

// TestSearchLabelFilterDeleted: a filter on a label deleted since is dropped,
// not kept as an invisible filter that finds nothing.
func TestSearchLabelFilterDeleted(t *testing.T) {
	h, acc, m := labeledMail(t)
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Check-in closed", "too late"))
	h.syncNow(acc)
	h.autoLabel(m.ID, "otp")
	p := h.app.mail
	p.searchOpen = true
	p.query = "Check-in"
	p.sf.labelID = h.labelID("otp")
	p.runSearch()
	h.until("the filtered result", func() bool { h.tt.Frame(); return p.searched && len(p.results) == 1 })
	if err := h.env.Svc.DeleteLabel(context.Background(), h.labelID("otp")); err != nil {
		t.Fatal(err)
	}
	h.waitMail("both results again", func() bool { return len(p.results) == 2 && p.sf.labelID == 0 })
}

// TestLabelViewArchive: archiving from a label view moves the mail, as in
// any other view.
func TestLabelViewArchive(t *testing.T) {
	h, acc, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	h.app.navigate(Route{Page: PageMail, View: core.View{LabelID: h.labelID("otp")}})
	p := h.app.mail
	h.waitMail("the label view", func() bool { return len(p.items) == 1 })
	p.batch("archive", p.items[:1])
	archive := h.folderOf(acc, "archive")
	h.waitMail("the mail archived", func() bool {
		page, err := h.env.Svc.ListMessages(context.Background(), core.View{FolderID: archive.ID}, "", 10)
		return err == nil && len(page.Items) == 1
	})
}

// TestLabelViewDeleted: a label view whose label is deleted goes to the
// inbox, and back no longer leads to it.
func TestLabelViewDeleted(t *testing.T) {
	h, _, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	otp := h.labelID("otp")
	h.app.navigate(Route{Page: PageMail, View: core.View{LabelID: otp}})
	h.app.navigate(Route{Page: PageSettings, Settings: "labels"})
	if err := h.env.Svc.DeleteLabel(context.Background(), otp); err != nil {
		t.Fatal(err)
	}
	h.app.navigate(Route{Page: PageMail, View: core.View{LabelID: otp}}) // a stale link
	h.waitMail("the inbox", func() bool { return h.app.route.View == core.View{Unified: "inbox"} })
	for _, r := range h.app.history {
		if r.View.LabelID == otp {
			t.Errorf("history keeps the deleted label view: %+v", h.app.history)
		}
	}
}

package app

import (
	"context"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

// TestDeadlineRowText: a row's reply date, as the design writes it, on a Wednesday.
func TestDeadlineRowText(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		d       core.Deadline
		want    string
		overdue bool
	}{
		{core.Deadline{Date: "2026-11-25"}, "今天", false},
		{core.Deadline{Date: "2026-11-25", Time: "15:00"}, "今天 15:00 前", false},
		{core.Deadline{Date: "2026-11-26"}, "明天前", false},
		{core.Deadline{Date: "2026-11-26", Time: "15:00"}, "明天 15:00 前", false},
		{core.Deadline{Date: "2026-11-27"}, "周五前", false},
		{core.Deadline{Date: "2026-11-30"}, "下周一前", false},
		{core.Deadline{Date: "2026-12-12"}, "12月12日前", false},
		{core.Deadline{Date: "2026-11-23", Overdue: true}, "已过期 2 天", true},
		{core.Deadline{Date: "2026-11-25", Time: "09:00", Overdue: true}, "已过期", true},
	} {
		got, overdue := deadlineText(now, c.d)
		if got != c.want || overdue != c.overdue {
			t.Errorf("%+v: got %q %v, want %q %v", c.d, got, overdue, c.want, c.overdue)
		}
	}
}

// datedHarness opens the view "Acme" of doc on two unreplied conversations,
// each with a reply date a program set: "soon" tomorrow, "late" yesterday.
func datedHarness(t *testing.T, doc core.SavedViewDoc) *harness {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.clock.mu.Lock()
	h.clock.now = time.Now() // the backend's clock, which the dates follow
	h.clock.mu.Unlock()
	h.put(accs[0], "INBOX", coretest.Msg("anna@acme.example", "alice@example.com", "soon", "Please reply by tomorrow."))
	h.put(accs[0], "INBOX", coretest.Msg("ben@acme.example", "alice@example.com", "late", "Please reply by yesterday."))
	h.syncNow(accs[0])
	if _, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: doc}); err != nil {
		t.Fatal(err)
	}
	h.savedViewNamed("Acme")
	h.click("Acme")
	h.waitMail("the rows", func() bool { return !h.app.mail.list.loading && len(h.items()) == 2 })
	for _, it := range h.items() {
		days := map[string]int{"soon": 1, "late": -1}[it.Subject]
		d := core.Deadline{Date: time.Now().AddDate(0, 0, days).Format(time.DateOnly), MessageID: h.messages(it.ID)[0].ID, Quote: "Please reply by"}
		if err := h.env.Svc.SetThreadDeadline(context.Background(), it.ID, d); err != nil {
			t.Fatal(it.Subject, err)
		}
	}
	return h
}

var condFromAcme = core.RuleCondition{Field: core.FieldFrom, Op: core.OpContains, Value: "acme"}

// TestViewRowReplyDate: a row says its reply date after its 回复状态, overdue
// ones first; in a view not about 回复状态, the date alone.
func TestViewRowReplyDate(t *testing.T) {
	h := datedHarness(t, core.SavedViewDoc{Match: core.MatchAll, Conditions: []core.RuleCondition{condFromAcme},
		Groups: []core.ViewGroup{{ID: "r", Name: "R", Match: core.MatchAll, Conditions: []core.RuleCondition{{Field: core.FieldReply, Op: core.OpIs, Value: core.ViewReplyUnreplied}}}}})
	h.waitMail("the dates", func() bool { return h.tt.HasText("明天前") && h.tt.HasText("已过期 1 天") })
	h.see(i18n.T("view.status.unreplied.today"))
	if got := h.items()[0].Subject; got != "late" {
		t.Fatalf("first row %q, want the overdue one", got)
	}

	b := h.savedViewNamed("Acme")
	b.Doc.Groups = nil
	if _, err := h.env.Svc.SaveView(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the date alone", func() bool {
		return h.tt.HasText("明天前") && !h.tt.HasText(i18n.T("view.status.unreplied.today"))
	})
}

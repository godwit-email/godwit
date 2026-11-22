package app

import (
	"context"
	"fmt"
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

// TestDeadlineReaderText: the reader's reply date line, its quote and source.
func TestDeadlineReaderText(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		d                   core.Deadline
		lead, quote, source string
	}{
		{core.Deadline{Date: "2026-11-27", Quote: "请周五前确认报价", Source: core.DeadlineAI}, "对方希望 周五（11月27日）前回复", "：「请周五前确认报价」", "邮件总结找到"},
		{core.Deadline{Date: "2026-11-27", Time: "15:00", Source: core.DeadlineProgram}, "对方希望 周五（11月27日）15:00 前回复", "", "其他应用设置"},
		{core.Deadline{Date: "2026-11-24", Quote: "明天给我", Source: core.DeadlineWit, Overdue: true}, "对方希望 周二（11月24日）前回复", "：「明天给我」", "Wit 设置"},
		{core.Deadline{Date: "2026-11-27", Source: core.DeadlineManual}, "你设的：周五（11月27日）前回复", "", ""},
		{core.Deadline{Date: "2026-11-15", Quote: "尽快", Source: core.DeadlineAI, Overdue: true}, "对方希望 11月15日前回复，已过去 10 天", "", "邮件总结找到"},
	} {
		lead, quote, source := deadlineSentence(now, c.d)
		if lead != c.lead || quote != c.quote || source != c.source {
			t.Errorf("%+v: got %q %q %q", c.d, lead, quote, source)
		}
	}
}

// datedHarness opens the view "Acme" of doc on two unreplied conversations,
// each with a reply date a program set: "soon" tomorrow, "late" yesterday.
func datedHarness(t *testing.T, doc core.SavedViewDoc) *harness {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.useBackendClock()
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

// useBackendClock sets the app's clock to the backend's, which reply status
// and reply dates follow.
func (h *harness) useBackendClock() {
	h.clock.mu.Lock()
	h.clock.now = time.Now()
	h.clock.mu.Unlock()
}

// datedThread is thread2 with a reply date a program set on its older
// message, quoting it.
func (h *harness) datedThread(acc core.Account) []core.MessageSummary {
	h.useBackendClock()
	msgs := h.thread2(acc)
	if err := h.env.Svc.SetThreadDeadline(context.Background(), msgs[0].ThreadID, core.Deadline{Date: "2027-01-15", MessageID: msgs[0].ID, Quote: "first words"}); err != nil {
		h.t.Fatal(err)
	}
	return msgs
}

// TestReaderReplyDate: opened outside any view, a conversation says its reply
// date with no summary, in one 等你处理 line; its quote opens the message.
func TestReaderReplyDate(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.datedThread(accs[0])
	h.openThreadWith(msgs[0].ThreadID)
	h.see("对方希望 周五（1月15日）前回复")
	h.see("其他应用设置")
	if n := h.countExact("等你处理"); n != 1 {
		t.Fatalf("%d 等你处理 lines", n)
	}
	r := h.app.mail.reader
	if r.open[msgs[0].ID] {
		t.Fatal("the older message is open before its quote is clicked")
	}
	h.click("：「first words」")
	h.until("the quoted message open", func() bool { return r.open[msgs[0].ID] })
}

// TestReaderReplyDateInDigest: a thread's summary carries the reply date in
// its 等你处理 line, not in a second one.
func TestReaderReplyDateInDigest(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.datedThread(acc)
	r := h.app.mail.reader
	r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
	digestSkill(r, func() string {
		return fmt.Sprintf("PROGRESS\n- Bob proposed a plan <mail id=\"%d\"/>\nTODO: Confirm the plan\n", msgs[0].ID)
	})
	h.openThread(msgs[0].ThreadID)
	h.see("Confirm the plan")
	h.see("对方希望 周五（1月15日）前回复")
	if n := h.countExact("等你处理"); n != 1 {
		t.Fatalf("%d 等你处理 lines", n)
	}
}

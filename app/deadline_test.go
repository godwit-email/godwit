package app

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	mygoi18n "github.com/egoist/mygo/i18n"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
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

// TestRowStatusLine: an unreplied row says 未回复 and its reply date, never
// when it came or how long it has waited; a 待跟进 row keeps its wait.
func TestRowStatusLine(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.Local) // a Monday
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	unreplied := func(came time.Time, d *core.Deadline) mailItem {
		return mailItem{Status: core.ReplyStatusUnreplied, Date: came, Deadline: d}
	}
	for _, c := range []struct {
		it      mailItem
		want    string
		overdue bool
	}{
		{unreplied(now, nil), "未回复", false},
		{unreplied(ago(12), nil), "未回复", false},
		{unreplied(now, &core.Deadline{Date: "2026-11-23"}), "未回复 · 今天", false},
		{unreplied(ago(1), &core.Deadline{Date: "2026-11-24", Time: "15:00"}), "未回复 · 明天 15:00 前", false},
		{unreplied(ago(3), &core.Deadline{Date: "2026-11-21", Overdue: true}), "未回复 · 已过期 2 天", true},
		{unreplied(ago(2), &core.Deadline{Date: "2026-11-27"}), "未回复 · 周五前", false},
		{mailItem{Status: core.ReplyStatusWaiting, WaitingDays: 4, Date: ago(4), Deadline: &core.Deadline{Date: "2026-11-26"}}, "对方未回复 · 已等 4 天 · 周四前", false},
		{mailItem{Date: ago(2), Deadline: &core.Deadline{Date: "2026-11-27"}}, "周五前", false},
	} {
		_, line, due, overdue := rowStatus(now, c.it)
		got := strings.Join(slices.DeleteFunc([]string{line, due}, func(s string) bool { return s == "" }), " · ")
		if got != c.want || overdue != c.overdue {
			t.Errorf("%+v: got %q %v, want %q %v", c.it, got, overdue, c.want, c.overdue)
		}
	}
}

// TestDeadlineReaderText: the reader's reply date line (with the colon its
// quote follows), the quote and the source.
func TestDeadlineReaderText(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		d                   core.Deadline
		lead, quote, source string
	}{
		{core.Deadline{Date: "2026-11-27", Quote: "请周五前确认报价", Source: core.DeadlineAI}, "对方希望 周五（11月27日）前回复：", "请周五前确认报价", "邮件总结找到"},
		{core.Deadline{Date: "2026-11-27", Time: "15:00", Source: core.DeadlineProgram}, "对方希望 周五（11月27日）15:00 前回复", "", "其他应用设置"},
		{core.Deadline{Date: "2026-11-24", Quote: "明天给我", Source: core.DeadlineWit, Overdue: true}, "对方希望 周二（11月24日）前回复：", "明天给我", "Wit 设置"},
		{core.Deadline{Date: "2026-11-27", Source: core.DeadlineManual}, "你设的：周五（11月27日）前回复", "", ""},
		// today and tomorrow in the row's words
		{core.Deadline{Date: "2026-11-25", Source: core.DeadlineManual}, "你设的：今天回复", "", ""},
		{core.Deadline{Date: "2026-11-25", Time: "15:00", Quote: "今天给我", Source: core.DeadlineAI}, "对方希望今天 15:00 前回复：", "今天给我", "邮件总结找到"},
		{core.Deadline{Date: "2026-11-25", Time: "09:00", Source: core.DeadlineProgram, Overdue: true}, "对方希望今天 09:00 前回复", "", "其他应用设置"},
		{core.Deadline{Date: "2026-11-26", Source: core.DeadlineProgram}, "对方希望 明天（11月26日）前回复", "", "其他应用设置"},
		{core.Deadline{Date: "2026-11-26", Time: "15:00", Source: core.DeadlineManual}, "你设的：明天（11月26日）15:00 前回复", "", ""},
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
		if _, err := h.env.Svc.SetThreadDeadline(context.Background(), it.ID, d); err != nil {
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
	h.see("未回复")
	if got := h.items()[0].Subject; got != "late" {
		t.Fatalf("first row %q, want the overdue one", got)
	}

	b := h.savedViewNamed("Acme")
	b.Doc.Groups = nil
	if _, err := h.env.Svc.SaveView(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the date alone", func() bool {
		return h.tt.HasText("明天前") && !h.tt.HasText("未回复")
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
	if _, err := h.env.Svc.SetThreadDeadline(context.Background(), msgs[0].ThreadID, core.Deadline{Date: "2027-01-15", MessageID: msgs[0].ID, Quote: "first words"}); err != nil {
		h.t.Fatal(err)
	}
	return msgs
}

// TestReaderReplyDate: opened outside any view, a conversation says its reply
// date with no summary, in one 等你处理 line, its 回复状态 under it; its
// quote opens the message.
func TestReaderReplyDate(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.datedThread(accs[0])
	h.resize(1440, 900)
	h.openThreadWith(msgs[0].ThreadID)
	h.see("对方希望 周五（1月15日）前回复：")
	h.see("其他应用设置")
	r := h.app.mail.reader
	h.see(r.unrepliedText())
	if n := h.countExact("等你处理"); n != 1 {
		t.Fatalf("%d 等你处理 lines", n)
	}
	h.seeCardOnTop("Plan", "发给 我")
	if r.open[msgs[0].ID] {
		t.Fatal("the older message is open before its quote is clicked")
	}
	h.click("first words")
	h.until("the quoted message open", func() bool { return r.open[msgs[0].ID] })
}

// TestReaderReplyDateSingle: a message alone has its 等你处理 card where a
// thread has it, under the subject above the message, and its 回复状态
// under the date as small and muted as the source, with no full stop.
func TestReaderReplyDateSingle(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.resize(1440, 900)
	h.useBackendClock()
	h.put(accs[0], "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Lone ask", "Please reply by Friday.", at(h.clock.Now(), 24*3)))
	h.syncNow(accs[0])
	page, err := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("threads %+v, %v", page.Items, err)
	}
	msgs := h.messages(page.Items[0].ID)
	if _, err := h.env.Svc.SetThreadDeadline(context.Background(), msgs[0].ThreadID, core.Deadline{Date: "2027-01-15", MessageID: msgs[0].ID, Quote: "Please reply"}); err != nil {
		t.Fatal(err)
	}
	h.openThreadWith(msgs[0].ThreadID)
	h.see("对方希望 周五（1月15日）前回复：")
	h.seeCardOnTop("Lone ask", "发给 我")
	status := "直接发给你，尚未回复，已等 3 天"
	if h.countExact(status) != 1 {
		t.Fatalf("no %q in %q", status, h.tt.Texts())
	}
	got, _ := h.tt.Find(status)
	src, _ := h.tt.Find("其他应用设置")
	if got.H != src.H || got.X != src.X {
		t.Fatalf("status %v, source %v: not alike", got, src)
	}
}

// TestReaderReplyDateSingleSummary: a message alone with its summary carries
// its 等你处理 line in that summary, under the sender, and in no card of its own.
func TestReaderReplyDateSingleSummary(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	h.resize(1440, 900)
	h.useBackendClock()
	h.put(acc, "INBOX", coretest.Msg("Bob <bob@example.com>", acc.Email, "Lone ask", "Please reply by Friday."))
	h.syncNow(acc)
	page, err := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("threads %+v, %v", page.Items, err)
	}
	msgs := h.messages(page.Items[0].ID)
	if _, err := h.env.Svc.SetThreadDeadline(context.Background(), msgs[0].ThreadID, core.Deadline{Date: "2027-01-15", MessageID: msgs[0].ID}); err != nil {
		t.Fatal(err)
	}
	h.openThreadWith(msgs[0].ThreadID)
	r := h.app.mail.reader
	h.until("the summary", func() bool { return r.summaryOn(r.ai[msgs[0].ID]) })
	h.see("对方希望 周五（1月15日）前回复")
	if n := h.countExact("等你处理"); n != 1 {
		t.Fatalf("%d 等你处理 lines", n)
	}
	label, _ := h.tt.Find("等你处理")
	head, _ := h.tt.Find("发给 我")
	if label.Y <= head.Y {
		t.Fatalf("等你处理 at %v, the message's header at %v: not in the summary under it", label, head)
	}
}

// TestReaderBandReplyDateOutsideView: opened outside any view, a
// conversation waiting on someone else's reply says the user's reply date
// in its 待跟进 band, with 改日期 and 写催促信, and has no 等你处理 card;
// one with no date has no band.
func TestReaderBandReplyDateOutsideView(t *testing.T) {
	h, accs := mailHarness(t, nil, "you@example.com")
	h.useBackendClock()
	acc := accs[0]
	h.putSeen(acc, "Sent", coretest.Msg("Me <you@example.com>", "Tom <tom@acme.example>", "Renewal quote", "The quote is attached.", coretest.Date(time.Now().AddDate(0, 0, -4).Add(-time.Hour))))
	h.syncNow(acc)
	h.click("已发送")
	h.waitMail("the sent row", func() bool { return !h.app.mail.list.loading && len(h.items()) == 1 })
	tid := h.items()[0].ID
	h.openThreadWith(tid)
	if h.tt.HasText(i18n.Tf("view.band.waiting", 4)) {
		t.Fatal("a 待跟进 band with no reply date, outside a view")
	}
	day := time.Now().AddDate(0, 0, 3)
	h.app.setReplyDate(tid, core.Deadline{Date: day.Format(time.DateOnly)})
	h.seeRefreshed("你设的：" + weekdayDay(day) + "前回复")
	h.see(i18n.Tf("view.band.waiting", 4))
	h.see(i18n.T("view.follow_up"))
	if h.tt.HasText("等你处理") {
		t.Fatal("a 等你处理 card on a conversation waiting on someone else")
	}
	h.click("改日期")
	if got, want := h.tt.Menu(), dateMenu(time.Now(), true); !slices.Equal(got, want) {
		t.Fatalf("menu %q, want %q", got, want)
	}
}

// seeCardOnTop checks the 等你处理 card is above the message (whose header
// says to), its label in line with the title: the badge's text 6 in from
// its side, as kit.Badge has it.
func (h *harness) seeCardOnTop(title, to string) {
	h.t.Helper()
	label, _ := h.tt.Find("等你处理")
	head, _ := h.tt.Find(to)
	if label.Y >= head.Y {
		h.t.Fatalf("等你处理 at %v, the message's header at %v: not above it", label, head)
	}
	if ti, _ := h.tt.Find(title); label.X != ti.X+6 {
		h.t.Fatalf("等你处理 at %v, the title at %v: not in line", label, ti)
	}
}

// TestReaderReplyDateInDigest: a thread's summary carries the reply date in
// its 等你处理 line, not in a second one, and in place of its to-do.
func TestReaderReplyDateInDigest(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.datedThread(acc)
	r := h.app.mail.reader
	r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
	digestSkill(r, func() string {
		return fmt.Sprintf("PROGRESS\n- Bob proposed a plan <mail id=\"%d\"/>\nTODO: Confirm the plan\n", msgs[0].ID)
	})
	h.openThread(msgs[0].ThreadID)
	h.see("Bob proposed a plan")
	h.see("对方希望 周五（1月15日）前回复：")
	if n := h.countExact("等你处理"); n != 1 {
		t.Fatalf("%d 等你处理 lines", n)
	}
	if h.tt.HasText("Confirm the plan") {
		t.Fatal("the summary's to-do beside the reply date")
	}
}

// TestViewBandReplyDate: a conversation waiting on someone else, in a view,
// says the user's reply date in its 待跟进 band, with 改日期, and has no
// 等你处理 card.
func TestViewBandReplyDate(t *testing.T) {
	h, accs := mailHarness(t, nil, "you@example.com")
	h.useBackendClock()
	acc := accs[0]
	h.putSeen(acc, "Sent", coretest.Msg("Me <you@example.com>", "Tom <tom@acme.example>", "Renewal quote", "The quote is attached.", coretest.Date(time.Now().AddDate(0, 0, -4).Add(-time.Hour))))
	h.syncNow(acc)
	if _, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldTo, Op: core.OpContains, Value: "acme.example"}},
		Groups:     []core.ViewGroup{{ID: "w", Name: "待跟进", Match: core.MatchAll, Conditions: []core.RuleCondition{{Field: core.FieldReply, Op: core.OpOlderThan, Value: "3"}}}}}}); err != nil {
		t.Fatal(err)
	}
	h.click(h.savedViewNamed("Acme").Name)
	h.waitMail("the row", func() bool { return len(h.items()) == 1 })
	tid := h.items()[0].ID
	day := time.Now().AddDate(0, 0, 3)
	h.app.setReplyDate(tid, core.Deadline{Date: day.Format(time.DateOnly)})
	h.until("the user's date", func() bool { d := h.replyDateIn(tid); return d != nil && d.Source == core.DeadlineManual })
	h.app.mail.open(h.items(), 0)
	h.see(i18n.Tf("view.band.waiting", 4))
	h.seeRefreshed("你设的：" + weekdayDay(day) + "前回复")
	h.see(i18n.T("view.follow_up"))
	if h.tt.HasText("等你处理") {
		t.Fatal("a 等你处理 card on a conversation waiting on someone else")
	}
	h.click("改日期")
	if got, want := h.tt.Menu(), dateMenu(time.Now(), true); !slices.Equal(got, want) {
		t.Fatalf("menu %q, want %q", got, want)
	}
}

// replyDateIn reads the conversation's reply date as the reader would.
func (h *harness) replyDateIn(thread int64) *core.Deadline {
	h.t.Helper()
	got, err := h.env.Svc.ThreadDeadline(context.Background(), thread)
	if err != nil {
		h.t.Fatal(err)
	}
	return got.Deadline
}

// seeRefreshed waits for text once the app has reread on a change's event.
func (h *harness) seeRefreshed(text string) {
	h.t.Helper()
	h.waitMail(fmt.Sprintf("text %q", text), func() bool { return h.tt.HasText(text) })
}

// dateMenu is the native menu 改日期 and 设回复日期 open, as it should read
// at now: the presets with their days, 选日期…, and 去掉 only with a date.
func dateMenu(now time.Time, dated bool) []string {
	var want []string
	for _, p := range replyDatePresets(now) {
		want = append(want, p.label+" · "+monthDay(p.day))
	}
	want = append(want, "-", "选日期…")
	if dated {
		want = append(want, "-", "去掉")
	}
	return want
}

// TestReaderChangeReplyDate: 改日期 opens a menu of presets; one makes the
// date the user's, and the toast's 撤销 puts the program's back.
func TestReaderChangeReplyDate(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.datedThread(accs[0])
	tid := msgs[0].ThreadID
	h.openThreadWith(tid)
	h.see("对方希望 周五（1月15日）前回复")
	h.click("改日期")
	if got, want := h.tt.Menu(), dateMenu(time.Now(), true); !slices.Equal(got, want) {
		t.Fatalf("menu %q, want %q", got, want)
	}
	tomorrow := time.Now().AddDate(0, 0, 1)
	h.click(i18n.T("view.deadline.tomorrow") + " · " + monthDay(tomorrow))
	h.until("the user's date", func() bool {
		d := h.replyDateIn(tid)
		return d != nil && d.Source == core.DeadlineManual && d.Date == tomorrow.Format(time.DateOnly)
	})
	h.seeRefreshed("你设的：")
	if d := h.replyDateIn(tid); d.Time != "" {
		t.Fatalf("a preset set a time: %+v", d)
	}
	h.click("撤销")
	h.until("the program's date back", func() bool { d := h.replyDateIn(tid); return d != nil && d.Source == core.DeadlineProgram })
	h.seeRefreshed("对方希望 周五（1月15日）前回复")
}

// TestReaderRemoveReplyDate: the menu's 去掉 leaves the unreplied line with
// 设回复日期, whose menu has no 去掉, and 撤销 brings the date back.
func TestReaderRemoveReplyDate(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.datedThread(accs[0])
	tid := msgs[0].ThreadID
	h.openThreadWith(tid)
	h.see("对方希望 周五（1月15日）前回复")
	if h.tt.HasText("去掉") {
		t.Fatal("去掉 on the line, not only in the menu")
	}
	h.click("改日期")
	h.click("去掉")
	h.until("removed", func() bool { return h.replyDateIn(tid) == nil })
	h.seeRefreshed(i18n.T("view.band.unreplied"))
	h.click("设回复日期")
	if got, want := h.tt.Menu(), dateMenu(time.Now(), false); !slices.Equal(got, want) {
		t.Fatalf("menu %q, want %q", got, want)
	}
	h.tt.CloseMenu()
	h.click("撤销")
	h.until("back", func() bool { return h.replyDateIn(tid) != nil })
	h.seeRefreshed("对方希望 周五（1月15日）前回复")
}

// dayCell is the calendar's name for day, as the dialog's day buttons say it
// in the interface language (zh-Hans).
func dayCell(day time.Time) string {
	return fmt.Sprintf("%d年%d月%d日", day.Year(), day.Month(), day.Day())
}

// TestReplyDateDialogLanguage: 选日期…'s calendar is in the interface
// language, its week starting on the region's first day.
func TestReplyDateDialogLanguage(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.tt.SetRegion(mygoi18n.Region{Locale: "en-US", FirstWeekday: time.Sunday, HourCycle: 12})
	msgs := h.datedThread(accs[0])
	h.openThreadWith(msgs[0].ThreadID)
	h.click("改日期")
	h.click("选日期…")
	h.see("希望在什么时候之前回复")
	h.see(weekdayDay(time.Date(2027, 1, 15, 0, 0, 0, 0, time.Local)) + "前回复")
	h.see("2027年1月")
	sun, _ := h.tt.Find("周日")
	mon, _ := h.tt.Find("周一")
	if sun.X >= mon.X {
		t.Errorf("周日 at %v, 周一 at %v: the week does not start on the region's Sunday", sun.X, mon.X)
	}
	if _, ok := h.tt.Access(dayCell(time.Date(2027, 1, 12, 0, 0, 0, 0, time.Local))); !ok {
		t.Errorf("the days are not named in the interface language: %q", h.tt.Texts())
	}
}

// TestReaderPickReplyDate: 选日期… opens the dialog on today with any time;
// 取消 sets nothing; a day with any time sets that day; with a time, one
// gone by today is refused and a later day's is set.
func TestReaderPickReplyDate(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.useBackendClock()
	msgs := h.thread2(accs[0])
	tid := msgs[0].ThreadID
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	h.clock.mu.Lock()
	h.clock.now = today.Add(20 * time.Hour) // after the dialog's 18:00, so today's is gone by
	h.clock.mu.Unlock()
	later := today.AddDate(0, 0, 2)
	h.openThreadWith(tid)

	h.click("设回复日期")
	h.click("选日期…")
	h.see("希望在什么时候之前回复")
	h.see(weekdayDay(today) + "前回复")
	h.click("取消")
	h.until("closed", func() bool { return !h.app.dateAsk.open })
	if d := h.replyDateIn(tid); d != nil {
		t.Fatalf("取消 set %+v", d)
	}

	h.click("设回复日期")
	h.click("选日期…")
	h.click(dayCell(later))
	h.see(weekdayDay(later) + "前回复")
	h.click("设定")
	h.until("the day set", func() bool {
		d := h.replyDateIn(tid)
		return d != nil && d.Source == core.DeadlineManual && d.Date == later.Format(time.DateOnly) && d.Time == ""
	})
	h.seeRefreshed("你设的：" + weekdayDay(later) + "前回复")

	h.click("改日期")
	h.click("选日期…")
	h.click("不限时间")
	h.click(dayCell(today))
	h.click("设定")
	h.see("请选择一个将来的时间。")
	if !h.app.dateAsk.open {
		t.Fatal("a time gone by closed the dialog")
	}
	h.click(dayCell(later))
	h.see(weekdayDay(later) + "18:00 前回复")
	h.click("设定")
	h.until("the day and time set", func() bool {
		d := h.replyDateIn(tid)
		return d != nil && d.Date == later.Format(time.DateOnly) && d.Time == "18:00"
	})
	h.seeRefreshed("你设的：" + weekdayDay(later) + "18:00 前回复")
}

// TestReplyDateShots writes the reader's 等你处理 line with a date and 选日期…'s
// dialog to $GODWIT_SHOTS, at 1440×900.
func TestReplyDateShots(t *testing.T) {
	if os.Getenv("GODWIT_SHOTS") == "" {
		t.Skip("GODWIT_SHOTS not set")
	}
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.resize(1440, 900)
	msgs := h.datedThread(accs[0])
	h.openThreadWith(msgs[0].ThreadID)
	h.see("对方希望 周五（1月15日）前回复")
	settle := func() {
		h.tt.Advance(2 * theme.MotionBase)
		h.drain()
		h.tt.Frame()
	}
	settle()
	shoot(t, h.tt, "reply-line.png")
	h.click("改日期")
	h.click("选日期…")
	h.see("希望在什么时候之前回复")
	settle()
	shoot(t, h.tt, "reply-dialog.png")
}

// TestReaderSetReplyDateKeepsTodo:with no reply date, a thread summary's
// 等你处理 line keeps its to-do and offers 设回复日期.
func TestReaderSetReplyDateKeepsTodo(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	h.useBackendClock()
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
	digestSkill(r, func() string {
		return fmt.Sprintf("PROGRESS\n- Bob proposed a plan <mail id=\"%d\"/>\nTODO: Confirm the plan\n", msgs[0].ID)
	})
	h.openThread(msgs[0].ThreadID)
	h.see("Confirm the plan")
	h.see("设回复日期")
	if h.tt.HasText(i18n.T("view.band.unreplied")) {
		t.Fatal("the reply status beside the summary's to-do")
	}
}

// TestMailMenuReplyDate: a conversation's row menu sets a reply date, and
// removes it once there is one.
func TestMailMenuReplyDate(t *testing.T) {
	h := datedHarness(t, core.SavedViewDoc{Match: core.MatchAll, Conditions: []core.RuleCondition{condFromAcme}})
	h.waitMail("the dates", func() bool { return h.tt.HasText("明天前") })
	if err := h.tt.RightClick("soon"); err != nil {
		t.Fatal(err)
	}
	menu := h.tt.Menu()
	// in 标为未读 and 加星's group, before 归档's
	if i := slices.Index(menu, "加星"); i < 0 || !slices.Equal(menu[i+1:i+4], []string{"设回复日期…", "去掉回复日期", "-"}) {
		t.Fatalf("menu %q: the reply date's items not after 加星", menu)
	}
	var soon int64
	for _, it := range h.items() {
		if it.Subject == "soon" {
			soon = it.ID
		}
	}
	h.click("去掉回复日期")
	h.until("removed", func() bool { return h.replyDateIn(soon) == nil })
	h.waitMail("the row's date gone", func() bool { return !h.tt.HasText("明天前") })
	if err := h.tt.RightClick("soon"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.tt.Menu(), "去掉回复日期") {
		t.Fatal("去掉回复日期 with no date")
	}
	h.click("设回复日期…")
	h.see("希望在什么时候之前回复")
	h.click("设定")
	h.until("set", func() bool {
		d := h.replyDateIn(soon)
		return d != nil && d.Source == core.DeadlineManual && d.Date == time.Now().Format(time.DateOnly) && d.Time == ""
	})
}

// TestArrivalFooter: the date dialog's note points to 收到就总结 only while
// it is offered; under it, it says new mail's dates come from it; chosen
// before, that they no longer do.
func TestArrivalFooter(t *testing.T) {
	old := core.SummaryOnArrival
	t.Cleanup(func() { core.SummaryOnArrival = old })
	model := core.Settings{DefaultModel: "x/m", SummaryMode: core.SummaryOpen}
	arrive := core.Settings{DefaultModel: "x/m", SummaryMode: core.SummaryArrive}
	for _, c := range []struct {
		offered bool
		st      core.Settings
		chosen  string
		note    string
	}{
		{false, model, "", ""},
		{true, core.Settings{SummaryMode: core.SummaryOff}, "", ""},
		{true, model, "", "在设置的「AI 功能」里把邮件总结设为「收到就总结」，新邮件的回复日期会在收到时找出。"},
		{true, arrive, "x", "新邮件的回复日期由邮件总结在收到时找出。"},
		{true, model, "x", "新邮件不再在收到时总结。"},
	} {
		core.SummaryOnArrival = c.offered
		if note := arrivalNote(&App{settings: c.st, arrival: core.SummaryArrival{Service: c.chosen}}); note != c.note {
			t.Errorf("%+v: %q", c, note)
		}
	}
}

// TestReplyDatePresetChecked: the date menu checks the preset that is the
// reply date: its day, with no time.
func TestReplyDatePresetChecked(t *testing.T) {
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.Local) // a Wednesday: 今天, 明天, 周五, 下周一
	for _, c := range []struct {
		d    *core.Deadline
		want string
	}{
		{nil, ""},
		{&core.Deadline{Date: "2026-11-27"}, "周五"},
		{&core.Deadline{Date: "2026-11-25"}, "今天"},
		{&core.Deadline{Date: "2026-11-27", Time: "15:00"}, ""},
		{&core.Deadline{Date: "2026-11-28"}, ""},
	} {
		got := ""
		for _, p := range replyDatePresets(now) {
			if p.is(c.d) {
				got += p.label
			}
		}
		if got != c.want {
			t.Errorf("%+v: checked %q, want %q", c.d, got, c.want)
		}
	}
}

// TestReplyDatePresets: 周五 drops when it is today or tomorrow, 下周一 when
// it is tomorrow.
func TestReplyDatePresets(t *testing.T) {
	for _, c := range []struct {
		now  string
		want []string
	}{
		{"2026-11-25", []string{"今天 25", "明天 26", "周五 27", "下周一 30"}}, // Wednesday
		{"2026-11-26", []string{"今天 26", "明天 27", "下周一 30"}},          // Thursday
		{"2026-11-27", []string{"今天 27", "明天 28", "下周一 30"}},          // Friday
		{"2026-11-29", []string{"今天 29", "明天 30", "周五 4"}},            // Sunday
	} {
		now, _ := time.ParseInLocation(time.DateOnly, c.now, time.Local)
		var got []string
		for _, p := range replyDatePresets(now.Add(15 * time.Hour)) {
			got = append(got, fmt.Sprint(p.label, " ", p.day.Day()))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.now, got, c.want)
		}
	}
}

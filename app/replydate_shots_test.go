package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// TestReplyDateUIShots writes the reply date's screens to $GODWIT_SHOTS at
// 1440×900, named as the design check's (A: a thread with its summary, B:
// messages alone with none, S: the settings), each once the reader shows
// what it is about.
func TestReplyDateUIShots(t *testing.T) {
	if os.Getenv("GODWIT_SHOTS") == "" {
		t.Skip("GODWIT_SHOTS not set")
	}
	shot := func(h *harness, name, wait string) {
		t.Helper()
		h.seeRefreshed(wait)
		h.tt.Advance(2 * theme.MotionBase)
		h.drain()
		h.tt.Frame()
		shoot(t, h.tt, name+".png")
	}
	toHandle := func(h *harness, rows int) {
		t.Helper()
		h.waitMail("待处理", func() bool { return len(h.app.mail.views) > 0 })
		h.click(h.app.mail.views[0].Name)
		h.waitMail("its rows", func() bool { return !h.app.mail.list.loading && len(h.items()) == rows })
	}
	program := func(h *harness, thread, msg int64, day time.Time, quote string) {
		t.Helper()
		if _, err := h.env.Svc.SetThreadDeadline(context.Background(), thread, core.Deadline{Date: day.Format(time.DateOnly), MessageID: msg, Quote: quote}); err != nil {
			t.Fatal(err)
		}
	}
	threadOf := func(h *harness, subject string) int64 {
		t.Helper()
		for _, it := range h.items() {
			if it.Subject == subject {
				return it.ID
			}
		}
		t.Fatalf("no row %q", subject)
		return 0
	}
	now := time.Now()
	day := func(d int) time.Time { return now.AddDate(0, 0, d) }
	ago := func(d int) coretest.MsgOption { return coretest.Date(now.Add(-time.Duration(d) * 24 * time.Hour)) }

	t.Run("A", func(t *testing.T) {
		h, acc, _ := aiHarness(t, true, noQuick)
		h.resize(1440, 900)
		h.useBackendClock()
		h.put(acc, "INBOX", coretest.Msg("王磊 <wang@acme.example>", acc.Email, "Q4 报价确认", "附件是 Q4 报价单，请周五前确认报价。", coretest.MessageID("q1@x"), ago(1)))
		h.put(acc, "INBOX", coretest.Msg("李娜 <li@acme.example>", acc.Email, "Re: Q4 报价确认", "交付档期我们这边没问题。", coretest.MessageID("q2@x"), coretest.InReplyTo("q1@x"), coretest.Date(now.Add(-time.Hour))))
		h.put(acc, "INBOX", coretest.Msg("周宁 <zhou@corp.example>", acc.Email, "下周例会时间", "下周例会改到周三下午可以吗？", ago(2)))
		h.syncNow(acc)
		toHandle(h, 2)
		tid := threadOf(h, "Q4 报价确认")
		msgs := h.messages(tid)
		program(h, tid, msgs[0].ID, day(0), "请周五前确认报价")
		r := h.app.mail.reader
		r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
		digestSkill(r, func() string {
			return fmt.Sprintf("PROGRESS\n- 王磊发来 Q4 报价单 <mail id=\"%d\"/>\n- 李娜确认交付档期没问题 <mail id=\"%d\"/>\nTODO: 确认档期和最终报价单\n", msgs[0].ID, msgs[1].ID)
		})
		h.click("Q4 报价确认")
		shot(h, "A1-list-reader-summary", "请周五前确认报价")
		h.click("改日期")
		h.click("选日期…")
		shot(h, "A2-change-date-popover", "希望在什么时候之前回复")
		h.click("取消")
		h.until("closed", func() bool { return !h.app.dateAsk.open })
		h.click("改日期")
		h.click(i18n.T("view.deadline.tomorrow") + " · " + monthDay(day(1)))
		shot(h, "A3-manual-with-summary", "你设的：")
		h.click("改日期")
		h.click("去掉")
		shot(h, "A4-no-date-with-summary", "确认档期和最终报价单")
	})

	t.Run("B", func(t *testing.T) {
		h, acc, _ := aiHarness(t, false, noAuto)
		h.resize(1440, 900)
		h.useBackendClock()
		h.put(acc, "INBOX", coretest.Msg("财务部 <finance@corp.example>", acc.Email, "11 月差旅报销补充凭证", "请今天之内补交凭证。", coretest.Date(now.Add(-2*time.Hour))))
		h.put(acc, "INBOX", coretest.Msg("陈默 <chen@corp.example>", acc.Email, "技术方案评审纪要", "评审纪要见下，请在周五前给意见。", ago(12)))
		h.putSeen(acc, "Sent", coretest.Msg("Me <alice@example.com>", "Acme Cloud <sales@acme.example>", "Re: 年度合同续签", "续签合同已附上，请确认。", coretest.Date(now.Add(-97*time.Hour))))
		h.put(acc, "INBOX", coretest.Msg("周宁 <zhou@corp.example>", acc.Email, "下周例会时间", "下周例会改到周三下午可以吗？", ago(2)))
		h.syncNow(acc)
		toHandle(h, 4)
		for _, c := range []struct {
			subject string
			day     time.Time
			quote   string
		}{{"11 月差旅报销补充凭证", day(0), "请今天之内补交凭证。"}, {"技术方案评审纪要", day(-10), "请在周五前给意见"}} {
			tid := threadOf(h, c.subject)
			program(h, tid, h.messages(tid)[0].ID, c.day, c.quote)
		}
		follow := threadOf(h, "Re: 年度合同续签")
		h.app.setReplyDate(follow, core.Deadline{Date: day(3).Format(time.DateOnly)})
		h.until("the user's date", func() bool { d := h.replyDateIn(follow); return d != nil && d.Source == core.DeadlineManual })

		h.click("11 月差旅报销补充凭证")
		shot(h, "B1-other-app-no-summary", "请今天之内补交凭证。")
		h.click("技术方案评审纪要")
		shot(h, "B2-overdue-more-than-7-days", "，已过去 10 天")
		h.click("Re: 年度合同续签")
		shot(h, "B3-follow-up-manual", "你设的："+weekdayDay(day(3))+"前回复")
		h.click("下周例会时间")
		shot(h, "B4-no-date-no-summary", "直接发给你，尚未回复，已等 2 天")
		h.click("已发送") // outside any view
		h.waitMail("the sent row", func() bool { return !h.app.mail.list.loading && len(h.items()) == 1 })
		h.click("Re: 年度合同续签")
		shot(h, "B5-follow-up-outside-view", i18n.T("view.follow_up"))
	})

	t.Run("S", func(t *testing.T) {
		old := core.SummaryOnArrival
		core.SummaryOnArrival = true
		t.Cleanup(func() { core.SummaryOnArrival = old })
		h, _ := withAccount(t)
		h.resize(1440, 900)
		h.env.LLM(t).Release()
		h.waitMail("the model in the settings", func() bool { return h.app.settings.DefaultModel != "" })
		p := h.openSettings("features")
		p.st.SummaryMode = core.SummaryArrive
		h.drain()
		h.tt.Frame()
		shot(h, "S1-settings-ai-features-arrive", "更早的邮件")
	})
}

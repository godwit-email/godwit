package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func (h *harness) rules() []core.Rule {
	h.t.Helper()
	rs, err := h.env.Svc.Rules(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return rs
}

// A rule made in the editor labels the matching mail that arrives next; its
// switch turns it off.
func TestSettingsRulesCreate(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings("rules")
	h.see("还没有规则")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("保存")
	h.see("收到新邮件 · 发件人包含 billing@ → 打标签「验证码」")
	rs := h.rules()
	if len(rs) != 1 || !rs[0].Enabled || rs[0].Doc.Conditions[0].Value != "billing@" || rs[0].Doc.Actions[0].Label != "otp" {
		t.Fatalf("rules %+v", rs)
	}

	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.example", acc.Email, "Your bill", "x"))
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Hello", "x"))
	otp := h.labelID("otp")
	labeled := func(subject string) bool {
		page, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if m.Subject == subject {
				ls, err := h.env.Svc.MessageLabels(context.Background(), []int64{m.ID})
				return err == nil && len(ls[m.ID]) == 1 && ls[m.ID][0].LabelID == otp
			}
		}
		return false
	}
	h.until("the bill labeled", func() bool { return labeled("Your bill") })
	if labeled("Hello") {
		t.Error("the other mail was labeled")
	}

	h.click("启用「未命名规则」")
	h.until("the rule off", func() bool { return !h.rules()[0].Enabled })
}

// The editor offers a condition on the mail's age in whole days.
func TestSettingsRulesAgeCondition(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("字段")
	h.click("邮件时间")
	h.see("天数")
	h.click("保存")
	h.see("早于 30 天前")
	rs := h.rules()
	if len(rs) != 1 || rs[0].Doc.Conditions[0] != (core.RuleCondition{Field: core.FieldAge, Op: core.OpOlderThan, Value: "30"}) {
		t.Fatalf("rules %+v", rs)
	}
}

// A rule set to 后台运行 is tagged in the list and, while enabled, keeps
// Godwit running when the window closes with 后台常驻 off.
func TestSettingsRulesBackground(t *testing.T) {
	h, _ := notifyHarness(t, func(st *core.Settings) { st.KeepInBackground = false })
	if h.app.KeepInBackground() {
		t.Fatal("后台常驻 on")
	}
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("保存")
	h.see("收到新邮件 · 发件人包含 billing@")
	if h.tt.HasText("后台运行") || h.app.KeepInBackground() {
		t.Fatal("a rule not set to 后台运行 is tagged or keeps Godwit running")
	}

	h.click("编辑「未命名规则」")
	h.click("后台运行")
	h.click("保存")
	h.until("the rule in the background", func() bool { rs := h.rules(); return len(rs) == 1 && rs[0].Background })
	h.see("后台运行")
	if !h.app.KeepInBackground() {
		t.Error("closing the window would quit")
	}
	h.click("启用「未命名规则」")
	h.until("the rule off", func() bool { return !h.app.KeepInBackground() })
}

// An action added in the editor runs after the first; the rule's history
// shows the hit with each result, and undo puts the message back.
func TestSettingsRulesHistoryUndo(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("添加动作")
	h.see("整理")
	h.click("标为已读")
	h.click("保存")
	h.see("收到新邮件 · 发件人包含 billing@ → 打标签「验证码」 → 标为已读")

	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.example", acc.Email, "Your bill", "x"))
	id := h.rules()[0].ID
	h.until("the hit", func() bool {
		hs, err := h.env.Svc.RuleHits(context.Background(), id)
		return err == nil && len(hs) == 1 && hs[0].State == core.RuleRunDone
	})
	h.waitMail("the hit counted", func() bool { return h.tt.HasText("近 7 天 1 封") })
	h.click("展开最近命中")
	h.see("Your bill")
	h.see("标为已读")
	h.click("撤销")
	h.see("已撤销")
	hs, err := h.env.Svc.RuleHits(context.Background(), id)
	if err != nil || hs[0].Actions[1].State != core.RuleActionUndone {
		t.Fatalf("hits %+v %v", hs, err)
	}
}

// Saving a forward asks to confirm its recipient: going back saves nothing,
// confirming saves the rule with the recipient confirmed, tagged 外发.
func TestSettingsRulesForwardConfirm(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("动作 1")
	h.click("转发给")
	h.click("收件人邮箱")
	h.typeText("carol@example.com")
	h.click("保存")
	h.see("确认转发收件人")
	h.see("首次发送")
	h.click("返回修改")
	if rs := h.rules(); len(rs) != 0 {
		t.Fatalf("saved without confirming: %+v", rs)
	}

	h.click("保存")
	h.click("确认收件人")
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	if r := h.rules()[0]; len(r.Confirmed) != 1 || r.Confirmed[0] != "carol@example.com" || r.Doc.Actions[0].To != "carol@example.com" {
		t.Fatalf("rule %+v", r)
	}
	h.see("外发")
	h.see("转发给 carol@example.com")
}

// The editor lists the existing mail its conditions match once typing
// pauses; saving can run the rule once over it.
func TestSettingsRulesPreviewRunsOnExisting(t *testing.T) {
	h, acc := withAccount(t)
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.example", acc.Email, "Your bill", "x"))
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Hello", "x"))
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.example", acc.Email, "Old bill", "x", coretest.Date(time.Now().AddDate(0, 0, -90))))
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.advance(debounce)
	h.see("有 2 封符合")
	h.see("下面只列出最近 30 天的邮件")
	h.see("Your bill")
	if h.tt.HasText("Hello") || h.tt.HasText("Old bill") {
		t.Error("other mail, or mail older than 30 days, is listed")
	}
	h.click("保存")
	h.see("保存「未命名规则」")
	h.see("只执行：打标签「验证码」。")
	h.click("同时处理现有的 2 封邮件")
	h.click("保存规则")
	h.until("both bills labeled by the one-time run", func() bool {
		rs := h.rules()
		if len(rs) != 1 {
			return false
		}
		hs, err := h.env.Svc.RuleHits(context.Background(), rs[0].ID)
		return err == nil && len(hs) == 2 && hs[0].State == core.RuleRunDone && hs[1].State == core.RuleRunDone
	})
}

// No rule exists at first; a template opens the editor filled in and saves
// nothing until the user does.
func TestSettingsRulesTemplate(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.see("还没有规则")
	h.click("使用模板「可疑邮件移到垃圾邮件」")
	h.see("安全检查判定为")
	h.see("移到垃圾邮件")
	if len(h.rules()) != 0 {
		t.Fatal("a template saved a rule")
	}
	h.click("取消")
	h.see("还没有规则")
	if len(h.rules()) != 0 {
		t.Fatal("cancel saved a rule")
	}
}

// 为类似邮件创建规则… opens the editor with the sender filled in and offers
// the subject's word and the attachments as conditions.
func TestSettingsRulesFromMail(t *testing.T) {
	h, acc := withAccount(t)
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("AWS <billing@aws.example>", acc.Email, "Your AWS invoice is available", "x",
		coretest.Attachment("a.pdf", "application/pdf", []byte("%PDF"))))
	h.waitMail("the mail listed", func() bool { return h.tt.HasText("Your AWS invoice is available") })
	if err := h.tt.RightClick("Your AWS invoice is available"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("为类似邮件创建规则…"); err != nil {
		t.Fatal(err)
	}
	h.see("按「Your AWS invoice is available」填好了发件人。还想按这些匹配吗？")
	h.click("+ 主题包含 invoice")
	h.click("+ 有附件")
	h.click("保存")
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	d := h.rules()[0].Doc
	want := []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "billing@aws.example"},
		{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}, {Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}}
	if d.Name != "来自 AWS 的邮件" || !slices.Equal(d.Conditions, want) {
		t.Fatalf("rule %+v", d)
	}
}

// Wit's 在编辑器里修改 opens the editor filled in with its proposal; it is
// saved only from there, its forward recipient confirmed as any other's.
func TestSettingsRulesFromWit(t *testing.T) {
	h, _ := withAccount(t)
	h.app.EditRule(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "Stripe 发票转给会计", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "stripe.com"}},
		Actions:    []core.RuleAction{{Kind: core.ActionForward, To: "acct@company.example"}}}})
	h.see("已按 Wit 的提议填好。检查后保存，规则才会生效。")
	if rs := h.rules(); len(rs) != 0 {
		t.Fatalf("saved before the user did: %+v", rs)
	}
	h.click("保存")
	h.see("确认转发收件人")
	h.click("确认收件人")
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	if r := h.rules()[0]; r.Doc.Conditions[0].Value != "stripe.com" || len(r.Confirmed) != 1 {
		t.Fatalf("rule %+v", r)
	}
}

// Wit's actions are offered only once 「智能」's 规则可以使用 Wit is on.
func TestSettingsRulesWitActionsNeedTheSwitch(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.see("Wit 的动作会把邮件发给模型，默认关闭。在 智能 › 功能 里打开「规则可以使用 Wit」后才能选。")
	h.click("添加动作")
	h.see("需在「智能」里打开")
}

package app

import (
	"context"
	"slices"
	"testing"

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

// The editor lists the existing mail its conditions match once typing
// pauses; saving can run the rule once over it.
func TestSettingsRulesPreviewRunsOnExisting(t *testing.T) {
	h, acc := withAccount(t)
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.example", acc.Email, "Your bill", "x"))
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Hello", "x"))
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.advance(debounce)
	h.see("有 1 封符合")
	h.see("Your bill")
	if h.tt.HasText("Hello") {
		t.Error("the other mail is listed")
	}
	h.click("保存")
	h.see("保存「未命名规则」")
	h.see("只执行：打标签「验证码」。")
	h.click("同时处理现有的 1 封邮件")
	h.click("保存规则")
	h.until("the bill labeled by the one-time run", func() bool {
		rs := h.rules()
		if len(rs) != 1 {
			return false
		}
		hs, err := h.env.Svc.RuleHits(context.Background(), rs[0].ID)
		return err == nil && len(hs) == 1 && hs[0].Subject == "Your bill" && hs[0].State == core.RuleRunDone
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

package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

// saveRule saves the rule in the editor through the save sheet.
func (h *harness) saveRule() {
	h.t.Helper()
	h.click("保存")
	h.click("保存并启用")
}

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
	h.saveRule()
	h.see("已保存「未命名规则」")
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

func TestSettingsRulesBlockAction(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("spammer@")
	h.click("打标签")
	h.click(i18n.T("settings.rules.action.block"))
	h.saveRule()
	h.see("已保存「未命名规则」")
	rs := h.rules()
	if len(rs) != 1 || rs[0].Doc.Actions[0].Kind != core.ActionBlock {
		t.Fatalf("expected rule action to be block, got %+v", rs)
	}
}

// The editor offers a condition on the mail's age in whole days.
func TestSettingsRulesAgeCondition(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("字段")
	h.click("邮件时间")
	h.see("天数")
	h.saveRule()
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
	h.saveRule()
	h.see("收到新邮件 · 发件人包含 billing@")
	if h.tt.HasText("后台运行") || h.app.KeepInBackground() {
		t.Fatal("a rule not set to 后台运行 is tagged or keeps Godwit running")
	}

	h.click("展开最近命中")
	h.click("编辑规则")
	h.click("后台运行")
	h.saveRule()
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
	h.saveRule()
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
	h.see("第一次发往这个地址")
	h.click("返回修改")
	if rs := h.rules(); len(rs) != 0 {
		t.Fatalf("saved without confirming: %+v", rs)
	}

	h.click("保存")
	h.click("确认收件人")
	h.click("保存并启用")
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
	h.see("最近 30 天")
	h.see("Your bill")
	if h.tt.HasText("Hello") || h.tt.HasText("Old bill") {
		t.Error("other mail, or mail older than 30 days, is listed")
	}
	h.click("保存")
	h.see("保存「未命名规则」")
	h.see("只执行：打标签「验证码」。")
	h.click("同时处理现有的 2 封邮件")
	h.click("保存并启用")
	h.see("已保存「未命名规则」，正在处理现有的 2 封邮件")
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
	h.click("＋ 主题含 invoice")
	h.click("＋ 有附件")
	h.saveRule()
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	d := h.rules()[0].Doc
	want := []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "billing@aws.example"},
		{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}, {Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}}
	if d.Name != "来自 AWS 的邮件" || !slices.Equal(d.Conditions, want) {
		t.Fatalf("rule %+v", d)
	}
}

// Wit's 在编辑器里修改 opens the editor filled in with its proposal, quoting
// what was asked; it is saved only from there, its forward recipient
// confirmed as any other's.
func TestSettingsRulesFromWit(t *testing.T) {
	h, _ := withAccount(t)
	h.app.EditRule(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "Stripe 发票转给会计", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "stripe.com"}},
		Actions:    []core.RuleAction{{Kind: core.ActionForward, To: "acct@company.example"}}}}, 7, 0, "Stripe 的发票转给会计")
	h.see("Wit 按你说的填好了这条规则，检查后点「保存」才会生效。")
	h.see("“Stripe 的发票转给会计”")
	h.see("回到对话")
	if rs := h.rules(); len(rs) != 0 {
		t.Fatalf("saved before the user did: %+v", rs)
	}
	h.click("保存")
	h.see("确认转发收件人")
	h.click("确认收件人")
	h.click("保存并启用")
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	if r := h.rules()[0]; r.Doc.Conditions[0].Value != "stripe.com" || len(r.Confirmed) != 1 {
		t.Fatalf("rule %+v", r)
	}
}

// EditRule with no settings page to open does nothing rather than panic.
func TestEditRuleWithoutSettingsPage(t *testing.T) {
	h, _ := withAccount(t)
	settings := h.app.pages[PageSettings]
	delete(h.app.pages, PageSettings)
	t.Cleanup(func() { h.app.pages[PageSettings] = settings })
	h.app.EditRule(core.Rule{}, 0, 0, "")
}

// Wit's actions are listed but not chosen until 「智能」's 规则可以使用 Wit
// is on; the note on why shows while the add-action menu is open.
func TestSettingsRulesWitActionsNeedTheSwitch(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	note := "Wit 的动作会把邮件发给模型，默认关闭。在 智能 › 功能 里打开「规则可以使用 Wit」后才能选。"
	if h.tt.HasText(note) {
		t.Error("the note shows with the menu closed")
	}
	h.click("添加动作")
	h.see(note)
	h.see("需在「智能」里打开")
}

// 删除规则 asks first, then deletes the rule and its hits.
func TestSettingsRulesDelete(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.saveRule()
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	h.click("展开最近命中")
	h.click("删除规则")
	h.see("删除「未命名规则」？")
	h.click("取消")
	if len(h.rules()) != 1 {
		t.Fatal("cancel deleted the rule")
	}
	h.click("删除规则")
	h.click("删除")
	h.until("the rule deleted", func() bool { return len(h.rules()) == 0 })
	h.see("已删除「未命名规则」")
	h.see("还没有规则")
}

// The trigger can be kept to one account; the rule stores its id, and the
// list says which.
func TestSettingsRulesAccountScope(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.see("所有账户")
	h.click("值")
	h.typeText("billing@")
	h.click("收信账户")
	h.see(accountName(acc)) // the option; the sidebar has the name too, so the keys choose it
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.saveRule()
	h.until("the rule saved", func() bool { return len(h.rules()) == 1 })
	if d := h.rules()[0].Doc; d.Account != acc.ID {
		t.Fatalf("rule %+v", d)
	}
	h.see("在 " + accountName(acc) + " 收到新邮件 · 发件人包含 billing@")
}

// The templates are the design's six.
func TestSettingsRulesTemplates(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	for _, name := range []string{"账单归档", "订阅邮件不进收件箱", "可疑邮件移到垃圾邮件", "发票转给会计", "合同附件存到本机", "客户来信先起草回复"} {
		h.see(name)
	}
	h.click("使用模板「账单归档」")
	h.see("导出 PDF 到")
	h.click("取消")
	// Without 「智能」's switch the reply template leaves out Wit's action and says why.
	h.click("使用模板「客户来信先起草回复」")
	h.see("Wit 的动作会把邮件发给模型，默认关闭。在 智能 › 功能 里打开「规则可以使用 Wit」后才能选。")
}

// Dragging a rule's row moves it where the list shows, as the labels' do.
// The order is the one the rules run in.
func TestSettingsRulesReorder(t *testing.T) {
	h, _ := withAccount(t)
	for _, name := range []string{"A", "B", "C"} {
		doc := core.RuleDoc{Version: core.RuleVersion, Name: name, Trigger: core.TriggerReceive, Match: core.MatchAll,
			Actions: []core.RuleAction{{Kind: core.ActionStar}}}
		if _, err := h.env.Svc.SaveRule(context.Background(), core.Rule{Doc: doc, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	h.openSettings("rules")
	h.see("C")
	names := func() string {
		var s string
		for _, r := range h.rules() {
			s += r.Doc.Name
		}
		return s
	}
	first, _ := h.tt.Find("启用「A」")
	second, _ := h.tt.Find("启用「B」")
	row := second.Y - first.Y
	// drag moves the row at from by rows rows, as the labels' test does.
	drag := func(from int, rows float32) {
		h.t.Helper()
		x, y := first.X+first.W+row*2, first.Y+first.H/2+row*float32(from) // on the row, right of its switch
		h.tt.Move(x, y)
		h.tt.Press(x, y)
		h.tt.Move(x, y+row*rows/2)
		h.tt.Move(x, y+row*rows)
		h.tt.Release(x, y+row*rows)
		h.drain()
	}
	drag(0, 2)
	h.until("A moved after C", func() bool { return names() == "BCA" })
	drag(2, -2.4) // past the top row's middle
	h.until("A moved before B", func() bool { return names() == "ABC" })
}

// Opening a rule's hits grows the list, and closing them takes it back to
// as tall as it was, in the next frame.
func TestSettingsRulesListHeight(t *testing.T) {
	h, _ := withAccount(t)
	for _, name := range []string{"A", "B", "C"} {
		doc := core.RuleDoc{Version: core.RuleVersion, Name: name, Trigger: core.TriggerReceive, Match: core.MatchAll,
			Actions: []core.RuleAction{{Kind: core.ActionStar}}}
		if _, err := h.env.Svc.SaveRule(context.Background(), core.Rule{Doc: doc, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	h.openSettings("rules")
	h.see("C")
	h.tt.Frame()
	foot := func() float32 { r, _ := h.tt.Find(i18n.T("settings.rules.list.foot")); return r.Y }
	closed := foot()
	h.click("展开最近命中")
	h.see("删除规则")
	h.tt.Frame()
	if open := foot(); open <= closed {
		t.Fatalf("the list did not grow: %v, closed %v", open, closed)
	}
	h.click("收起")
	h.tt.Frame()
	h.tt.Frame()
	if got := foot(); got != closed {
		t.Fatalf("the list is %v from its closed place", got-closed)
	}
}

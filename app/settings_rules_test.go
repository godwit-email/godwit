package app

import (
	"context"
	"github.com/godwit-email/godwit/internal/theme"
	"reflect"
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
	if len(rs) != 1 || rs[0].Doc.Conditions[0].Field != core.FieldAge || rs[0].Doc.Conditions[0].Value != "30" {
		t.Fatalf("rules %+v", rs)
	}
}

// A rule's conditions go into groups: the button on a bracket's line switches
// its and or or, a group removed down to one leaves plain conditions, and 添加
// makes a group of the conditions so far.
func TestSettingsRulesConditionGroups(t *testing.T) {
	h, _ := withAccount(t)
	stripe := core.RuleCondition{Field: core.FieldFrom, Op: core.OpContains, Value: "stripe.com"}
	aws := core.RuleCondition{Field: core.FieldFrom, Op: core.OpContains, Value: "aws.example"}
	attached := core.RuleCondition{Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}
	h.app.EditRule(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "云服务", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Match: core.MatchAny, Group: []core.RuleCondition{stripe, aws}}, {Match: core.MatchAll, Group: []core.RuleCondition{attached}}},
		Actions:    []core.RuleAction{{Kind: core.ActionStar}}}}, 0, 0, "")
	h.openSettings("rules")
	h.click("现在是「且」，点击切换")
	h.saveRule()
	h.see("） 或 ") // the list says its groups, and the and it was switched to or
	d := h.rules()[0].Doc
	if d.Match != core.MatchAny || d.Conditions[0].Match != core.MatchAny || len(d.Rows()) != 3 {
		t.Fatalf("saved %+v", d)
	}

	h.click("展开最近命中")
	h.click("编辑规则")
	h.click("删除这组") // the first: the last group's conditions are plain again
	h.click("保存")
	h.click("保存并启用")
	h.see("收到新邮件 · 有附件 → 加星标")
	d = h.rules()[0].Doc
	if len(d.Conditions) != 1 || d.Conditions[0].Group != nil || d.Conditions[0].Field != core.FieldAttachments || d.Match != core.MatchAll {
		t.Fatalf("after removing a group %+v", d)
	}

	h.click("编辑规则")
	h.click(i18n.T("settings.rules.cond_group.add"))
	h.see("删除这组") // the condition so far is one group, the two new rows another
	h.click("取消")
	if d := h.rules()[0].Doc; len(d.Conditions) != 1 {
		t.Fatalf("saved without saving %+v", d)
	}
}

// Adding a group adds two conditions: with room for one, it does nothing, so
// the editor never builds a rule validRule refuses.
func TestSettingsRulesConditionGroupNeedsRoom(t *testing.T) {
	h, _ := withAccount(t)
	row := core.RuleCondition{Field: core.FieldSubject, Op: core.OpContains, Value: "a"}
	conds := slices.Repeat([]core.RuleCondition{row}, core.MaxRuleConditions-1)
	h.app.EditRule(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "满", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: conds, Actions: []core.RuleAction{{Kind: core.ActionStar}}}}, 0, 0, "")
	h.openSettings("rules")
	h.click(i18n.T("settings.rules.cond_group.add"))
	if h.tt.HasText("删除这组") {
		t.Fatal("a group added past the limit")
	}

	grouped := []core.RuleCondition{{Match: core.MatchAny, Group: conds[:len(conds)-1]}, {Match: core.MatchAll, Group: conds[:1]}}
	h.app.pages[PageSettings].(*settingsPage).rules.openEditor(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "满", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: grouped, Actions: []core.RuleAction{{Kind: core.ActionStar}}}})
	h.tt.Frame()
	h.click(i18n.T("settings.rules.cond_group.add"))
	if d := h.app.pages[PageSettings].(*settingsPage).rules.edit.Doc; len(d.Conditions) != 2 {
		t.Fatalf("a group added past the limit: %d groups", len(d.Conditions))
	}
}

// A rule set to 后台运行 is tagged in the list and, while enabled, keeps
// Godwit receiving when the window closes with 后台收信 at 关窗后停止.
func TestSettingsRulesBackground(t *testing.T) {
	h, _ := notifyHarness(t, func(st *core.Settings) { st.BackgroundReceive = core.BackgroundReceiveStopOnClose })
	if h.app.KeepInBackground() {
		t.Fatal("后台收信 keeps receiving")
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
	h.see(i18n.T("settings.rules.hits.note")) // what the hits allow, beside 删除规则 and 编辑规则
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

// The confirmation says which account a forward goes out from: for a rule
// on all accounts, the one that received the mail; for one account, it.
func TestSettingsRulesForwardConfirmSender(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(map[bool]string{false: "all", true: "scoped"}[scoped], func(t *testing.T) {
			h, acc := withAccount(t)
			h.openSettings("rules")
			h.click("新建规则")
			h.click("值")
			h.typeText("billing@")
			if scoped {
				h.click("所有账户")
				h.click(accountName(acc))
			}
			h.click("动作 1")
			h.click("转发给")
			h.click("收件人邮箱")
			h.typeText("carol@example.com")
			h.click("保存")
			h.see("确认转发收件人")
			fromReceiver, fromAccount := "从收到邮件的账户发出", "从 "+acc.Email+" 发出"
			h.see(map[bool]string{false: fromReceiver, true: fromAccount}[scoped])
			if h.tt.HasText(map[bool]string{false: fromAccount, true: fromReceiver}[scoped]) {
				t.Errorf("scoped=%v: the confirmation names the other scope's sender", scoped)
			}
		})
	}
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
	if r, ok := h.tt.Find("Your bill"); !ok || r.W <= 0 || r.H <= 0 {
		t.Fatalf("preview message row not visible: ok=%v bounds=%+v", ok, r)
	}
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
	if d.Name != "来自 AWS 的邮件" || !reflect.DeepEqual(d.Conditions, want) {
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
	h.tt.Advance(2 * theme.MotionBase)
	h.tt.Frame()
	if px := h.tt.Image().RGBAAt(640, 800); px.R > 235 { // the scrim over the white panel: modal even while it draws nothing
		t.Fatalf("the dialog is not drawn: %v", px)
	}
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
	h.click("所有账户")
	h.click(accountName(acc)) // the option is an item of the system menu, which takes no keys here
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

// Every language's inter-group connector pill and group match toggle fit their
// labels with room for the chevron and flip match mode on click.
func TestSettingsRulesLogicButtonFitsAllLocales(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	for _, lang := range i18n.Catalog.Languages() {
		i18n.SetLanguage(lang)
		for _, m := range []string{core.MatchAll, core.MatchAny} {
			outer, inner := m, m
			var wordW, matchW float32
			tt := ui.NewTester(func(c *ui.Context) {
				word := i18n.T("settings.rules.logic." + outer)
				wordW, _ = c.MeasureText(0, ui.Span{Text: word})
				match := i18n.T("settings.rules.match." + inner)
				matchW, _ = c.MeasureText(0, ui.Span{Text: match})
				ui.Column(c).FillWidth().Gap(8).Children(func() {
					condGroupConnector(c, &outer)
					condMatchButton(c, &inner)
				})
			}, 400, 120)
			word := i18n.T("settings.rules.logic." + m)
			r, ok := tt.Find(i18n.Tf("settings.rules.logic.flip", word))
			if !ok {
				t.Fatalf("%s/%s: connector %q not found", lang, m, word)
			}
			if r.W < wordW+16 {
				t.Errorf("%s/%s (%q): connector=%+v wordW=%v", lang, m, word, r, wordW)
			}
			match := i18n.T("settings.rules.match." + m)
			mr, ok := tt.Find(i18n.Tf("settings.rules.logic.flip", match))
			if !ok {
				t.Fatalf("%s/%s: match button %q not found", lang, m, match)
			}
			if mr.W < matchW+16 {
				t.Errorf("%s/%s (%q): matchBtn=%+v matchW=%v", lang, m, match, mr, matchW)
			}
			tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
			tt.ClickAt(mr.X+mr.W/2, mr.Y+mr.H/2)
			if outer == m || inner == m {
				t.Errorf("%s/%s: clicking did not flip outer=%q inner=%q", lang, m, outer, inner)
			}
		}
	}
}

// Dragging an action's grip handle reorders the actions inside the 就 card.
func TestSettingsRulesActionDragReorder(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("添加动作")
	h.click("归档")
	rv := h.app.pages[PageSettings].(*settingsPage).rules
	if len(rv.edit.Doc.Actions) != 2 || rv.edit.Doc.Actions[0].Kind != core.ActionLabel || rv.edit.Doc.Actions[1].Kind != core.ActionArchive {
		t.Fatalf("initial actions %+v", rv.edit.Doc.Actions)
	}
	a1, ok1 := h.tt.Find(i18n.Tf("settings.rules.action.n", 1))
	a2, ok2 := h.tt.Find(i18n.Tf("settings.rules.action.n", 2))
	if !ok1 || !ok2 {
		t.Fatalf("action selects not found: ok1=%v ok2=%v", ok1, ok2)
	}
	// Grip sits directly left of the action select (gap 8 + grip center 10).
	sx, sy := a1.X-18, a1.Y+a1.H/2
	dx, dy := a2.X-18, a2.Y+a2.H/2
	h.tt.Move(sx, sy)
	h.tt.Press(sx, sy)
	h.tt.Move(sx, (sy+dy)/2)
	h.tt.Move(dx, dy)
	h.tt.Release(dx, dy)
	h.tt.Frame()
	if rv.edit.Doc.Actions[0].Kind != core.ActionArchive || rv.edit.Doc.Actions[1].Kind != core.ActionLabel {
		t.Fatalf("after drag reorder: %+v", rv.edit.Doc.Actions)
	}
}

// Hovering a collapsed rule row offers a direct 编辑 button to open the editor
// without expanding its 7-day hit history first.
func TestSettingsRulesHoverEdit(t *testing.T) {
	h, _ := withAccount(t)
	doc := core.RuleDoc{Version: core.RuleVersion, Name: "账单归档", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Actions: []core.RuleAction{{Kind: core.ActionStar}}}
	if _, err := h.env.Svc.SaveRule(context.Background(), core.Rule{Doc: doc, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.openSettings("rules")
	h.see("账单归档")
	if _, shown := h.tt.AccessByRole(ui.RoleButton, i18n.T("settings.rules.edit")); shown {
		t.Fatal("hover edit shown before hovering")
	}
	r, ok := h.tt.Find("账单归档")
	if !ok {
		t.Fatal("rule row not found")
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	if _, shown := h.tt.AccessByRole(ui.RoleButton, i18n.T("settings.rules.edit")); !shown {
		t.Fatal("hover edit not shown on hover")
	}
	h.click(i18n.T("settings.rules.edit"))
	h.see("保存")
	if !h.app.pages[PageSettings].(*settingsPage).rules.editing {
		t.Fatal("clicking hover 编辑 did not open editor")
	}
}

// TestSettingsRulesHitActions: a done undoable action keeps its undo when a
// later action fails (beside retry) and when the message is gone since (a
// block, undone by sender), as UndoRuleRun allows.
func TestSettingsRulesHitActions(t *testing.T) {
	done := core.RuleActionResult{Kind: core.ActionStar, State: core.RuleActionDone}
	failed := core.RuleActionResult{Kind: core.ActionJunk, State: core.RuleActionFailed, Failure: core.RuleFailNoFolder}
	block := core.RuleActionResult{Kind: core.ActionBlock, State: core.RuleActionDone}
	for _, tc := range []struct {
		name        string
		hit         core.RuleHit
		retry, undo bool
	}{
		{"failed after a done action", core.RuleHit{MessageID: 1, State: core.RuleRunFailed, Actions: []core.RuleActionResult{done, failed}}, true, true},
		{"failed with nothing done", core.RuleHit{MessageID: 1, State: core.RuleRunFailed, Actions: []core.RuleActionResult{failed}}, true, false},
		{"block, message gone", core.RuleHit{State: core.RuleRunDone, Actions: []core.RuleActionResult{block}}, false, true},
		{"undone", core.RuleHit{MessageID: 1, State: core.RuleRunUndone, Actions: []core.RuleActionResult{done}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &rulesView{}
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				v.hit(c, core.Rule{}, tc.hit, time.Now())
			}, 600, 100)
			tt.Frame()
			if _, ok := tt.Find(i18n.T("common.retry")); ok != tc.retry {
				t.Errorf("retry shown = %v, want %v", ok, tc.retry)
			}
			if _, ok := tt.Find(i18n.T("settings.rules.hits.undo")); ok != tc.undo {
				t.Errorf("undo shown = %v, want %v", ok, tc.undo)
			}
		})
	}
}

// TestSettingsRulesActionMoveButtons: 上移/下移 reorder actions without the
// pointer, as keyboard and screen-reader users must.
func TestSettingsRulesActionMoveButtons(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("添加动作")
	h.click("归档")
	rv := h.app.pages[PageSettings].(*settingsPage).rules
	if a := rv.edit.Doc.Actions; len(a) != 2 || a[0].Kind != core.ActionLabel {
		t.Fatalf("initial actions %+v", a)
	}
	h.click(i18n.T("settings.rules.action.down")) // the first's: the last's is disabled
	h.tt.Frame()
	if a := rv.edit.Doc.Actions; a[0].Kind != core.ActionArchive || a[1].Kind != core.ActionLabel {
		t.Fatalf("after 下移: %+v", a)
	}
}

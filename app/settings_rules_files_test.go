package app

import (
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
)

// A file action writes to the folder picked, one subfolder per message by
// default; the path preview follows the naming, and 自定义… opens the
// token field, whose naming every file action to that folder shares.
func TestSettingsRulesFileActions(t *testing.T) {
	h, _ := withAccount(t)
	h.plat.dir = t.TempDir()
	h.openSettings("rules")
	h.click("新建规则")
	h.click("值")
	h.typeText("billing@")
	h.click("动作 1")
	h.click("保存附件到")
	h.click("选择文件夹…")
	h.see(h.plat.dir)
	h.see(filepath.Join(h.plat.dir, "2026-11-03 Invoice #1234 from Acme Inc", "invoice.pdf"))
	h.click("文件夹命名")
	h.click("年 / 月 / 日期 主题")
	h.see(filepath.Join(h.plat.dir, "2026", "11", "2026-11-03 Invoice #1234 from Acme Inc", "invoice.pdf"))
	h.click("文件夹命名")
	h.click("自定义…")
	h.click("发件人域名")
	h.see(filepath.Join(h.plat.dir, "2026", "11", "2026-11-03 Invoice #1234 from Acme Incstripe.com", "invoice.pdf"))
	h.click("添加动作")
	h.click("导出原始邮件到")
	h.click("保存")
	h.click("保存并启用")
	h.see("保存附件到「" + filepath.Base(h.plat.dir) + "」")

	rs := h.rules()
	want := core.RuleAction{Kind: core.ActionAttachments, Dir: h.plat.dir, Subfolder: true, Naming: "{year}/{month}/{date} {subject}{domain}"}
	if len(rs) != 1 || len(rs[0].Doc.Actions) != 2 || rs[0].Doc.Actions[0] != want {
		t.Fatalf("rules %+v", rs)
	}
	// The new action took the folder and naming of the one before it.
	want.Kind = core.ActionEML
	if rs[0].Doc.Actions[1] != want {
		t.Fatalf("second action %+v", rs[0].Doc.Actions[1])
	}
}

package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

// masking opens 隐私与安全 on its 隐私脱敏 tab, tall enough to show it whole.
func (h *harness) masking() {
	h.t.Helper()
	h.resize(1440, 3000)
	h.openSettings("privacy")
	h.see(i18n.T("settings.masking.foot"))
}

// A rule that can't be used is not saved, and the page says which line and
// why; fixed, it is saved with the words and the allowlist.
func TestMaskingRulesSaveOnlyWhenValid(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	h.typeUnder(i18n.T("settings.masking.rules"), "BAD (x")
	h.click(i18n.T("settings.masking.save"))
	h.see("第 1 行的正则无效")
	h.see("missing closing )")
	if len(h.app.settings.MaskRules) != 0 {
		t.Fatalf("an invalid rule was saved: %v", h.app.settings.MaskRules)
	}
	h.typeUnder(i18n.T("settings.masking.rules"), ")")
	h.typeUnder(i18n.T("settings.masking.words"), "Falcon")
	h.typeUnder(i18n.T("settings.masking.allow"), "GitHub")
	h.click(i18n.T("settings.masking.save"))
	h.until("the rules saved", func() bool {
		st := h.app.settings
		return len(st.MaskRules) == 1 && st.MaskRules[0] == core.MaskRule{Kind: "BAD", Regex: "(x)"} &&
			len(st.MaskWords) == 1 && st.MaskWords[0] == "Falcon" && len(st.MaskAllow) == 1 && st.MaskAllow[0] == "GitHub"
	})
}

// A rule under a built-in category's name is not saved, and the page says
// which line.
func TestMaskingRuleMayNotTakeABuiltInKind(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	h.typeUnder(i18n.T("settings.masking.rules"), "ORDER ORD-\\d+\nphone x\\d+")
	h.click(i18n.T("settings.masking.save"))
	h.see("第 2 行的类别名和内置类别重名")
	if len(h.app.settings.MaskRules) != 0 {
		t.Fatalf("a rule under a built-in kind was saved: %v", h.app.settings.MaskRules)
	}
}

// A rule stored under a built-in kind before stays: the rest still saves,
// and the preview masks with it rather than refusing.
func TestMaskingKeepsAnOldReservedRule(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	p := h.app.pages[PageSettings].(*settingsPage)
	h.post(func() { h.app.settingsOK.MaskRules = []core.MaskRule{{Kind: "PHONE", Regex: `ext-\d+`}} }) // as a build before stored it
	h.typeUnder(i18n.T("settings.masking.rules"), "PHONE ext-\\d+")
	h.typeUnder(i18n.T("settings.masking.allow"), "GitHub")
	h.click(i18n.T("settings.masking.save"))
	if h.tt.HasText("第 1 行的类别名和内置类别重名") || p.mask.err != "" {
		t.Fatalf("the stored rule stops the save: %q", p.mask.err)
	}
	if st, _ := p.mask.edited(p.st); p.maskCheck(st, []int{1}) != "" {
		t.Fatal("the preview refuses the stored rule")
	}
}

// The preview shows a pasted text masked, with the words being edited, and
// saves nothing.
func TestMaskingPreview(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	h.typeUnder(i18n.T("settings.masking.words"), "Falcon")
	r, _ := h.tt.Find(i18n.T("settings.masking.preview")) // the section's title; its card is the text area
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H+40)
	h.typeText("Falcon card 4111 1111 1111 1111")
	h.see("{{TERM_")
	h.see("{{BANK_CARD_")
	if st := h.app.settings; len(st.MaskWords) != 0 {
		t.Fatalf("the preview saved words: %v", st.MaskWords)
	}
}

// Names are off until turned on; each category's switch saves at once.
func TestMaskingCategorySwitches(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	if h.app.settings.MaskCategories.Names.On {
		t.Fatal("names are masked by default")
	}
	// the switch at the row's right, where the save button below ends
	label, _ := h.tt.Find(i18n.T("settings.masking.cat.names"))
	help, _ := h.tt.Find(i18n.T("settings.masking.cat.names.help"))
	save, _ := h.tt.Find(i18n.T("settings.masking.save"))
	h.tt.ClickAt(save.X+save.W-16, (label.Y+help.Y+help.H)/2)
	h.drain()
	h.tt.Frame()
	h.until("names on", func() bool { return h.app.settings.MaskCategories.Names.On })
	h.see(i18n.T("settings.masking.action.mask"))
}

// 识别模型 is off, and can't be turned on while its files are not here;
// once they are, its switch turns it on, and names with it.
func TestMaskModelSwitch(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	h.see(i18n.T("settings.masking.model.missing"))
	h.click(i18n.T("settings.masking.model"))
	h.drain()
	h.tt.Frame()
	if h.app.settings.MaskModel {
		t.Fatal("turned on without its files")
	}

	dir := filepath.Join(h.env.DataDir, "cache", "guard", guard.Revision) // as a download leaves them
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range guard.Files {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.openSettings("general")
	h.masking()
	h.see(i18n.T("settings.masking.model.help"))
	h.click(i18n.T("settings.masking.model"))
	h.until("on, with names", func() bool { st := h.app.settings; return st.MaskModel && st.MaskCategories.Names.On })
}

// A summary of mail a blocked category (拦截) holds is not asked: the
// reader says which category held it back.
func TestABlockedSummarySaysWhy(t *testing.T) {
	h, acc, llm := aiHarness(t, true, func(st *core.Settings) { noQuick(st); st.MaskCategories.IDs.Action = core.MaskBlock })
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Card", "my card is 4111 1111 1111 1111"))
	h.openThread(m.ThreadID)
	h.see("隐私脱敏拦截了这次请求，其中有证件与金融号码")
	if n := llm.Requests(); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

// 脱敏记录, a page of its own, lists the requests; one opens on what it
// held, by first character and length (a short value by its length only),
// and the tiles pick the requests by what was done.
func TestMaskingLogShowsWhatWasSent(t *testing.T) {
	h, acc, llm := aiHarness(t, true, func(st *core.Settings) { noQuick(st); st.MaskCategories.Emails.Action = core.MaskWarn })
	llm.Script(coretest.Turn{Text: []string{"ok"}})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Card", "card 4111 1111 1111 1111"))
	h.openThread(m.ThreadID)
	h.until("the summary done", func() bool { s := h.app.mail.reader.ai[m.ID]; return s != nil && s.summary != nil && s.summary.done })
	h.masking()
	h.see("最近 30 天：1 次请求，拦截 0 次，1 个值原样发出")
	h.click("查看脱敏记录")
	h.until("the log's page", func() bool { return h.app.route.Settings == "masklog" })
	h.see("有值原样发出")
	h.click(i18n.T("settings.masking.feature.skill"))
	h.see("4…，19 个字符")
	h.see("3 个字符") // bob: short, no first character
	if h.tt.HasText("4111") || h.tt.HasText("b…，3 个字符") {
		t.Fatal("the log shows too much of a value")
	}
	h.click("被拦截的请求")
	h.see("没有符合筛选条件的请求")
}

// A long log shows a page of requests at a time; the tiles count them all.
func TestMaskingLogPages(t *testing.T) {
	h, _ := withAccount(t)
	h.resize(1440, 20000)
	p := h.openSettings("masklog")
	now := h.app.clock.Now()
	n := maskLogPage + 10
	h.post(func() {
		p.maskLog = nil
		for i := range n {
			p.maskLog = append(p.maskLog, core.MaskRecord{At: now.Add(-time.Duration(i) * time.Minute), Request: int64(n - i), Feature: "wit", Kind: "EMAIL", Category: "emails", Len: 16, Action: core.MaskMask})
		}
	})
	last := now.Add(-time.Duration(n-1) * time.Minute).In(now.Location()).Format("15:04")
	h.see(strconv.Itoa(n))
	h.see(i18n.T("settings.masking.log.more"))
	if h.tt.HasText(last) {
		t.Fatal("the oldest request shows on the first page")
	}
	h.click(i18n.T("settings.masking.log.more"))
	h.see(last)
	if h.tt.HasText(i18n.T("settings.masking.log.more")) {
		t.Fatal("显示更多 stays with nothing more to show")
	}
}

func TestMaskingCopy(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	for _, s := range []string{"隐私脱敏", "godwit mcp", "第三方的人名要靠识别模型", "只支持中文和英文"} {
		h.see(s)
	}
}

package app

import (
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

// masking opens General, tall enough to show its 隐私脱敏 sections whole.
func (h *harness) masking() {
	h.t.Helper()
	h.resize(1440, 3000)
	h.openSettings("general")
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

func TestMaskingCopy(t *testing.T) {
	h, _ := withAccount(t)
	h.masking()
	for _, s := range []string{"隐私脱敏", "godwit mcp", "第三方的人名和地址"} {
		h.see(s)
	}
}

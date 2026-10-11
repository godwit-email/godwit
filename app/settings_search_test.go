package app

import (
	"strings"
	"testing"

	"github.com/godwit-email/godwit/internal/i18n"
)

// TestSettingsIndexCoversRows: every text a settings page shows that is a
// settings.* catalog message is in that page's search index.
func TestSettingsIndexCoversRows(t *testing.T) {
	h, acc := withAccount(t)
	h.resize(1440, 3000)
	byText := map[string][]string{}
	for _, k := range i18n.Keys() {
		if strings.HasPrefix(k, "settings.") && !strings.Contains(k, "%") {
			byText[i18n.T(k)] = append(byText[i18n.T(k)], k)
		}
	}
	h.openSettings("none") // no such page: only the sidebar and the shell show
	chrome := map[string]bool{}
	for _, txt := range h.tt.Texts() {
		chrome[txt] = true
	}
	for sec, route := range map[string]string{"account": accountRoute(acc.ID), "general": "general", "models": "models", "extensions": "extensions", "privacy": "privacy", "about": "about"} {
		h.openSettings(route)
		in := map[string]bool{}
		for _, k := range settingsIndex[sec] {
			in[k] = true
		}
		for _, txt := range h.tt.Texts() {
			ks := byText[txt]
			if len(ks) == 0 || chrome[txt] {
				continue
			}
			found := false
			for _, k := range ks {
				found = found || in[k]
			}
			if !found {
				t.Errorf("%s: %q (%v) is not in settingsIndex", sec, txt, ks)
			}
		}
	}
}

func TestSettingsSearchFilter(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings("general")
	all := p.matching("")
	if len(all.apps) != len(appSections) || !all.accounts {
		t.Fatalf("empty query hides sections: %+v", all)
	}
	if m := p.matching("语言"); len(m.apps) != 1 || m.apps[0] != "general" || m.accounts {
		t.Errorf("语言: %+v", m)
	}
	if m := p.matching("签名"); !m.accounts || len(m.apps) != 0 {
		t.Errorf("签名: %+v", m)
	}
	if m := p.matching(acc.Email[:3]); !m.accounts {
		t.Errorf("email prefix: %+v", m)
	}
	if m := p.matching("zzzz"); m.accounts || len(m.apps) != 0 {
		t.Errorf("zzzz: %+v", m)
	}
}

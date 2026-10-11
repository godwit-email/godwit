package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

// TestSettingsIndexCoversRows: every text a settings page shows that is a
// settings.* catalog message is in that page's search index.
func TestSettingsIndexCoversRows(t *testing.T) {
	h, acc := withAccount(t)
	h.resize(1440, 3000)
	byText := map[string][]string{}
	for _, k := range i18n.Keys() {
		if (strings.HasPrefix(k, "settings.") || strings.HasPrefix(k, "mail.folder.")) && !strings.Contains(k, "%") {
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
	for _, l := range accountOptions() { // the selects' options, which the page shows only when open
		if m := p.matching(l); !m.accounts {
			t.Errorf("option %q: %+v", l, m)
		}
	}
	for _, q := range []string{"已发送", "归档", "3 个月", "插入图片"} {
		if m := p.matching(q); !m.accounts {
			t.Errorf("%s: %+v", q, m)
		}
	}
	for q, sec := range map[string]string{"mcp": "extensions", "skill": "extensions", "判断": "models"} {
		if m := p.matching(q); !slices.Contains(m.apps, sec) {
			t.Errorf("%s: %+v, want %s", q, m, sec)
		}
	}
	if m := p.matching("zzzz"); m.accounts || len(m.apps) != 0 {
		t.Errorf("zzzz: %+v", m)
	}
}

// Enter on an email opens that account, and the sidebar lists only it.
func TestSettingsSearchEmail(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.AddAccount(t, "bob@example.org")
	})
	h.until("initial load", func() bool { return h.app.loaded && len(h.app.accounts) == 2 })
	h.openSettings("general")
	bob := h.app.accounts[slices.IndexFunc(h.app.accounts, func(a core.Account) bool { return a.Email == "bob@example.org" })]
	h.click("搜索设置")
	h.tt.Type("bob@")
	h.tt.Frame()
	if h.tt.HasText("alice@example.com") {
		t.Fatalf("the sidebar lists alice for bob@: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	if h.app.route.Settings != accountRoute(bob.ID) {
		t.Fatalf("Enter opened %q, want bob's %q", h.app.route.Settings, accountRoute(bob.ID))
	}
}

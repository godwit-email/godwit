package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestSettingsSearchFocusRingIsNotClipped(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(map[bool]string{false: "light", true: "dark"}[dark], func(t *testing.T) {
			p := &settingsPage{}
			tok := theme.Light()
			if dark {
				tok = theme.Dark()
			}
			tt := ui.NewTester(func(c *ui.Context) {
				c.DisableAnimations()
				theme.Apply(c, tok)
				ui.Column(c).Fill().Padding(10).Background(tok.Window).Children(func() {
					ui.Column(c).Grow(1).Label("viewport").Children(func() {
						sidebarScroll(c, func() { p.searchField(c) })
					})
					ui.Box(c).Size(10, 10).Label("ring reference").Background(c.Theme().Focus)
				})
			}, 240, 100)
			if err := tt.Click(i18n.T("settings.search")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			in, _ := tt.Find(i18n.T("settings.search"))
			viewport, _ := tt.Find("viewport")
			ref, _ := tt.Find("ring reference")
			img := tt.Image()
			want := img.RGBAAt(int(ref.X+ref.W/2), int(ref.Y+ref.H/2))
			for x := 0; x < int(theme.RingWidth); x++ {
				got := img.RGBAAt(int(viewport.X)+x, int(in.Y+in.H/2))
				if max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) > 3 {
					t.Errorf("left focus ring clipped at %d px: got %v, want %v", x, got, want)
				}
			}
		})
	}
}

// TestSettingsIndexCoversRows: every text a settings page shows that is a
// settings.* catalog message is in that page's search index.
func TestSettingsIndexCoversRows(t *testing.T) {
	h, acc := withAccount(t)
	h.resize(1440, 3000)
	byText := map[string][]string{}
	for _, k := range i18n.Keys() {
		if (strings.HasPrefix(k, "settings_") || strings.HasPrefix(k, "mail_folder_")) && !strings.Contains(k, "%") {
			byText[i18n.T(k)] = append(byText[i18n.T(k)], k)
		}
	}
	h.openSettings("none") // no such page: only the sidebar and the shell show
	chrome := map[string]bool{}
	for _, txt := range h.tt.Texts() {
		chrome[txt] = true
	}
	for sec, route := range map[string]string{"account": accountRoute(acc.ID), "general": "general", "models": "models", "extensions": "extensions", "privacy": "privacy", "about": "about", "labels": "labels", "signatures": "signatures"} {
		p := h.openSettings(route)
		tabs := map[string]*int{"models": &p.aiTab, "privacy": &p.privacyTab}[sec]
		in := map[string]bool{}
		for _, k := range settingsIndex[sec] {
			in[strings.ReplaceAll(k, ".", "_")] = true
		}
		for tab := range 4 { // every tab of a page that has them (the Intelligence page has 4)
			if tabs == nil && tab > 0 {
				break
			}
			if tabs != nil {
				*tabs = tab
				h.tt.Frame()
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
					t.Errorf("%s tab %d: %q (%v) is not in settingsIndex", sec, tab, txt, ks)
				}
			}
		}
	}
}

// A search opens the AI and 隐私与安全 pages on the tab it found.
func TestSettingsSearchOpensTab(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("general")
	p.query = "本机 API"
	h.openSettings("models")
	h.see("启用本机 API")
	p.query = "屏蔽"
	h.openSettings("privacy")
	h.see("没有已屏蔽的发件人。")
}

func TestSettingsSearchFilter(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings("general")
	all := p.matching("")
	if len(all.apps) != len(appSections) || !all.accounts {
		t.Fatalf("empty query hides sections: %+v", all)
	}
	if m := p.matching("语言"); !slices.Equal(m.apps, []string{"general", "models"}) || m.accounts { // 界面语言; 阅读语言 on the Intelligence page
		t.Errorf("语言: %+v", m)
	}
	if m := p.matching("签名"); !m.accounts || !slices.Equal(m.apps, []string{"signatures"}) {
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
	for _, q := range []string{"已发送", "归档", "3 个月", "默认签名"} {
		if m := p.matching(q); !m.accounts {
			t.Errorf("%s: %+v", q, m)
		}
	}
	for q, sec := range map[string]string{"mcp": "extensions", "skill": "extensions", "判断": "models", "插入图片": "signatures"} {
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

package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
)

// pageDescs is what each page's heading says under its title.
var pageDescs = map[string]string{
	"general": "settings.general.sub", "signatures": "settings.signatures.sub", "labels": "settings.labels.sub", "rules": "settings.rules.sub",
	"models": "settings.models.desc", "features": "settings.ai.sub", "skills": "settings.skills.sub", "mcp": "settings.mcp.sub", "plugins": "settings.plugins.sub", "clients": "settings.clients.sub",
	"masking": "settings.privacy.sub", "security": "settings.security.sub", "images": "settings.remote_images.sub", "blocked": "settings.blocked.sub", "labs": "settings.labs.sub",
}

// Every page's key, the old routes included, opens a page of the sidebar and
// shows its heading.
func TestSettingsRoutesLand(t *testing.T) {
	h, _ := withAccount(t)
	shows := func(sec string) {
		t.Helper()
		if sec == "about" {
			h.see(i18n.Tf("settings.about.version", Version))
		} else {
			h.see(i18n.T(pageDescs[sec]))
		}
	}
	if len(pageDescs)+1 != len(appSections) {
		t.Fatalf("%d pages, %d with a heading to check", len(appSections), len(pageDescs))
	}
	for _, sec := range appSections {
		p := h.openSettings(sec)
		if got, _ := p.section(); got != sec {
			t.Errorf("%s opened %q", sec, got)
		}
		if _, name := p.Title(); name != pageName(sec) {
			t.Errorf("%s: title %q, want %q", sec, name, pageName(sec))
		}
		shows(sec)
	}
	for old, first := range map[string]string{"models": "models", "extensions": "skills", "privacy": "masking"} {
		h.openSettings("about") // another page first: the old route is a new one
		p := h.openSettings(old)
		if got, _ := p.section(); got != first {
			t.Errorf("old route %q opened %q, want %q", old, got, first)
		}
		shows(first)
	}
}

// The pages are grouped and ordered as the design has them, one row each.
func TestSettingsSidebarGroups(t *testing.T) {
	h, _ := withAccount(t)
	h.resize(1440, 1600)
	h.openSettings("about")
	want := []string{"账户", "应用", "通用", "签名", "标签", "规则", "智能与扩展", "模型", "AI 功能", "Skill", "MCP", "插件", "接入 AI 应用", "隐私与安全", "隐私脱敏", "发件人安全", "远程图片", "已屏蔽的发件人", "实验室", "关于"}
	texts := h.tt.Texts()
	at := 0
	for _, w := range want {
		i := slices.Index(texts[at:], w)
		if i < 0 {
			t.Fatalf("%q is missing or out of order in %q", w, texts)
		}
		at += i + 1
	}
	if len(appSections) != len(want)-4 { // the accounts' and the three groups' titles are not pages
		t.Errorf("%d pages in settingsNav, %d in the sidebar", len(appSections), len(want)-4)
	}
}

// 添加账户 is a + in the accounts' title; it opens the add-account sheet.
func TestSettingsAddAccountButton(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	if h.app.addAccount != nil {
		t.Fatal("the add-account sheet is open before the click")
	}
	h.click(i18n.T("settings.add_account"))
	h.until("the sheet", func() bool { return h.app.addAccount != nil })
}

// Subpages name themselves and their page, for the title bar's breadcrumb; the
// pages of the sidebar have none.
func TestSettingsBreadcrumb(t *testing.T) {
	h, acc := withAccount(t)
	crumb := func(p *settingsPage, parent, current string) {
		t.Helper()
		h.tt.Frame()
		gotParent, gotCurrent, ok := p.Breadcrumb()
		if !ok || gotParent != parent || gotCurrent != current {
			t.Errorf("breadcrumb %q, %q, %v; want %q, %q", gotParent, gotCurrent, ok, parent, current)
		}
	}
	for _, sec := range []string{"general", "signatures", "rules", "plugins", "masking", accountRoute(acc.ID)} {
		if _, _, ok := h.openSettings(sec).Breadcrumb(); ok {
			t.Errorf("%s has a breadcrumb", sec)
		}
	}
	up := func(p *settingsPage, want string) {
		t.Helper()
		p.BreadcrumbUp()
		h.drain()
		h.tt.Frame()
		if got, _ := p.section(); got != want {
			t.Errorf("up opened %q, want %q", got, want)
		}
		if _, _, ok := p.Breadcrumb(); ok {
			t.Error("up stays in the subpage")
		}
	}
	p := h.openSettings("masklog")
	crumb(p, "隐私脱敏", "脱敏记录")
	up(p, "masking")

	sig := h.openSignature(acc.ID, "Work")
	crumb(p, "签名", sig.Name)
	up(p, "signatures")

	h.openSettings("rules")
	h.click(i18n.T("settings.rules.new"))
	h.until("the editor", func() bool { return p.rules.editing })
	crumb(p, "规则", "新建规则")
	up(p, "rules")
}

// The plugin directory is 插件's subpage.
func TestSettingsBreadcrumbPluginDirectory(t *testing.T) {
	h, _, _ := registryHarness(t)
	p := h.openSettings("plugins")
	h.click("浏览插件目录")
	h.seeExactly("插件目录")
	h.tt.Frame()
	if parent, current, ok := p.Breadcrumb(); !ok || parent != "插件" || current != "插件目录" {
		t.Errorf("breadcrumb %q, %q, %v", parent, current, ok)
	}
	p.BreadcrumbUp()
	h.tt.Frame()
	if _, _, ok := p.Breadcrumb(); ok || p.ext.onDirectory {
		t.Error("up stays in the plugin directory")
	}
	h.see("浏览插件目录")
}

// Typing a search narrows the sidebar to the pages that match, the pages
// that were tabs of the AI and 隐私与安全 pages among them, and Enter opens the
// first.
func TestSettingsSearchFindsFormerTabs(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("general")
	for q, page := range map[string]string{"本机 API": "clients", "屏蔽": "blocked", "远程图片": "images", "发件人安全": "security", "脱敏": "masking", "MCP": "mcp", "Skill": "skills", "沙箱": "plugins", "翻译方式": "features", "判断": "models"} {
		if m := p.matching(q); !slices.Contains(m.apps, page) {
			t.Errorf("%s: %+v, want %s", q, m, page)
		}
	}
	m := p.matching("本机 API")
	if m.accounts || len(m.apps) == 0 || m.apps[0] != "clients" {
		t.Fatalf("本机 API: %+v", m)
	}
	h.click("搜索设置")
	h.tt.Type("本机 API")
	h.tt.Frame()
	if h.tt.HasText("实验室") {
		t.Errorf("the sidebar lists a page that does not match: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	if h.app.route.Settings != "clients" {
		t.Fatalf("Enter opened %q, want clients", h.app.route.Settings)
	}
	h.see("允许外部应用和脚本接入")
}

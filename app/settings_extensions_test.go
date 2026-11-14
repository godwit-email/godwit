package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestMain(m *testing.M) {
	coretest.ServeMCPIfChild()
	coretest.ServeNativeIfChild()
	kit.BackdropBlur = false // slow on the Tester's CPU renderer; kit's own test covers it
	os.Exit(m.Run())
}

func (h *harness) plugin(name string) (core.PluginInfo, bool) {
	h.t.Helper()
	list, err := h.env.Svc.ListPlugins(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return core.PluginInfo{}, false
}

func (h *harness) pluginConfig(name string) map[string]core.ConfigValue {
	h.t.Helper()
	vals, err := h.env.Svc.GetPluginConfig(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]core.ConfigValue{}
	for _, v := range vals {
		out[v.Key] = v
	}
	return out
}

func TestSkillsChangedRefreshesSettings(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("extensions")
	e := h.app.pages[PageSettings].(*settingsPage).ext
	h.until("initial skills", func() bool { return len(e.skills) > 0 })
	count := len(e.skills)
	h.click(i18n.Tf("settings.ext.tab.skills", count))
	writeSkill(t, h.env, "meeting-notes")
	h.app.onEvent(events.Event{Type: events.SkillsChanged})
	h.advance(refreshEvery)
	h.see("Meeting Notes")
	h.see("Brief the email")
	h.see(i18n.Tf("settings.ext.tab.skills", count+1))
	if !viewUnchanged([]events.Event{{Type: events.SkillsChanged}}) {
		t.Fatal("a skill change must not invalidate mail views")
	}
}

// skillRefreshApp runs real reads and writes, with their UI callbacks under
// the test's control and no background event subscription.
func skillRefreshApp(t *testing.T) (*App, *extView, func() func()) {
	t.Helper()
	env := coretest.New(t, coretest.WithoutAutoSync())
	posted := make(chan func(), 32)
	a := New(Deps{Svc: env.Svc, Post: func(f func()) { posted <- f }, Clock: newFakeClock()})
	t.Cleanup(a.cancel)
	a.route = Route{Page: PageSettings, Settings: "extensions"}
	e := a.pages[PageSettings].(*settingsPage).ext
	var err error
	e.skills, err = env.Svc.ListSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return a, e, func() func() {
		t.Helper()
		select {
		case f := <-posted:
			return f
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the UI callback")
			return nil
		}
	}
}

func TestSkillsChangedOnlyRefreshesSkills(t *testing.T) {
	for name, page := range map[string]Page{"settings": PageSettings, "mail": PageMail} {
		t.Run(name, func(t *testing.T) {
			a, e, next := skillRefreshApp(t)
			a.route.Page, a.route.ThreadID = page, 1
			a.onEvent(events.Event{Type: events.SkillsChanged})
			a.clock.(*fakeClock).advance(refreshEvery)
			next()() // the coalesced refresh
			if a.loads.Gen() != 0 || a.mail.list.Gen() != 0 || e.list.Gen() != 0 || a.mail.reader.read.Gen() != 0 {
				t.Fatalf("skill event reloaded unrelated data: shell=%d mail=%d extensions=%d reader=%d", a.loads.Gen(), a.mail.list.Gen(), e.list.Gen(), a.mail.reader.read.Gen())
			}
		})
	}
}

func TestSkillWriteOnlyRefreshesSkills(t *testing.T) {
	a, e, next := skillRefreshApp(t)
	e.write("skills", func(ctx context.Context) error { return a.svc.SetSkillEnabled(ctx, "summarize", false) }, nil)
	for slices.ContainsFunc(e.skills, func(s core.SkillInfo) bool { return s.Name == "summarize" && s.Enabled }) {
		next()()
	}
	if e.list.Gen() != 0 {
		t.Fatal("a skill write reloaded plugins, registry and MCP servers")
	}
}

func TestSkillReadKeepsOptimisticToggle(t *testing.T) {
	a, e, next := skillRefreshApp(t)
	a.page().refresh([]events.Event{{Type: events.SkillsChanged}})
	stale := next() // read finished before the click; its result has not applied
	i := slices.IndexFunc(e.skills, func(s core.SkillInfo) bool { return s.Name == "summarize" })
	e.skills[i].Enabled = false
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	e.write("skills", func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}, nil)
	stale()
	if e.skills[i].Enabled {
		t.Fatal("a pre-click ListSkills result replaced the optimistic toggle")
	}
}

func TestSkillReadsShareVersion(t *testing.T) {
	for _, fullFirst := range []bool{true, false} {
		name := "full_then_skills"
		if !fullFirst {
			name = "skills_then_full"
		}
		t.Run(name, func(t *testing.T) {
			a, e, next := skillRefreshApp(t)
			read := func(full bool) []func() {
				if full {
					e.load()
					return []func(){next(), next()}
				}
				a.page().refresh([]events.Event{{Type: events.SkillsChanged}})
				return []func(){next()}
			}
			stale := read(fullFirst)
			if err := a.svc.SetSkillEnabled(context.Background(), "summarize", false); err != nil {
				t.Fatal(err)
			}
			for _, apply := range read(!fullFirst) {
				apply()
			}
			for _, apply := range stale {
				apply()
			}
			if slices.ContainsFunc(e.skills, func(s core.SkillInfo) bool { return s.Name == "summarize" && s.Enabled }) {
				t.Fatal("an older full or skill-only read replaced the new skill state")
			}
			if len(e.plugins) == 0 {
				t.Fatal("the skill-only read invalidated the unrelated plugin read")
			}
		})
	}
}

func TestSkillWritesWaitForAllToggles(t *testing.T) {
	a, e, next := skillRefreshApp(t)
	for i := range e.skills {
		if e.skills[i].Name == "summarize" || e.skills[i].Name == "translate" {
			e.skills[i].Enabled = false
		}
	}
	e.write("skills", func(ctx context.Context) error { return a.svc.SetSkillEnabled(ctx, "summarize", false) }, nil)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	e.write("skills", func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return a.svc.SetSkillEnabled(ctx, "translate", false)
	}, nil)
	next()() // first write completes; the second is still blocked
	if len(a.writes.chains["skills"]) != 1 {
		t.Fatal("the second toggle was not queued")
	}
	a.page().refresh([]events.Event{{Type: events.SkillsChanged}})
	e.load() // an unrelated plugin refresh while the second toggle waits
	next()()
	if len(e.plugins) == 0 {
		t.Fatal("the plugin refresh did not complete while skill writes were pending")
	}
	if slices.ContainsFunc(e.skills, func(s core.SkillInfo) bool {
		return (s.Name == "summarize" || s.Name == "translate") && s.Enabled
	}) {
		t.Fatal("a refresh replaced a toggle while another skill write was pending")
	}
	e.skills = nil // the final write must reconcile the list after the queue empties
	close(release)
	for len(e.skills) == 0 {
		next()()
	}
	if slices.ContainsFunc(e.skills, func(s core.SkillInfo) bool {
		return (s.Name == "summarize" || s.Name == "translate") && s.Enabled
	}) {
		t.Fatal("the settled list did not include both saved toggles")
	}
}

func TestSkillsChangedWithMailEventsRefreshesMail(t *testing.T) {
	a, _, _ := skillRefreshApp(t)
	a.route = Route{Page: PageMail, ThreadID: 1}
	a.refresh([]events.Event{{Type: events.SkillsChanged}, {Type: events.MessagesChanged}})
	if a.loads.Gen() != 1 || a.mail.list.Gen() != 1 || a.mail.reader.read.Gen() != 1 {
		t.Fatalf("mixed events skipped mail refresh: shell=%d mail=%d reader=%d", a.loads.Gen(), a.mail.list.Gen(), a.mail.reader.read.Gen())
	}
}

func TestSkillToggle(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("extensions")
	h.see("插件 · 1") // plugins come first
	skills, err := h.env.Svc.ListSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.click(i18n.Tf("settings.ext.tab.skills", len(skills))) // the page may not have loaded them yet
	h.see("Summarize")
	h.see("内置")
	h.click("启用 Summarize")
	h.until("summarize off", func() bool {
		list, err := h.env.Svc.ListSkills(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.Name == "summarize" {
				return !s.Enabled
			}
		}
		return false
	})
}

func TestPluginInstallGrantConfigure(t *testing.T) {
	h, _ := withAccount(t)
	h.plat.dir = coretest.PluginFixture(t, "config-demo")
	p := h.openSettings("extensions")
	h.click("选择文件夹")
	h.called("OpenDirectory")
	h.until("installed", func() bool { _, ok := h.plugin("config-demo"); return ok })
	h.see("待授权")
	h.see("套餐")
	pending := p.ext.cfgs["config-demo"]
	h.click("允许并启用")
	// The grant has landed in the interface, not only in the Service: it
	// reads the form again, which would close a menu opened before it.
	h.until("enabled", func() bool {
		info, _ := h.plugin("config-demo")
		return info.Enabled && !info.NeedsGrant && p.ext.cfgs["config-demo"] != pending && h.tt.HasText("卸载插件")
	})

	// endpoint shows only on the Pro plan.
	h.see("套餐")
	if h.tt.HasText("自定义地址") {
		t.Fatal("endpoint shows on the free plan")
	}
	h.click("Free")
	h.click("Pro")
	h.see("自定义地址")
	h.fill("自定义地址", "https://pro.example")

	// A short secret fails under its field; nothing is saved.
	h.fill("API Key", "short")
	h.click("保存")
	h.until("the field error", func() bool { f := p.ext.cfgs["config-demo"]; return f != nil && f.vals["apiKey"].err != "" })
	if h.pluginConfig("config-demo")["endpoint"].Set {
		t.Fatal("saved despite the error")
	}

	h.fill("API Key", "supersecret1")
	h.click("保存")
	h.until("saved", func() bool {
		c := h.pluginConfig("config-demo")
		return c["endpoint"].Set && c["apiKey"].Set
	})
	c := h.pluginConfig("config-demo")
	var ep, plan string
	_ = json.Unmarshal(c["endpoint"].Value, &ep)
	_ = json.Unmarshal(c["plan"].Value, &plan)
	if ep != "https://pro.example" || plan != "pro" {
		t.Fatalf("config %+v", c)
	}
	h.until("the secret field clears", func() bool {
		f := p.ext.cfgs["config-demo"]
		return f != nil && f.vals["apiKey"].s == "" && f.vals["apiKey"].set
	})
	if h.tt.HasText("supersecret1") {
		t.Fatal("the secret shows")
	}

	h.click("Pro")
	h.click("Free")
	h.until("endpoint hides", func() bool { return !h.tt.HasText("自定义地址") })
}

func TestPluginFolderInstallLoading(t *testing.T) {
	h, _ := withAccount(t)
	h.plat.dir = coretest.PluginFixture(t, "config-demo")
	h.openSettings("extensions")
	release := h.block("plugins")
	h.click("选择文件夹")
	h.see("正在安装…")
	release()
	h.until("installed without progress", func() bool {
		_, ok := h.plugin("config-demo")
		return ok && !h.tt.HasText("正在安装…")
	})
}

func TestPluginDetailNoSend(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.InstallPlugin(t, "config-demo")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("extensions")
	h.click("插件 · 2")
	h.click("config-demo")
	h.see("任何插件都不能发送邮件。")
	h.see("不请求任何权限")
	h.click("卸载插件")
	h.see("卸载 config-demo？")
	h.click("卸载")
	h.until("uninstalled", func() bool { _, ok := h.plugin("config-demo"); return !ok })
}

func TestMCPAddAndTrust(t *testing.T) {
	h, _ := withAccount(t)
	cmd, _, env := coretest.MCPCommand(t)
	p := h.aiTab(aiMCP)
	h.click("添加服务器")
	h.fill("名称", "lookup-server")
	h.fill("命令", cmd)
	for k, v := range env {
		h.fill("环境变量", k+"="+v)
	}
	h.click("保存")
	h.see("lookup-server")
	h.see("需要授权")
	h.click("授权并启用")
	h.see("lookup")
	h.see("声明只读（仅供参考）")
	h.click("信任为只读工具")
	h.until("trusted", func() bool {
		list, err := h.env.Svc.ListMCPServers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return len(list) == 1 && len(list[0].Tools) == 1 && list[0].Tools[0].Trusted
	})

	// Editing shows the keys, never the values.
	h.click("编辑")
	h.until("the edit form", func() bool { return p.ext.mform != nil })
	for k, v := range env {
		if p.ext.mform.env != k+"=" || h.tt.HasText(v) {
			t.Fatalf("env field %q", p.ext.mform.env)
		}
	}
}

func TestPluginConfigEditDuringSave(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.InstallPlugin(t, "config-demo")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	p := h.openSettings("extensions")
	h.click("插件 · 2")
	h.click("config-demo") // an installed plugin opens on a click
	h.click("Free")
	h.click("Pro")
	h.fill("自定义地址", "a.example")
	release := h.block("plugins")
	h.click("保存")
	h.fill("自定义地址", " b.example ")
	release()
	stored := func() string {
		var s string
		_ = json.Unmarshal(h.pluginConfig("config-demo")["endpoint"].Value, &s)
		return s
	}
	h.until("saved", func() bool { return stored() == "a.example" })
	h.until("the later edit stays", func() bool {
		f := p.ext.cfgs["config-demo"]
		return f != nil && !f.busy && f.vals["endpoint"].s == " b.example " && f.vals["endpoint"].orig == "a.example"
	})
	h.click("保存")
	h.until("saved verbatim", func() bool { return stored() == " b.example " })
}

func TestValuesVerbatim(t *testing.T) {
	fields := []plugins.ConfigField{{Key: "t", Type: "text"}, {Key: "s", Type: "secret"}, {Key: "n", Type: "number"}}
	f := &pluginForm{vals: map[string]*cfgVal{"t": {s: " a "}, "s": {s: " secret12 "}, "n": {s: " 3 "}}}
	got := f.values(fields)
	if string(got["t"]) != `" a "` || string(got["s"]) != `" secret12 "` || string(got["n"]) != "3" {
		t.Fatalf("values %q", got)
	}
	if kv := keyValues(" K = v \nX=y"); kv["K"] != " v " || kv["X"] != "y" {
		t.Fatalf("key values %q", kv)
	}
}

func TestMCPEditDuringSave(t *testing.T) {
	h, _ := withAccount(t)
	cmd, _, _ := coretest.MCPCommand(t)
	p := h.aiTab(aiMCP)
	h.click("添加服务器")
	h.fill("名称", "one")
	h.fill("命令", cmd)
	release := h.block("mcp")
	h.click("保存")
	h.fill("名称", "two")
	release()
	h.until("added", func() bool {
		list, err := h.env.Svc.ListMCPServers(context.Background())
		return err == nil && len(list) == 1 && list[0].Name == "one"
	})
	h.until("the form stays with the edit", func() bool {
		f := p.ext.mform
		return f != nil && !f.busy && f.id != 0 && f.name == "two"
	})
}

// pendingHarness has config-demo installed but not approved.
func pendingHarness(t *testing.T) *harness {
	h, _ := withAccount(t)
	if _, err := h.env.Svc.InstallPlugin(context.Background(), coretest.PluginFixture(t, "config-demo")); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPluginRowSwitch(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.InstallPlugin(t, "config-demo")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	p := h.openSettings("extensions")
	h.see("config-demo") // the plugins tab shows first, each plugin one closed row
	if h.tt.HasText("它请求的权限") || h.tt.HasText("卸载插件") {
		t.Fatal("an installed plugin opens by default")
	}
	h.click("config-demo")
	h.see("卸载插件")
	h.click("config-demo")
	h.until("closed", func() bool { return !h.tt.HasText("卸载插件") })
	h.click("启用 config-demo")
	// The next click must use the refreshed switch, not just the persisted value.
	h.until("off in service and view", func() bool {
		stored, _ := h.plugin("config-demo")
		return !stored.Enabled && len(h.app.writes.chains["plugins"]) == 0 &&
			slices.ContainsFunc(p.ext.plugins, func(p core.PluginInfo) bool {
				return p.Name == "config-demo" && !p.Enabled
			})
	})
	h.click("启用 config-demo")
	h.until("on", func() bool { p, _ := h.plugin("config-demo"); return p.Enabled })
}

func TestPendingPluginExcludedLine(t *testing.T) {
	got := pluginExcluded(plugins.Permissions{Mail: []string{"read"}})
	if got != "修改邮件、访问网络、调用大模型、发送邮件（任何插件都不能发送）" {
		t.Fatalf("excluded %q", got)
	}
	got = pluginExcluded(plugins.Permissions{Mail: []string{"read", "write"}, LLM: true})
	if got != "访问网络、发送邮件（任何插件都不能发送）" {
		t.Fatalf("excluded %q", got)
	}
	h := pendingHarness(t)
	h.openSettings("extensions")
	h.click("插件 · 2")
	h.see("待授权")
	h.see("它请求的权限")
	h.see("不包含")
	if h.tt.HasText("保存") || h.tt.HasText("卸载插件") {
		t.Fatal("a pending plugin shows save or uninstall")
	}
}

// 允许并启用 saves what the pending plugin's form holds, then enables it.
func TestPendingAllowSavesConfig(t *testing.T) {
	h := pendingHarness(t)
	h.openSettings("extensions")
	h.fill("API Key", "supersecret1")
	h.click("允许并启用")
	h.until("enabled with its key", func() bool {
		p, _ := h.plugin("config-demo")
		return p.Enabled && !p.NeedsGrant && h.pluginConfig("config-demo")["apiKey"].Set
	})
}

// 允许并启用 is one chained write: the form is busy meanwhile; a config error
// shows on its field only (no toast) and clears on the retry.
func TestPendingAllowBusyAndFieldError(t *testing.T) {
	h := pendingHarness(t)
	p := h.openSettings("extensions")
	h.until("the form", func() bool { return p.ext.cfgs["config-demo"] != nil })
	f := p.ext.cfgs["config-demo"]
	f.vals["plan"].s = "gold"
	release := h.block("plugins")
	h.click("允许并启用")
	if !f.busy {
		t.Fatal("not busy while it writes")
	}
	release()
	h.until("the error on its field", func() bool { return !f.busy && f.vals["plan"].err != "" })
	msg := f.vals["plan"].err
	for range 3 {
		h.tt.Frame()
		h.drain()
	}
	if n := len(slices.DeleteFunc(h.tt.Texts(), func(s string) bool { return s != msg })); n != 1 {
		t.Fatalf("the error shows %d times, want once (no toast): %q", n, msg)
	}
	f.vals["plan"].s = "pro"
	h.click("允许并启用")
	if f.vals["plan"].err != "" {
		t.Fatal("the old error stays during the retry")
	}
	h.until("enabled", func() bool { p, _ := h.plugin("config-demo"); return p.Enabled })
}

// A plugin row shows the manifest's description under the name, on one
// line however long (the whole text in its tooltip).
func TestPluginRowDescription(t *testing.T) {
	h, _ := withAccount(t)
	long := strings.Repeat("演示插件", 80)
	shotInstall(t, h, "desc-demo", `{"name":"desc-demo","version":"0.1.0","description":{"default":"Demo","zh-CN":"`+long+`"},"main":"index.ts"}`,
		`export default function (_ctx: any) {}`, true)
	h.openSettings("extensions")
	h.see("desc-demo")
	h.see("演示插件")
	desc, ok := h.tt.Find(long)
	sm := theme.Light().Text.Sm
	if line := sm * theme.Light().Text.Line(sm); !ok || desc.H > line*1.5 {
		t.Fatalf("the description is %v high, a line %v: not one line", desc.H, line)
	}
}

func TestPendingNotNowCollapses(t *testing.T) {
	h := pendingHarness(t)
	h.openSettings("extensions")
	h.click("插件 · 2")
	h.see("允许并启用")
	h.click("暂不启用")
	h.until("collapsed", func() bool { return !h.tt.HasText("允许并启用") })
	if p, _ := h.plugin("config-demo"); !p.NeedsGrant || p.Enabled {
		t.Fatalf("core changed: %+v", p)
	}
	h.see("待授权")
}

// Two pending plugins each keep their own form, read once, across frames.
func TestPendingPluginsKeepOwnForms(t *testing.T) {
	h, _ := withAccount(t)
	src := coretest.PluginFixture(t, "config-demo")
	if _, err := h.env.Svc.InstallPlugin(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	src2 := coretest.PluginFixture(t, "config-demo")
	m, err := os.ReadFile(src2 + "/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	m = bytes.Replace(m, []byte(`"config-demo"`), []byte(`"config-two"`), 1)
	if err := os.WriteFile(src2+"/plugin.json", m, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.InstallPlugin(context.Background(), src2); err != nil {
		t.Fatal(err)
	}
	p := h.openSettings("extensions")
	h.click("插件 · 3")
	h.until("both forms", func() bool { return p.ext.cfgs["config-demo"] != nil && p.ext.cfgs["config-two"] != nil })
	a, b := p.ext.cfgs["config-demo"], p.ext.cfgs["config-two"]
	a.vals["apiKey"].s, b.vals["apiKey"].s = "aaa", "bbb"
	for range 5 {
		h.tt.Frame()
		h.drain()
	}
	if p.ext.cfgs["config-demo"] != a || p.ext.cfgs["config-two"] != b || a.vals["apiKey"].s != "aaa" || b.vals["apiKey"].s != "bbb" {
		t.Fatal("a form was re-read or lost its values")
	}
	if len(p.ext.cfgBusy) != 0 {
		t.Fatal("a read is still in flight")
	}
}

// A native plugin awaiting consent shows its risk, not permissions it cannot be held to.
func TestNativePluginRiskAndGrant(t *testing.T) {
	h, _ := withAccount(t)
	h.plat.dir = coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil)
	h.openSettings("extensions")
	h.click("选择文件夹")
	h.called("OpenDirectory")
	h.until("installed", func() bool { _, ok := h.plugin("dlx"); return ok })
	h.until("the 原生 pill", func() bool { _, ok := h.tt.Find("原生"); return ok }) // exactly: the page's subtitle mentions 原生插件
	h.see("待授权")
	h.see("以 Godwit 的全部权限运行")
	h.see("这个文件不在 Godwit 审核过的插件目录里")
	h.see("SHA-256")
	if h.tt.HasText("不包含") || h.tt.HasText("它请求的权限") || h.tt.HasText("允许并启用") {
		t.Fatal("a native plugin shows script permissions")
	}
	h.click("仍然启用")
	h.until("enabled", func() bool { info, _ := h.plugin("dlx"); return info.Enabled && !info.NeedsGrant })
	h.see("你已授权这个文件")
}

func TestScriptPluginCardUnchanged(t *testing.T) {
	h := pendingHarness(t)
	h.openSettings("extensions")
	h.see("它请求的权限")
	h.see("不包含")
	h.see("允许并启用")
	if _, pill := h.tt.Find("原生"); pill || h.tt.HasText("仍然启用") {
		t.Fatal("a script plugin shows the native card")
	}
}

// loginFixture is a native plugin with the login capability, enabled and listed.
func loginFixture(t *testing.T, h *harness, extra map[string]any) *settingsPage {
	t.Helper()
	h.plat.dir = coretest.NativePluginFixture(t, "signin", "0.1.0", map[string]any{"name": "signin", "version": "0.1.0", "abi": 1,
		"capabilities": []string{"login"}}, extra)
	path := filepath.Join(h.plat.dir, "plugin.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["capabilities"] = []string{"login"}
	b, _ = json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	p := h.openSettings("extensions")
	h.click("选择文件夹")
	h.until("installed", func() bool { _, ok := h.plugin("signin"); return ok })
	h.click("仍然启用")
	// In the view, not just stored. The install leaves the row open.
	h.until("enabled", func() bool {
		return slices.ContainsFunc(p.ext.plugins, func(p core.PluginInfo) bool { return p.Name == "signin" && p.Enabled && !p.NeedsGrant })
	})
	return p
}

func TestPluginSignIn(t *testing.T) {
	h, _ := withAccount(t)
	p := loginFixture(t, h, map[string]any{
		"login": map[string]any{
			"login.status": map[string]any{"signedIn": false, "notice": map[string]any{"default": "Risk", "zh-Hans": "这可能违反条款"}},
			"login.start":  map[string]any{"flow": "f1"},
		},
		"loginPoll": []any{
			map[string]any{"state": "open", "url": "https://accounts.example.com/auth"},
			map[string]any{"state": "open", "url": "https://accounts.example.com/auth"},
			map[string]any{"state": "done", "account": "a@example.com"},
		},
	})
	h.until("the sign-in button", func() bool { return h.tt.HasText("登录") })
	h.click("登录")
	h.see("这可能违反条款") // nothing starts before the notice is read
	if slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, "OpenURL") }) {
		t.Fatal("the page opened before the user agreed")
	}
	h.click("仍然登录")
	h.until("the page's host", func() bool { return h.tt.HasText("accounts.example.com") })
	if slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, "OpenURL") }) {
		t.Fatal("the page opened before the user saw its host")
	}
	h.click("打开")
	h.called("OpenURL https://accounts.example.com/auth")
	// The plugin is asked again only as the clock moves, so its "done" (the
	// third answer) cannot come before the click, however slow this test runs.
	h.until("the flow's end", func() bool {
		h.clock.advance(time.Second)
		return p.ext.loginFlow == ""
	})
	n := 0
	for _, c := range h.plat.Calls() {
		if c == "OpenURL https://accounts.example.com/auth" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the page opened %d times", n)
	}
}

func TestPluginSignedInShowsAccountAndSignsOut(t *testing.T) {
	h, _ := withAccount(t)
	loginFixture(t, h, map[string]any{"login": map[string]any{"login.status": map[string]any{"signedIn": true, "account": "a@example.com"}}})
	h.until("the account", func() bool { return h.tt.HasText("已登录 a@example.com") })
	if _, button := h.tt.Find("登录"); button {
		t.Fatal("no sign-in while signed in")
	}
	h.click("退出登录")
	h.until("the write", func() bool { return len(h.app.writes.chains["plugins"]) == 0 })
}

func TestSameConsentCoversTheKind(t *testing.T) {
	shown := core.RegistryEntry{Version: "1.0.0", Script: true}
	if !sameConsent(shown, core.PluginInfo{Version: "1.0.0"}) {
		t.Fatal("the release the dialog showed")
	}
	if sameConsent(shown, core.PluginInfo{Version: "1.0.0", Native: true}) {
		t.Fatal("a native release of the same version must ask again: the user approved a script's permissions")
	}
}

func TestPluginSignInPageRefused(t *testing.T) {
	h, _ := withAccount(t)
	p := loginFixture(t, h, map[string]any{
		"login":     map[string]any{"login.status": map[string]any{"signedIn": false}, "login.start": map[string]any{"flow": "f1"}},
		"loginPoll": []any{map[string]any{"state": "open", "url": "https://elsewhere.example.net/auth"}},
	})
	h.until("the sign-in button", func() bool { return h.tt.HasText("登录") })
	h.click("登录")
	h.click("仍然登录")
	h.until("the page's host", func() bool { return h.tt.HasText("elsewhere.example.net") })
	h.click("取消")
	h.until("the flow's end", func() bool { return p.ext.loginFlow == "" })
	if slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, "OpenURL") }) {
		t.Fatal("a refused page opened")
	}
}

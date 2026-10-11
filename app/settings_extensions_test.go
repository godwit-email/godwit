package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestMain(m *testing.M) {
	coretest.ServeMCPIfChild()
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

func TestSkillToggle(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("extensions")
	h.see("插件 · 0") // plugins come first
	h.click(i18n.Tf("settings.ext.tab.skills", len(p.ext.skills)))
	h.see("summarize")
	h.see("内置")
	h.click("启用 summarize")
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
	h.click("从文件夹安装")
	h.called("OpenDirectory")
	h.until("installed", func() bool { _, ok := h.plugin("config-demo"); return ok })
	h.see("待授权")
	h.click("允许并启用")
	h.until("enabled", func() bool { info, _ := h.plugin("config-demo"); return info.Enabled && !info.NeedsGrant })

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

func TestPluginDetailNoSend(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.InstallPlugin(t, "config-demo")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("extensions")
	h.click("插件 · 1")
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
	p := h.openSettings("extensions")
	h.click("MCP · 0")
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
	h.click("插件 · 1")
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
	p := h.openSettings("extensions")
	h.click("MCP · 0")
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
	h.openSettings("extensions")
	h.see("config-demo") // the plugins tab shows first, each plugin one closed row
	if h.tt.HasText("它请求的权限") || h.tt.HasText("卸载插件") {
		t.Fatal("an installed plugin opens by default")
	}
	h.click("config-demo")
	h.see("卸载插件")
	h.click("config-demo")
	h.until("closed", func() bool { return !h.tt.HasText("卸载插件") })
	h.click("启用 config-demo")
	h.until("off", func() bool { p, _ := h.plugin("config-demo"); return !p.Enabled })
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
	h.click("插件 · 1")
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
	h.click("插件 · 1")
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
	h.click("插件 · 2")
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

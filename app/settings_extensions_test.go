package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/plugins"
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
	h.openSettings("extensions")
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
	h.click("安装插件…")
	h.called("OpenDirectory")
	h.until("installed", func() bool { _, ok := h.plugin("config-demo"); return ok })
	h.see("需要授权")
	h.click("授权并启用")
	h.until("enabled", func() bool { info, _ := h.plugin("config-demo"); return info.Enabled && !info.NeedsGrant })
	h.see("已启用")

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
	h.until("the field error", func() bool { f := p.ext.cfg; return f != nil && f.vals["apiKey"].err != "" })
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
	h.until("the secret field clears", func() bool { f := p.ext.cfg; return f != nil && f.vals["apiKey"].s == "" && f.vals["apiKey"].set })
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
	h.see("任何插件都不能发送邮件")
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
		f := p.ext.cfg
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

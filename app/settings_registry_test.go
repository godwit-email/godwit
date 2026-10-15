package app

import (
	"context"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/coretest"
)

var regT0 = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

func regPlugin(name string, releases ...map[string]any) map[string]any {
	return map[string]any{"name": name, "title": name, "summary": "Summary of " + name, "releases": releases}
}

// registryHarness: an account, and a registry rs published with dlx (native) and hello (script).
func registryHarness(t *testing.T) (*harness, *coretest.RegistryServer, map[string]any) {
	t.Helper()
	rs := coretest.NewRegistryServer(t)
	dlx := rs.NativeRelease(t, "dlx", "0.1.0", nil)
	rs.Publish(t, coretest.Registry(regT0, regPlugin("dlx", dlx),
		regPlugin("hello", rs.ScriptRelease("hello", "0.1.0", `{"net":["api.example.com"]}`, `export default function () {}`))))
	h := newHarnessWith(t, []coretest.Option{coretest.WithRegistry(rs.Src)}, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	return h, rs, dlx
}

func (h *harness) seeExactly(text string) {
	h.t.Helper()
	h.until("an element "+text, func() bool { _, ok := h.tt.Find(text); return ok })
}

func TestRegistrySectionInstallNative(t *testing.T) {
	h, _, _ := registryHarness(t)
	h.openSettings("extensions")
	h.seeExactly("插件目录")
	h.see("Summary of dlx")
	h.seeExactly("脚本")
	h.click("安装 dlx")
	h.see("安装原生插件 dlx 0.1.0")
	h.see("这个文件经过 Godwit 插件目录审核")
	h.click("继续安装")
	h.until("installed and enabled", func() bool { p, ok := h.plugin("dlx"); return ok && p.Enabled && p.Trust == "listed" })
	h.see("已审核：Godwit 插件目录。")
}

func TestRegistrySectionInstallScript(t *testing.T) {
	h, _, _ := registryHarness(t)
	h.openSettings("extensions")
	h.click("安装 hello")
	h.until("installed", func() bool { p, ok := h.plugin("hello"); return ok && p.Trust == "listed" })
	if h.tt.HasText("安装原生插件") {
		t.Fatal("a script plugin asked for the native confirmation")
	}
	h.see("待授权")
	h.see("它请求的权限")
	h.see("已审核：Godwit 插件目录。")
}

func TestRegistrySectionUpdate(t *testing.T) {
	h, rs, dlx := registryHarness(t)
	ctx := context.Background()
	if err := h.env.Svc.RefreshPluginRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.InstallPluginFromRegistry(ctx, "dlx"); err != nil {
		t.Fatal(err)
	}
	rs.Publish(t, coretest.Registry(regT0.Add(time.Hour), regPlugin("dlx", dlx, rs.NativeRelease(t, "dlx", "0.2.0", nil))))
	h.openSettings("extensions")
	h.click("更新到 0.2.0 dlx")
	h.until("updated", func() bool { p, _ := h.plugin("dlx"); return p.Version == "0.2.0" })
	if h.tt.HasText("安装原生插件") {
		t.Fatal("an update asked for confirmation")
	}
}

func TestRegistryRevokedNotice(t *testing.T) {
	h, rs, _ := registryHarness(t)
	ctx := context.Background()
	if err := h.env.Svc.RefreshPluginRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.InstallPluginFromRegistry(ctx, "dlx"); err != nil {
		t.Fatal(err)
	}
	rs.Publish(t, coretest.Registry(regT0.Add(time.Hour)))
	h.openSettings("extensions")
	h.until("revoked", func() bool { p, _ := h.plugin("dlx"); return p.DisabledReason == "trust_revoked" }) // it needs a grant again: its row opens
	h.see("Godwit 插件目录已撤销对这个文件的信任。")
}

func TestRegistrySectionHiddenWhenOff(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("extensions")
	h.see("插件 · 0")
	if _, ok := h.tt.Find("插件目录"); ok {
		t.Fatal("the registry section shows without a registry")
	}
}

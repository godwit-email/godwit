package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/plugins"
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
	h.click("浏览插件目录")
	h.seeExactly("插件目录")
	h.see("Summary of dlx")
	h.see("脚本 · 0.1.0")
	h.click("安装 dlx")
	h.see("以 Godwit 的全部权限运行")
	h.see("插件目录，已审核")
	h.click("安装并启用")
	h.until("installed and enabled", func() bool { p, ok := h.plugin("dlx"); return ok && p.Enabled && p.Trust == "listed" })
	h.see("已审核：Godwit 插件目录。")
	if h.tt.HasText("也可能让 Godwit 崩溃") {
		t.Fatal("a reviewed plugin shows the unlisted-source warning")
	}
}

func TestRegistryNativeChangedFilesShowRisk(t *testing.T) {
	h, _, _ := registryHarness(t)
	if err := h.env.Svc.RefreshPluginRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.InstallPluginFromRegistry(context.Background(), "dlx"); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(h.env.DataDir, "plugins", "dlx", "*", plugins.LibraryName()))
	if err != nil || len(files) != 1 {
		t.Fatalf("installed library: %v, %v", files, err)
	}
	if err := os.WriteFile(files[0], []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := h.openSettings("extensions")
	h.until("integrity error", func() bool {
		info, ok := h.plugin("dlx")
		return ok && info.Trust == "listed" && info.Error != ""
	})
	p.ext.plugin = "dlx"
	h.see("the installed files were changed or damaged")
	h.see("也可能让 Godwit 崩溃")
	if h.tt.HasText("已审核：Godwit 插件目录。") {
		t.Fatal("a changed native plugin is labeled as reviewed")
	}
}

func TestRegistryInstallLoading(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			h, rs, _ := registryHarness(t)
			h.openSettings("extensions")
			h.click("浏览插件目录")
			if fail {
				rs.Serve("/files/hello-0.1.0-any", nil)
			}
			release := h.block("plugins")
			h.click("安装 hello")
			h.click("允许并安装")
			if !h.tt.HasText("正在安装…") {
				t.Fatal("no visible progress while installation is pending")
			}
			// Repeated activation must not queue another installation.
			if err := h.tt.Click("正在安装… hello"); err != nil {
				t.Fatal(err)
			}
			h.tt.Frame()
			if got := len(h.app.writes.chains["plugins"]); got != 2 {
				t.Fatalf("queued %d writes, want the blocker and one install", got)
			}
			release()
			h.until("installation settles", func() bool { return len(h.app.writes.chains["plugins"]) == 0 && !h.tt.HasText("正在安装…") })
			if fail {
				h.seeExactly("安装 hello")
				if _, ok := h.plugin("hello"); ok {
					t.Fatal("a failed download installed the plugin")
				}
				rs.Serve("/files/hello-0.1.0-any", []byte(`export default function () {}`))
				h.click("安装 hello")
				h.click("允许并安装")
				h.until("installed on retry", func() bool { _, ok := h.plugin("hello"); return ok })
			} else {
				h.until("installed", func() bool { _, ok := h.plugin("hello"); return ok })
			}
		})
	}
}

func TestRegistryDownloadProgress(t *testing.T) {
	h, rs, _ := registryHarness(t)
	code := "export default function () {}\n"
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, code[:len(code)/2])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, code[len(code)/2:])
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	rel := rs.ScriptRelease("hello", "0.2.0", `{}`, code)
	rel["artifacts"].(map[string]any)["any"].(map[string]any)["url"] = server.URL
	rs.Publish(t, coretest.Registry(regT0.Add(time.Hour), regPlugin("hello", rel)))
	h.openSettings("extensions")
	h.click("浏览插件目录")
	h.see("脚本 · 0.2.0")
	h.click("安装 hello")
	h.click("允许并安装")
	h.see("正在下载… 50%")
	if _, ok := h.plugin("hello"); ok {
		t.Fatal("the plugin was installed before its download finished")
	}
	unblock()
	h.until("installed without progress", func() bool {
		_, ok := h.plugin("hello")
		return ok && !h.tt.HasText("正在下载…") && !h.tt.HasText("正在安装…")
	})
}

func TestRegistrySectionInstallScript(t *testing.T) {
	h, _, _ := registryHarness(t)
	h.openSettings("extensions")
	h.click("浏览插件目录")
	h.click("安装 hello")
	h.see("api.example.com") // the dialog names where it can send data before the user allows it
	h.see("在沙箱里运行，只能访问这些地址：")
	if _, ok := h.plugin("hello"); ok {
		t.Fatal("the dialog installed before the user confirmed")
	}
	h.click("允许并安装")
	h.until("installed, allowed and on", func() bool {
		p, ok := h.plugin("hello")
		return ok && p.Trust == "listed" && p.Enabled && !p.NeedsGrant
	})
	h.see("已审核：Godwit 插件目录。")
}

func TestRegistryDialogCancelInstallsNothing(t *testing.T) {
	h, _, _ := registryHarness(t)
	h.openSettings("extensions")
	h.click("浏览插件目录")
	h.click("安装 hello")
	h.click("取消")
	h.until("the dialog closes", func() bool { return !h.tt.HasText("允许并安装") })
	if _, ok := h.plugin("hello"); ok {
		t.Fatal("cancel installed the plugin")
	}
}

// A script that needs config stays pending: consent comes with its form.
func TestRegistryConfigurableScriptStaysPending(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	rel := rs.ScriptRelease("hello", "0.1.0", `{"net":["api.example.com"]}`, `export default function () {}`)
	rel["manifest"] = map[string]any{"name": "hello", "version": "0.1.0", "main": "index.js", "permissions": map[string]any{"net": []string{"api.example.com"}},
		"config": []any{map[string]any{"key": "token", "type": "text", "label": "Token"}}}
	rs.Publish(t, coretest.Registry(regT0, regPlugin("hello", rel)))
	h := newHarnessWith(t, []coretest.Option{coretest.WithRegistry(rs.Src)}, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("extensions")
	h.click("浏览插件目录")
	h.click("安装 hello")
	if h.tt.HasText("允许并安装") {
		t.Fatal("offers to allow a plugin whose config comes first")
	}
	h.click("继续安装")
	h.until("installed", func() bool { p, ok := h.plugin("hello"); return ok && p.NeedsGrant && !p.Enabled })
	h.see("待授权")
}

// rawGitHub serves README files in place of raw.githubusercontent.com.
type rawGitHub struct {
	base  http.RoundTripper
	files map[string]string
	got   []string
}

func (r *rawGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "raw.githubusercontent.com" {
		return r.base.RoundTrip(req)
	}
	r.got = append(r.got, req.URL.Path)
	rec := httptest.NewRecorder()
	if s, ok := r.files[req.URL.Path]; ok {
		io.WriteString(rec, s)
	} else {
		rec.WriteHeader(http.StatusNotFound)
	}
	return rec.Result(), nil
}

func TestRegistryDialogReadme(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	en := regPlugin("hello", rs.ScriptRelease("hello", "0.1.0", `{}`, `export default function () {}`))
	en["homepage"] = "https://github.com/godwit-email/plugins/tree/main/packages/hello"
	en["publisher"], en["dataFlow"], en["category"] = "Godwit Community", "local", "other"
	rs.Publish(t, coretest.Registry(regT0, en))
	raw := &rawGitHub{base: rs.Src.Client.Transport, files: map[string]string{
		"/godwit-email/plugins/main/packages/hello/README.md": "# Hello\n\n[简体中文](README.zh-Hans.md)\n\nFirst **line** with a [link](https://evil.example).\n\n## Use\n\n1. Open it\n",
	}}
	rs.Src.Client = &http.Client{Transport: raw}
	h := newHarnessWith(t, []coretest.Option{coretest.WithRegistry(rs.Src)}, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("extensions")
	h.click("浏览插件目录")
	h.see("Godwit Community")
	h.see("仅在本机")
	h.click("安装 hello")
	h.see("由 Godwit Community 发布")
	h.see("以下说明来自插件的 GitHub 主页，不在审核范围内。")
	h.see("First line with a link.")
	h.see("Open it")
	if h.tt.HasText("evil.example") {
		t.Fatal("a README link's target shows")
	}
	if len(raw.got) == 0 || !strings.HasSuffix(raw.got[0], "/README.zh-Hans.md") {
		t.Fatalf("read %v, want the language's README first", raw.got)
	}
}

func TestRegistryDirectoryOffline(t *testing.T) {
	rs := coretest.NewRegistryServer(t)
	h := newHarnessWith(t, []coretest.Option{coretest.WithRegistry(rs.Src)}, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	p := h.openSettings("extensions")
	h.click("浏览插件目录")
	p.ext.reg.Stale, p.ext.reg.Entries = "offline", nil
	h.see("暂时读取不到插件目录")
	h.seeExactly("重试")
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
	release := h.block("plugins")
	h.click("更新到 0.2.0 dlx")
	if !h.tt.HasText("正在安装…") {
		t.Fatal("no visible progress during an update")
	}
	release()
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
	h.see("插件 · 1") // the built-in plugin: the list has loaded
	if _, ok := h.tt.Find("浏览插件目录"); ok {
		t.Fatal("the plugin directory is offered without a registry")
	}
}

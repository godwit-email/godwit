package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// withNativeTranslator installs, grants and (if on) enables the native fixture dlx.
func withNativeTranslator(t *testing.T, on bool) func(*coretest.Env) {
	return func(e *coretest.Env) {
		ctx := context.Background()
		if _, err := e.Svc.InstallPlugin(ctx, coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil)); err != nil {
			t.Fatal(err)
		}
		if err := e.Svc.GrantPermission(ctx, "dlx"); err != nil {
			t.Fatal(err)
		}
		if on {
			if err := e.Svc.SetPluginEnabled(ctx, "dlx", true); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestReaderTranslatesWithPlugin(t *testing.T) {
	h, acc, _ := aiHarness(t, false, func(st *core.Settings) { st.Translator = "plugin/dlx" }, withNativeTranslator(t, true))
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "the original words"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("翻译")
	h.see("[zh-CN] the original words")
	h.see("已翻译为中文 · translate · dlx")
}

func TestReaderTranslatorOff(t *testing.T) {
	h, acc, _ := aiHarness(t, false, func(st *core.Settings) { st.Translator = "plugin/dlx" }, withNativeTranslator(t, false))
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "the original words"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("翻译")
	h.see("翻译插件没有运行")
}

func TestTranslatorSetting(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") }, withNativeTranslator(t, true))
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("general")
	h.see("翻译方式")
	h.click("模型（默认）")
	h.click("dlx")
	h.until("saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.Translator == "plugin/dlx"
	})
	if err := h.env.Svc.SetPluginEnabled(context.Background(), "dlx", false); err != nil {
		t.Fatal(err)
	}
	h.openSettings("extensions")
	h.openSettings("general")
	h.see("dlx（已停用）")
}

func TestScriptTranslatorPermissionLine(t *testing.T) {
	h, _ := withAccount(t)
	if _, err := h.env.Svc.InstallPlugin(context.Background(), coretest.PluginFixture(t, "translate-demo")); err != nil {
		t.Fatal(err)
	}
	h.openSettings("extensions")
	h.see("待授权")
	h.see("会收到你要翻译的邮件内容")
}

package app

import (
	"context"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// withLLM is an App with an account and the fake model service "test".
func withLLM(t *testing.T) *harness {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.LLM(t)
	})
	h.until("initial load", func() bool { return h.app.loaded })
	return h
}

func (h *harness) modelService(name string) (core.ModelService, bool) {
	h.t.Helper()
	list, err := h.env.Svc.ListModelServices(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range list {
		if s.Name == name {
			return s, true
		}
	}
	return core.ModelService{}, false
}

func TestModelServiceCRUD(t *testing.T) {
	h := withLLM(t)
	test, _ := h.modelService("test")
	h.openSettings("models")
	h.see("test · OpenAI 兼容")
	h.see("默认")
	h.click("添加服务")
	h.fill("名称", "local")
	h.fill("Base URL", test.BaseURL)
	h.fill("API Key", "test-key")
	h.click("保存")
	h.until("added", func() bool { _, ok := h.modelService("local"); return ok })
	h.see("编辑：local")

	h.click("从 /models 拉取")
	h.click("m1")
	h.click("保存")
	h.until("models saved", func() bool { s, _ := h.modelService("local"); return slices.Equal(s.Models, []string{"m1"}) })

	h.click("test / m1") // the default model
	h.click("local / m1")
	h.until("default model saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.DefaultModel == "local/m1"
	})

	h.click("测试连接")
	h.see("连接正常 · 0 ms")

	h.click("删除此服务")
	h.until("removed", func() bool { _, ok := h.modelService("local"); return !ok })
	h.until("the list drops it", func() bool { return !h.tt.HasText("local · OpenAI 兼容") })
}

func TestModelServiceKeyKept(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	h.see("编辑：test")
	if f := p.models.form; f == nil || f.key != "" || !f.hasKey {
		t.Fatalf("form %+v: the stored key must not be shown", f)
	}
	if h.tt.HasText("test-key") {
		t.Fatal("the API key shows")
	}
	h.fill("名称", "renamed")
	h.click("保存")
	h.until("renamed", func() bool { _, ok := h.modelService("renamed"); return ok })
	s, _ := h.modelService("renamed")
	if !s.HasKey {
		t.Fatal("saving with an empty key field dropped the key")
	}
	if err := h.env.Svc.TestModelService(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
}

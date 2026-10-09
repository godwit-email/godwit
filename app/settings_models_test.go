package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

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

	m := h.app.pages[PageSettings].(*settingsPage).models
	fetch := m.fetchModels
	m.fetchModels = func(ctx context.Context, id int64) ([]string, error) {
		time.Sleep(50 * time.Millisecond) // "test / m1" shows already: the click must wait for the box itself
		return fetch(ctx, id)
	}
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

// block holds the writes chained on key until release is called.
func (h *harness) block(key string) (release func()) {
	ch := make(chan struct{})
	h.app.writes.Chain(key, func(ctx context.Context) error {
		select {
		case <-ch:
		case <-ctx.Done():
		}
		return nil
	}, nil)
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	h.t.Cleanup(release)
	return release
}

func (h *harness) savedDefault() string {
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return st.DefaultModel
}

func TestModelServiceEditDuringSave(t *testing.T) {
	h := withLLM(t)
	test, _ := h.modelService("test")
	p := h.openSettings("models")
	h.see("编辑：test")
	release := h.block("models")
	h.fill("名称", "renamed")
	h.click("保存")
	h.fill("名称", "renamed again")
	release()
	h.until("saved", func() bool { _, ok := h.modelService("renamed"); return ok })
	h.until("the save settles", func() bool { return !p.models.form.busy })
	if f := p.models.form; f.name != "renamed again" || f.id != test.ID || f.title != "renamed" {
		t.Fatalf("form %+v lost the later edit", f)
	}
}

func TestModelServiceDefaultFollows(t *testing.T) {
	h := withLLM(t)
	h.openSettings("models")
	h.see("编辑：test")
	h.fill("名称", "renamed")
	h.click("保存")
	h.until("default renamed", func() bool { return h.savedDefault() == "renamed/m1" })
	h.see("编辑：renamed")
	h.click("删除此服务")
	h.until("default cleared", func() bool { return h.savedDefault() == "" })
}

func TestModelServiceMissingDefault(t *testing.T) {
	h := withLLM(t)
	st := h.app.settings
	st.DefaultModel = "ghost/x"
	h.app.changeSettings(st)
	h.openSettings("models")
	h.see("未找到的模型：ghost/x")
	if slices.Contains(h.tt.Texts(), "ghost/x") {
		t.Fatal("the missing model is offered")
	}
}

func TestModelServiceBusyAcrossNavigation(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	h.see("编辑：test")
	m := p.models
	release := make(chan struct{})
	m.fetchModels = func(context.Context, int64) ([]string, error) { <-release; return []string{"stale-model"}, nil }
	m.testService = func(context.Context, int64) error { <-release; return nil }
	h.click("从 /models 拉取")
	h.click("测试连接")
	f := m.form
	h.openSettings("general")
	h.openSettings("models")
	close(release)
	h.until("fetch and test settle", func() bool { return !f.fetching && !f.testing })
	if m.form != f {
		t.Fatal("the form was replaced")
	}
	if slices.Contains(f.offered, "stale-model") || f.testMsg != "" || h.tt.HasText("stale-model") {
		t.Fatalf("a result from before the navigation shows: offered %q, test %q", f.offered, f.testMsg)
	}
}

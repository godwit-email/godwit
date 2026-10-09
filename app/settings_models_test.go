package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
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
	h.click("自定义")
	h.fill("名称", "local")
	h.fill("Base URL", test.BaseURL)
	h.fill("API Key", "test-key")

	// a new service lists its models before its first save: it needs one
	m := h.app.pages[PageSettings].(*settingsPage).models
	check := m.checkService
	m.checkService = func(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		time.Sleep(50 * time.Millisecond) // "test / m1" shows already: the click must wait for the box itself
		return check(ctx, id, in)
	}
	h.click("探测")
	h.click("m1")
	h.click("保存")
	h.until("added", func() bool { s, _ := h.modelService("local"); return slices.Equal(s.Models, []string{"m1"}) })
	h.see("编辑：local")

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

func TestRenameServiceKeepsDefault(t *testing.T) {
	h := withLLM(t)
	h.openSettings("models")
	h.see("编辑：test")
	h.see("引用名：test")
	h.fill("名称", "renamed")
	h.click("保存")
	h.until("renamed", func() bool { _, ok := h.modelService("renamed"); return ok })
	h.see("编辑：renamed")
	h.see("引用名：test")
	if d := h.savedDefault(); d != "test/m1" {
		t.Fatalf("default %q: a rename must not touch it", d)
	}
	h.see("renamed / m1") // the default select still finds it
	if h.tt.HasText("未找到的模型：test/m1") {
		t.Fatal("the default shows as missing")
	}
	h.click("删除此服务")
	h.until("default cleared", func() bool { return h.savedDefault() == "" })
}

func TestDeselectDefaultClears(t *testing.T) {
	h := withLLM(t)
	s, _ := h.modelService("test")
	if _, err := h.env.Svc.UpdateModelService(context.Background(), s.ID, core.ModelServiceInput{Name: s.Name, Kind: s.Kind, BaseURL: s.BaseURL, Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err) // a service keeps at least one model
	}
	h.openSettings("models")
	h.see("编辑：test")
	h.click("m1") // untick the default model
	h.click("保存")
	h.until("models saved", func() bool { s, _ := h.modelService("test"); return slices.Equal(s.Models, []string{"m2"}) })
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
	m.checkService = func(context.Context, int64, core.ModelServiceInput) (core.ServiceCheck, error) {
		<-release
		return core.ServiceCheck{Models: []string{"stale-model"}, ListSupported: true}, nil
	}
	m.testService = func(context.Context, int64) error { <-release; return nil }
	h.click("探测")
	h.click("测试连接")
	f := m.form
	h.openSettings("general")
	h.openSettings("models")
	close(release)
	h.until("probe and test settle", func() bool { return !f.probing && !f.testing })
	if m.form != f {
		t.Fatal("the form was replaced")
	}
	if slices.Contains(f.offered, "stale-model") || f.testMsg != "" || h.tt.HasText("stale-model") {
		t.Fatalf("a result from before the navigation shows: offered %q, test %q", f.offered, f.testMsg)
	}
}

// modelsServer is a model service listing models (nil: no /models, 404) that
// answers with status when it is not 0, and records the keys it got.
func modelsServer(t *testing.T, status int, models ...string) (url string, keys func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case status != 0:
			http.Error(w, "no", status)
		case models == nil || r.URL.Path != "/v1/models":
			http.NotFound(w, r)
		default:
			fmt.Fprint(w, `{"data":[`)
			for i, m := range models {
				if i > 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"id":%q}`, m)
			}
			fmt.Fprint(w, `]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(got) }
}

// newService opens the form for a new service from the "自定义" preset.
func (h *harness) newService() *modelsView {
	h.t.Helper()
	p := h.openSettings("models")
	h.see("编辑：test")
	h.click("添加服务")
	h.click("自定义")
	return p.models
}

func (h *harness) typeModel(id string) {
	h.t.Helper()
	h.fill("手动添加模型 ID", id)
	h.click("添加模型")
}

func TestAddFromPreset(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	h.see("编辑：test")
	h.click("添加服务")
	h.click("Ollama")
	f := p.models.form
	if f.kind != ai.KindOpenAI || f.name != "Ollama" || f.baseURL != "http://localhost:11434/v1" {
		t.Fatalf("form %+v", f)
	}
	h.see("API Key（可选）")
	h.see("将请求 http://localhost:11434/v1/chat/completions")
	if h.tt.HasText("获取 API Key ↗") {
		t.Fatal("a local preset links to a key page")
	}
	h.click("添加服务")
	h.click("OpenAI")
	if f := p.models.form; f.kind != ai.KindResponses || f.baseURL != "https://api.openai.com/v1" {
		t.Fatalf("form %+v", f)
	}
	h.click("获取 API Key ↗")
	h.until("the key page opens", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://platform.openai.com/api-keys")
	})
	if h.tt.HasText("API Key（可选）") {
		t.Fatal("a key the service needs is marked optional")
	}
}

func TestBaseURLNormalizedOnBlur(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	h.fill("Base URL", "https://api.example.com/v1/chat/completions/")
	h.click("名称")
	h.until("normalized", func() bool { return m.form.baseURL == "https://api.example.com/v1" })
	h.see("将请求 https://api.example.com/v1/chat/completions")
	h.fill("Base URL", "http://example.com/v1")
	h.click("名称")
	h.see("地址必须使用 https（只有本机地址可以用 http）")
}

func TestManualModelAlwaysAvailable(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0, "listed")
	m := h.newService()
	h.fill("名称", "local")
	h.fill("Base URL", url)
	h.click("探测")
	h.until("listed", func() bool { return slices.Contains(m.form.offered, "listed") })
	h.typeModel("unlisted")
	h.click("保存")
	h.until("saved", func() bool { s, _ := h.modelService("local"); return slices.Equal(s.Models, []string{"unlisted"}) })
}

func TestSaveWithTypedModels(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0) // no /models
	h.newService()
	h.fill("名称", "plain")
	h.fill("Base URL", url)
	h.click("探测")
	h.see("这个服务没有提供模型列表，请手动填写模型 ID")
	h.typeModel("m1")
	h.click("保存")
	h.until("saved", func() bool { s, _ := h.modelService("plain"); return slices.Equal(s.Models, []string{"m1"}) })
}

func TestSaveRejectsBadKey(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, http.StatusUnauthorized)
	m := h.newService()
	h.fill("名称", "bad")
	h.fill("Base URL", url)
	h.fill("API Key", "wrong-key")
	h.typeModel("m1")
	h.click("保存")
	h.see("API Key 被拒绝（401/403）")
	h.until("the save settles", func() bool { return !m.form.probing && !m.form.busy })
	if _, ok := h.modelService("bad"); ok {
		t.Fatal("a service with a rejected key was saved")
	}
}

func TestAddressChangeRequiresKeyDecision(t *testing.T) {
	h := withLLM(t)
	url, keys := modelsServer(t, 0, "m1")
	p := h.openSettings("models")
	h.see("编辑：test")
	h.fill("Base URL", url)
	h.see("地址已改变：请重新输入 API Key，或选择不使用 Key")
	h.click("保存")
	h.click("探测")
	h.drain()
	if s, _ := h.modelService("test"); s.BaseURL == url || len(keys()) != 0 {
		t.Fatalf("saved %q or probed (%q) without a key decision", s.BaseURL, keys())
	}
	h.click("不使用 Key")
	h.click("保存")
	h.until("saved without a key", func() bool { s, _ := h.modelService("test"); return s.BaseURL == url && !s.HasKey })
	h.until("the form shows no key", func() bool { f := p.models.form; return !f.busy && f.savedURL == url && !f.hasKey })
	if slices.Contains(keys(), "Bearer test-key") {
		t.Fatal("the stored key went to the new address")
	}
}

func TestStaleProbeIgnored(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	h.see("编辑：test")
	m := p.models
	release := make(chan struct{})
	m.checkService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		<-release
		return core.ServiceCheck{BaseURL: in.BaseURL, Models: []string{"stale-model"}, ListSupported: true}, nil
	}
	h.click("探测")
	h.fill("Base URL", "https://other.example.com/v1")
	close(release)
	f := m.form
	h.until("the probe settles", func() bool { return !f.probing })
	if slices.Contains(f.offered, "stale-model") || f.checked != "" {
		t.Fatalf("a probe of the old address was adopted: offered %q", f.offered)
	}
}

func TestResponsesKindSelectable(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0, "r1")
	h.newService()
	h.click("OpenAI 兼容（Responses）")
	h.fill("名称", "resp")
	h.fill("Base URL", url)
	h.see("将请求 " + url + "/responses")
	h.typeModel("r1")
	h.click("保存")
	h.until("saved", func() bool { s, _ := h.modelService("resp"); return s.Kind == ai.KindResponses })
}

func TestSmallModelSetting(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick, func(e *coretest.Env) {
		ctx := context.Background()
		svcs, err := e.Svc.ListModelServices(ctx)
		if err != nil || len(svcs) != 1 {
			t.Fatalf("services %+v, %v", svcs, err)
		}
		s := svcs[0]
		if _, err := e.Svc.UpdateModelService(ctx, s.ID, core.ModelServiceInput{Name: s.Name, Kind: s.Kind, BaseURL: s.BaseURL, KeyAction: core.KeyKeep, Models: []string{"m1", "m2"}}); err != nil {
			t.Fatal(err)
		}
	})
	small := func() string {
		st, err := h.env.Svc.GetSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return st.SmallModel
	}
	h.openSettings("models")
	h.see("编辑：test")
	h.see("小任务模型（总结、翻译、即时回复）")
	h.click("与默认模型相同")
	h.click("test / m2")
	h.until("small model saved", func() bool { return small() == "test/m2" })

	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "the plan body"))
	r := h.app.mail.reader
	h.app.navigate(Route{Page: PageMail})
	h.openThread(m.ThreadID)
	h.until("the summary done", func() bool { s := r.ai[m.ID]; return s != nil && s.summary != nil && s.summary.done })
	h.see("总结 · summarize · m2")

	h.openSettings("models")
	h.see("编辑：test")
	h.click("m2") // untick the small model
	h.click("保存")
	h.until("small model cleared", func() bool { return small() == "" })
}

// probes swaps m's probe for one answering from results in turn, each after
// a receive on gate when gate is not nil; it returns how many were asked.
func probes(m *modelsView, gate chan struct{}, results ...error) func() int {
	var mu sync.Mutex
	n := 0
	m.checkService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		if gate != nil {
			<-gate
		}
		mu.Lock()
		err := results[min(n, len(results)-1)]
		n++
		mu.Unlock()
		if err != nil {
			return core.ServiceCheck{}, err
		}
		return core.ServiceCheck{BaseURL: in.BaseURL, Models: []string{"stale-model"}, ListSupported: true}, nil
	}
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func TestRefusedKeyBlocksSave(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	asked := probes(m, nil, nil, core.ErrModelAuth)
	h.fill("名称", "fresh")
	h.fill("Base URL", "https://api.example.com/v1")
	h.fill("API Key", "k")
	h.typeModel("m1")
	h.click("探测")
	h.until("the first probe taken", func() bool { return m.form.checked != "" })
	h.click("探测")
	h.see("API Key 被拒绝（401/403）")
	h.click("保存")
	h.until("save probes again", func() bool { return asked() == 3 && !m.form.probing })
	h.drain()
	if _, ok := h.modelService("fresh"); ok {
		t.Fatal("saved after the key was refused")
	}
	h.see("API Key 被拒绝（401/403）")

	// a saved service whose connection is unchanged probes again too
	h.openSettings("models")
	h.click("test · OpenAI 兼容（Chat Completions）")
	h.see("编辑：test")
	h.click("探测")
	h.see("API Key 被拒绝（401/403）")
	h.fill("名称", "renamed")
	h.click("保存")
	h.until("save probes again", func() bool { return asked() == 5 && !m.form.probing })
	h.drain()
	if _, ok := h.modelService("renamed"); ok {
		t.Fatal("saved after the key was refused")
	}
}

func TestEditDuringSaveProbeDiscards(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	gate := make(chan struct{})
	probes(m, gate, nil)
	h.fill("名称", "first")
	h.fill("Base URL", "https://api.example.com/v1")
	h.typeModel("m1")
	h.click("保存")
	h.fill("名称", "second")
	close(gate)
	f := m.form
	h.until("the probe settles", func() bool { return !f.probing })
	h.drain()
	for _, n := range []string{"first", "second"} {
		if _, ok := h.modelService(n); ok {
			t.Fatalf("%q submitted from a probe the form changed under", n)
		}
	}
	if f.checked != "" {
		t.Fatal("the probe was taken")
	}
}

func TestAddressBackAndForthDiscardsProbe(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	gate := make(chan struct{})
	probes(m, gate, nil)
	h.fill("Base URL", "https://api.example.com/v1")
	h.click("探测")
	h.fill("Base URL", "https://other.example.com/v1")
	h.fill("Base URL", "https://api.example.com/v1")
	close(gate)
	f := m.form
	h.until("the probe settles", func() bool { return !f.probing })
	if slices.Contains(f.offered, "stale-model") || f.checked != "" {
		t.Fatalf("a probe from before the edits was taken: offered %q", f.offered)
	}
}

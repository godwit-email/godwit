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

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
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

// editService opens the page and the dialog of the service called name.
func (h *harness) editService(name string) *settingsPage {
	h.t.Helper()
	p := h.openSettings("models")
	h.click(name + " · OpenAI 兼容（Chat Completions）")
	h.see("编辑：" + name)
	return p
}

// blurKey leaves the API key field, which lists the models.
func (h *harness) blurKey() {
	h.t.Helper()
	h.click("API Key")
	h.click("名称")
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
	h.click("其他服务")
	h.fill("名称", "local")
	h.fill("地址", test.BaseURL)
	h.fill("API Key", "test-key")

	// a new service lists its models before its first save: it needs one
	m := h.app.pages[PageSettings].(*settingsPage).models
	check := m.checkService
	m.checkService = func(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		time.Sleep(50 * time.Millisecond) // "test / m1" shows already: the click must wait for the box itself
		return check(ctx, id, in)
	}
	h.blurKey()
	h.click("m1")
	h.click("添加")
	h.until("added", func() bool { s, _ := h.modelService("local"); return slices.Equal(s.Models, []string{"m1"}) })
	h.until("the dialog closes on the save", func() bool { return m.form == nil && !m.open })
	h.see("local · OpenAI 兼容（Chat Completions）")

	h.click("test / m1") // the default model
	h.click("local / m1")
	h.until("default model saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.DefaultModel == "local/m1"
	})

	h.click("local · OpenAI 兼容（Chat Completions）")
	h.click("测试连接")
	h.see("连接正常 · 0 ms")
	h.until("the list shows it normal", func() bool { return h.tt.HasText("正常") })

	h.click("删除此服务")
	h.click("删除")
	h.until("removed", func() bool { _, ok := h.modelService("local"); return !ok })
	h.until("the list drops it", func() bool { return !h.tt.HasText("local · OpenAI 兼容") })
}

func TestModelServiceKeyKept(t *testing.T) {
	h := withLLM(t)
	p := h.editService("test")
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
	if _, err := h.env.Svc.TestCapability(context.Background(), s.ID, s.Input(), core.CapGen); err != nil {
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
	p := h.editService("test")
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
	h.editService("test")
	h.see("引用名：test")
	h.fill("名称", "renamed")
	h.click("保存")
	h.until("renamed", func() bool { _, ok := h.modelService("renamed"); return ok })
	h.see("renamed · OpenAI 兼容（Chat Completions）") // the dialog closed on the save
	h.click("renamed · OpenAI 兼容（Chat Completions）")
	h.see("引用名：test")
	if d := h.savedDefault(); d != "test/m1" {
		t.Fatalf("default %q: a rename must not touch it", d)
	}
	h.see("renamed / m1") // the default select still finds it
	if h.tt.HasText("未找到的模型：test/m1") {
		t.Fatal("the default shows as missing")
	}
	h.click("删除此服务")
	h.click("删除")
	h.until("default cleared", func() bool { return h.savedDefault() == "" })
}

func TestDeselectDefaultClears(t *testing.T) {
	h := withLLM(t)
	s, _ := h.modelService("test")
	if _, err := h.env.Svc.UpdateModelService(context.Background(), s.ID, core.ModelServiceInput{Name: s.Name, Kind: s.Kind, BaseURL: s.BaseURL, Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err) // a service keeps at least one model
	}
	h.editService("test")
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
	m := p.models
	release := make(chan struct{})
	m.checkService = func(context.Context, int64, core.ModelServiceInput) (core.ServiceCheck, error) {
		<-release
		return core.ServiceCheck{Models: []string{"stale-model"}, ListSupported: true}, nil
	}
	m.testService = func(context.Context, int64, core.ModelServiceInput, string) (core.CapStatus, error) {
		<-release
		return core.CapStatus{}, nil
	}
	h.click("test · OpenAI 兼容（Chat Completions）") // opening lists the models: that fetch waits on release
	h.see("编辑：test")
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

// newService opens the dialog for a new service from the "其他服务" provider.
func (h *harness) newService() *modelsView {
	h.t.Helper()
	p := h.openSettings("models")
	h.see("test · OpenAI 兼容")
	h.click("添加服务")
	h.click("其他服务")
	return p.models
}

func (h *harness) typeModel(id string) {
	h.t.Helper()
	h.fill("添加模型 ID", id)
	h.click("添加模型")
}

func TestAddFromPreset(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	h.see("test · OpenAI 兼容")
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
	h.fill("地址", "https://api.example.com/v1/chat/completions/")
	h.click("名称")
	h.until("normalized", func() bool { return m.form.baseURL == "https://api.example.com/v1" })
	h.see("将请求 https://api.example.com/v1/chat/completions")
	h.fill("地址", "http://example.com/v1")
	h.click("名称")
	h.see("地址必须使用 https（只有本机地址可以用 http）")
}

func TestManualModelAlwaysAvailable(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0, "listed")
	m := h.newService()
	h.fill("名称", "local")
	h.fill("地址", url)
	h.blurKey()
	h.until("listed", func() bool { return slices.Contains(m.form.offered, "listed") })
	h.typeModel("unlisted")
	h.click("添加")
	h.until("saved", func() bool { s, _ := h.modelService("local"); return slices.Equal(s.Models, []string{"unlisted"}) })
}

func TestSaveWithTypedModels(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0) // no /models
	h.newService()
	h.fill("名称", "plain")
	h.fill("地址", url)
	h.blurKey()
	h.see("拉取 /models 失败（HTTP 404）。这个服务可能不提供模型列表，直接输入模型 ID 也可以。")
	h.typeModel("m1")
	h.click("添加")
	h.until("saved", func() bool { s, _ := h.modelService("plain"); return slices.Equal(s.Models, []string{"m1"}) })
}

func TestSaveRejectsBadKey(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, http.StatusUnauthorized)
	m := h.newService()
	h.fill("名称", "bad")
	h.fill("地址", url)
	h.fill("API Key", "wrong-key")
	h.typeModel("m1")
	h.click("添加")
	h.see("API Key 被拒绝（401/403）")
	h.until("the save settles", func() bool { return !m.form.probing && !m.form.busy })
	if _, ok := h.modelService("bad"); ok {
		t.Fatal("a service with a rejected key was saved")
	}
}

func TestAddressChangeRequiresKeyDecision(t *testing.T) {
	h := withLLM(t)
	url, keys := modelsServer(t, 0, "m1")
	p := h.editService("test")
	h.fill("地址", url)
	h.see("地址已改变：请重新输入 API Key，或选择不使用 Key")
	h.click("保存")
	h.blurKey()
	h.drain()
	if s, _ := h.modelService("test"); s.BaseURL == url || len(keys()) != 0 {
		t.Fatalf("saved %q or probed (%q) without a key decision", s.BaseURL, keys())
	}
	h.click("不使用 Key")
	h.click("保存")
	h.until("saved without a key", func() bool { s, _ := h.modelService("test"); return s.BaseURL == url && !s.HasKey })
	h.until("the dialog closes on the save", func() bool { return p.models.form == nil })
	if slices.Contains(keys(), "Bearer test-key") {
		t.Fatal("the stored key went to the new address")
	}
}

func TestStaleProbeIgnored(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	m := p.models
	release := make(chan struct{})
	m.checkService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		<-release
		return core.ServiceCheck{BaseURL: in.BaseURL, Models: []string{"stale-model"}, ListSupported: true}, nil
	}
	h.click("test · OpenAI 兼容（Chat Completions）") // opening lists the models
	h.see("编辑：test")
	f := m.form
	h.until("the fetch started", func() bool { return f.probing })
	h.fill("地址", "https://other.example.com/v1")
	close(release)
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
	h.fill("地址", url)
	h.see("将请求 " + url + "/responses")
	h.typeModel("r1")
	h.click("添加")
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
	h.see("小任务模型（总结、翻译、即时回复）")
	h.click("与默认模型相同")
	h.click("test / m2")
	h.until("small model saved", func() bool { return small() == "test/m2" })

	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "the plan body"))
	r := h.app.mail.reader
	h.app.navigate(Route{Page: PageMail})
	h.openThread(m.ThreadID)
	h.until("the summary done", func() bool { s := r.ai[m.ID]; return s != nil && s.summary != nil && s.summary.done })
	h.see("summarize · m2")

	h.editService("test")
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
	h.fill("地址", "https://api.example.com/v1")
	h.fill("API Key", "k")
	h.typeModel("m1") // leaving the key field lists the models
	h.until("the first probe taken", func() bool { return m.form.checked != "" })
	h.blurKey()
	h.see("API Key 被拒绝（401/403）")
	h.click("添加")
	h.until("save probes again", func() bool { return asked() == 3 && !m.form.probing })
	h.drain()
	if _, ok := h.modelService("fresh"); ok {
		t.Fatal("saved after the key was refused")
	}
	h.see("API Key 被拒绝（401/403）")

	// a saved service whose connection is unchanged probes again too
	h.click("取消")
	h.click("test · OpenAI 兼容（Chat Completions）") // opening lists the models
	h.see("编辑：test")
	h.until("the fetch settles", func() bool { return asked() == 4 && !m.form.probing })
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
	h.fill("地址", "https://api.example.com/v1")
	h.typeModel("m1")
	h.click("添加")
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
	h.fill("地址", "https://api.example.com/v1")
	h.blurKey()
	h.fill("地址", "https://other.example.com/v1")
	h.fill("地址", "https://api.example.com/v1")
	close(gate)
	f := m.form
	h.until("the probe settles", func() bool { return !f.probing })
	if slices.Contains(f.offered, "stale-model") || f.checked != "" {
		t.Fatalf("a probe from before the edits was taken: offered %q", f.offered)
	}
}

func TestManualTextDuringProbeDiscards(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	gate := make(chan struct{})
	probes(m, gate, nil)
	h.fill("名称", "first")
	h.fill("地址", "https://api.example.com/v1")
	h.typeModel("m1")
	h.click("添加")
	h.fill("添加模型 ID", "m2")
	close(gate)
	f := m.form
	h.until("the probe settles", func() bool { return !f.probing })
	h.drain()
	if _, ok := h.modelService("first"); ok || f.checked != "" {
		t.Fatal("submitted from a probe the manual model field changed under")
	}
}

func TestManualTextKeptAcrossSave(t *testing.T) {
	h := withLLM(t)
	p := h.editService("test")
	release := h.block("models")
	h.fill("名称", "renamed")
	h.click("保存")
	h.fill("添加模型 ID", "draft-id")
	release()
	h.until("saved", func() bool { _, ok := h.modelService("renamed"); return ok })
	h.until("the save settles", func() bool { return p.models.form.title == "renamed" && !p.models.form.busy })
	if got := p.models.form.manual; got != "draft-id" {
		t.Fatalf("manual text %q, want the typed draft-id", got)
	}
}

// TestSaveFillsEmptyDefault: with no default model, saving a service makes its
// first model the default, so Wit works without a second step.
func TestSaveFillsEmptyDefault(t *testing.T) {
	h := withLLM(t)
	st := h.app.settings
	st.DefaultModel = ""
	h.app.changeSettings(st)
	h.editService("test")
	h.fill("名称", "test2")
	h.click("保存")
	h.until("default filled", func() bool { return h.savedDefault() == "test/m1" })
}

func TestModelsEmptyState(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	m := h.openSettings("models").models
	h.see("还没有接入模型服务")
	h.see("连接本地网关")
	if n := h.countTexts("添加服务"); n != 2 { // one button (label and text): the empty state's; the header's is hidden
		t.Fatalf("%d 添加服务 texts, want one button's", n)
	}
	h.click("连接本地网关")
	h.see("添加模型服务")
	if f := m.form; f == nil || f.preset != presetCustom || f.baseURL != "http://localhost:" {
		t.Fatalf("form %+v: want 其他服务 with a localhost address", f)
	}
	h.click("取消")
	h.click("添加服务")
	h.see("添加模型服务")
}

func TestModelsDialogAutoFetchOnBlur(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0, "listed")
	m := h.newService()
	h.fill("名称", "auto")
	h.fill("地址", url)
	h.fill("API Key", "k")
	h.click("名称") // leaving the key field lists the models
	h.until("listed", func() bool { return slices.Contains(m.form.offered, "listed") })
	h.see("已从 /models 拉取 1 个模型，点一下选中。")

	// a result that comes back after the key changed, or the dialog closed, is dropped
	gate := make(chan struct{})
	probes(m, gate, nil)
	f := m.form
	h.fill("API Key", "k2")
	h.click("名称")
	h.until("fetching", func() bool { return f.probing })
	h.fill("API Key", "k3")
	close(gate)
	h.until("the fetch settles", func() bool { return !f.probing })
	if slices.Contains(f.offered, "stale-model") || f.checked != "" {
		t.Fatalf("a fetch for the old key was adopted: offered %q", f.offered)
	}

	gate = make(chan struct{})
	probes(m, gate, nil)
	h.click("名称")
	h.until("fetching again", func() bool { return f.probing })
	h.click("取消")
	close(gate)
	h.until("the fetch settles", func() bool { return !f.probing })
	if slices.Contains(f.offered, "stale-model") || m.form != nil {
		t.Fatalf("a fetch after the dialog closed was adopted: offered %q, form %v", f.offered, m.form)
	}
}

func TestModelsFetchFailureRetry(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	fail := true
	m.checkService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		if fail {
			return core.ServiceCheck{}, context.DeadlineExceeded
		}
		return core.ServiceCheck{BaseURL: in.BaseURL, Models: []string{"back"}, ListSupported: true}, nil
	}
	h.fill("地址", "https://api.example.com/v1")
	h.blurKey()
	h.see("重试")
	fail = false
	h.click("重试")
	h.until("listed", func() bool { return slices.Contains(m.form.offered, "back") })
}

func TestModelsDeleteConfirmStrip(t *testing.T) {
	h := withLLM(t)
	h.editService("test")
	h.click("删除此服务")
	h.see("删除「test」？用到它的 skill 会改用默认模型，Key 会从钥匙串移除。")
	if _, ok := h.modelService("test"); !ok {
		t.Fatal("asking removed the service")
	}
	if h.tt.HasText("测试连接") {
		t.Fatal("the footer's buttons stay under the confirm strip")
	}
	h.click("取消")
	h.see("测试连接")
	h.click("删除此服务")
	h.click("删除")
	h.until("removed", func() bool { _, ok := h.modelService("test"); return !ok })
	h.see("还没有接入模型服务")
}

// TestModelStatusChipDark: the rendered chip's fill and dot are the dark tokens.
func TestModelStatusChipDark(t *testing.T) {
	d := theme.Dark()
	for _, c := range []struct {
		s       svcStatus
		bg, dot ui.Color
		label   string
	}{
		{statusOK, d.SuccessBg, d.Success, "正常"},
		{statusUntested, d.Hover, d.Muted, "未测试"},
		{statusBad, d.DangerBg, d.Danger, "Key 失效"},
	} {
		tt := ui.NewTester(func(c2 *ui.Context) {
			theme.Apply(c2, theme.FromSettings(core.Settings{Theme: "dark", Density: "compact", FontSize: 14, BodyFontSize: 14}, false))
			ui.Box(c2).Padding(20).Children(func() { statusChip(c2, c.s) })
		}, 200, 100)
		r, ok := tt.Find(c.label)
		if !ok {
			t.Fatalf("no chip %q", c.label)
		}
		y := int(r.Y + r.H/2)
		img := tt.Image()
		for name, p := range map[string]struct {
			x    int
			want ui.Color
		}{"fill": {int(r.X+r.W) + 3, c.bg}, "dot": {int(r.X) - 9, c.dot}} {
			got, want := img.RGBAAt(p.x, y), p.want.SRGB()
			if dd := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); dd > 8 {
				t.Errorf("%s %s: pixel %v, want dark %v", c.label, name, got, want)
			}
		}
	}
}

func TestModelStatusFromTest(t *testing.T) {
	h := withLLM(t)
	p := h.editService("test")
	m := p.models
	if m.status[m.form.id] == statusBad {
		t.Fatal("starts invalid")
	}
	m.testService = func(context.Context, int64, core.ModelServiceInput, string) (core.CapStatus, error) {
		return core.CapStatus{}, core.ErrModelAuth
	}
	h.click("测试连接")
	h.until("key invalid", func() bool { return m.status[m.form.id] == statusBad })
	h.click("取消")
	h.see("Key 失效")
}

func TestModelsKeyEyeShowsOnlyTyped(t *testing.T) {
	h := withLLM(t)
	h.editService("test") // its key is stored: the field is empty
	h.click("显示 API Key")
	h.see("隐藏 API Key")
	if h.tt.HasText("test-key") {
		t.Fatal("the stored key shows")
	}
	h.fill("API Key", "typed-secret")
	m := h.app.pages[PageSettings].(*settingsPage).models
	if !m.form.showKey || m.form.key != "typed-secret" {
		t.Fatalf("form %+v", m.form)
	}
}

func TestModelsCommonChipsAndCaptions(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	h.see("自填地址") // the tile's caption
	h.see("常用")
	h.click("Groq 常用")
	if f := m.form; f.name != "Groq" || f.baseURL != "https://api.groq.com/openai/v1" || f.preset != presetCustom {
		t.Fatalf("form %+v", f)
	}
	h.click("OpenAI") // a provider tile: no common chips
	if h.tt.HasText("常用") {
		t.Fatal("common chips show for a provider")
	}
}

func TestModelsFetchFailureShowsStatus(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, http.StatusInternalServerError)
	h.newService()
	h.fill("名称", "flaky")
	h.fill("地址", url)
	h.blurKey()
	h.see("拉取 /models 失败（HTTP 500）")
}

// A blur while a fetch runs, after the key changed, fetches again once the stale result is dropped.
func TestModelsFetchAgainAfterStaleDrop(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	gate := make(chan struct{})
	m.checkService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		<-gate
		return core.ServiceCheck{BaseURL: in.BaseURL, Models: []string{"for-" + in.APIKey}, ListSupported: true}, nil
	}
	f := m.form
	h.fill("地址", "https://api.example.com/v1")
	h.fill("API Key", "one")
	h.click("名称")
	h.until("fetching", func() bool { return f.probing })
	h.fill("API Key", "two")
	h.click("名称") // blur while the first fetch runs
	close(gate)
	h.until("fetched for the new key", func() bool { return slices.Contains(f.offered, "for-two") && !f.probing })
	if slices.Contains(f.offered, "for-one") {
		t.Fatalf("offered %q: the old key's result was adopted", f.offered)
	}
}

// Turning generate off moves the default to the first other service that
// generates and clears the small model; turning judge off clears its default.
func TestDropRefsGenOff(t *testing.T) {
	gw := core.ModelService{ID: 1, Slug: "gw", Models: []string{"m"}, JudgeFormat: "decisions"}
	others := []core.ModelService{gw, {ID: 2, Slug: "ts", JudgeFormat: "systemone"}, {ID: 3, Slug: "or", Kind: "openai", Models: []string{"a", "b"}}}
	st := core.Settings{DefaultModel: "gw/m", SmallModel: "gw/m", JudgeService: "gw"}
	got, changed := dropRefs(st, gw, others)
	if !changed || got.DefaultModel != "or/a" || got.SmallModel != "" || got.JudgeService != "gw" {
		t.Fatalf("%v %+v", changed, got)
	}
	gw.Kind, gw.JudgeFormat = "openai", ""
	got, changed = dropRefs(st, gw, others)
	if !changed || got.DefaultModel != "gw/m" || got.SmallModel != "gw/m" || got.JudgeService != "" {
		t.Fatalf("judge off: %v %+v", changed, got)
	}
	got, _ = dropRefs(st, core.ModelService{ID: 1, Slug: "gw"}, others[:2]) // removed, no alt
	if got.DefaultModel != "" || got.SmallModel != "" || got.JudgeService != "" {
		t.Fatalf("removed: %+v", got)
	}
}

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/kit"
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
	h.click(name)
	h.see("编辑 " + name)
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
	h.see("test")
	h.see("默认")
	h.click("添加服务")
	h.click("其他服务")
	h.fill("名称", "local")
	h.fill("地址", test.BaseURL)
	h.fill("API Key", "test-key")

	// leaving the key lists the models and chooses the first
	m := h.app.pages[PageSettings].(*settingsPage).models
	h.blurKey()
	h.until("m1 chosen", func() bool { return m.form != nil && slices.Equal(m.form.models, []string{"m1"}) })
	h.click("添加")
	h.until("added", func() bool { s, _ := h.modelService("local"); return slices.Equal(s.Models, []string{"m1"}) })
	h.until("the dialog closes on the save", func() bool { return m.form == nil && !m.open })
	h.see("local")

	h.click("test / m1") // the default model
	h.click("local / m1")
	h.until("default model saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.DefaultModel == "local/m1"
	})

	h.click("local")
	h.click("测试连接")
	h.until("the test passes", func() bool { return m.form != nil && m.form.gen.state == "ok" })
	h.until("the list shows it normal", func() bool { return h.tt.HasText("正常") })

	h.click("删除此服务")
	h.click("删除")
	h.until("removed", func() bool { _, ok := h.modelService("local"); return !ok })
	h.until("the list drops it", func() bool { return !h.tt.HasText("local") })
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
	h.fill("名称", "renamed")
	h.click("保存")
	h.until("renamed", func() bool { _, ok := h.modelService("renamed"); return ok })
	h.see("renamed") // the dialog closed on the save
	h.click("renamed")
	h.see("编辑 renamed")
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
	h.click("test") // opening lists the models: that fetch waits on release
	h.see("编辑 test")
	h.click("测试连接")
	f := m.form
	h.openSettings("general")
	h.openSettings("models")
	close(release)
	h.until("probe and test settle", func() bool { return !f.probing && !f.testing() })
	if m.form != f {
		t.Fatal("the form was replaced")
	}
	if slices.Contains(f.offered, "stale-model") || f.gen.state != "" || h.tt.HasText("stale-model") {
		t.Fatalf("a result from before the navigation shows: offered %q, test %+v", f.offered, f.gen)
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
	h.see("test")
	h.click("添加服务")
	h.click("其他服务")
	return p.models
}

func (h *harness) typeModel(id string) {
	h.t.Helper()
	h.fill("添加模型 ID", id)
	h.click("添加模型")
}

func TestProviderTiles(t *testing.T) {
	h := withLLM(t)
	p := h.openSettings("models")
	h.see("test")
	h.click("添加服务")
	f := p.models.form
	if f.provider != core.ProviderOpenAI || f.name != "OpenAI" || f.baseURL != "https://api.openai.com/v1" || f.kind != ai.KindResponses || !f.genOn || !f.judgeOn || f.judgeModel != "gpt-6-luna" {
		t.Fatalf("form %+v", f)
	}
	h.see("https://api.openai.com/v1/responses")
	h.click("Chat Completions")
	h.see("https://api.openai.com/v1/chat/completions")
	h.click("Anthropic")
	if f := p.models.form; f.provider != core.ProviderAnthropic || f.baseURL != "https://api.anthropic.com/v1" || f.judgeOn {
		t.Fatalf("form %+v", f)
	}
	h.see("Anthropic 不提供判断接口，判断会由生成模型兼任。")
	h.see("https://api.anthropic.com/v1/messages")
	h.click("TypeSafe")
	h.see("TypeSafe 不提供生成接口。")
	h.see("https://api.typesafe.ai/v1/systemone")
	h.click("其他服务")
	if f := p.models.form; f.provider != core.ProviderOther || f.name != "" || f.baseURL != "" || !f.genOn || f.judgeOn {
		t.Fatalf("form %+v", f)
	}
}

func TestBaseURLNormalizedOnBlur(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	h.fill("地址", "https://api.example.com/v1/chat/completions/")
	h.click("名称")
	h.until("normalized", func() bool { return m.form.baseURL == "https://api.example.com/v1" })
	h.see("https://api.example.com/v1/chat/completions")
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
	h.until("saved", func() bool {
		s, _ := h.modelService("local")
		return slices.Equal(s.Models, []string{"listed", "unlisted"})
	})
}

func TestSaveWithTypedModels(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0) // no /models
	h.newService()
	h.fill("名称", "plain")
	h.fill("地址", url)
	h.blurKey()
	h.see("拉取 /models 失败（404）。这个服务可能不提供模型列表，直接输入模型 ID 也可以。")
	h.typeModel("m1")
	h.click("添加")
	h.until("saved", func() bool { s, _ := h.modelService("plain"); return slices.Equal(s.Models, []string{"m1"}) })
}

// TestSaveDespiteRefusedKey: a refused key shows on the field, and saving
// doesn't wait on a probe or a test.
func TestSaveDespiteRefusedKey(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, http.StatusUnauthorized)
	m := h.newService()
	h.fill("名称", "bad")
	h.fill("地址", url)
	h.fill("API Key", "wrong-key")
	h.typeModel("m1") // leaving the key field lists the models
	h.see("API Key 被拒绝（401/403）")
	h.until("the fetch settles", func() bool { return !m.form.probing })
	h.click("添加")
	h.until("saved", func() bool { s, _ := h.modelService("bad"); return slices.Equal(s.Models, []string{"m1"}) })
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
	h.click("test") // opening lists the models
	h.see("编辑 test")
	f := m.form
	h.until("the fetch started", func() bool { return f.probing })
	h.fill("地址", "https://other.example.com/v1")
	close(release)
	h.until("the probe settles", func() bool { return !f.probing })
	if slices.Contains(f.offered, "stale-model") {
		t.Fatalf("a probe of the old address was adopted: offered %q", f.offered)
	}
}

func TestResponsesKindSelectable(t *testing.T) {
	h := withLLM(t)
	url, _ := modelsServer(t, 0, "r1")
	h.newService()
	h.click("OpenAI Responses")
	h.fill("名称", "resp")
	h.fill("地址", url)
	h.see(url + "/responses")
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
	h.see("小任务")
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
	if slices.Contains(f.offered, "stale-model") {
		t.Fatalf("a probe from before the edits was taken: offered %q", f.offered)
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
	if f := m.form; f == nil || f.provider != core.ProviderOther || f.baseURL != localGateway || f.name != "本地网关" || !f.nameAuto {
		t.Fatalf("form %+v: want 其他服务 named 本地网关 with a loopback address", f)
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
	if slices.Contains(f.offered, "stale-model") {
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

// TestModelStatusLineDark: a status line's dot is the dark token of its look.
func TestModelStatusLineDark(t *testing.T) {
	d := theme.Dark()
	for _, c := range []struct {
		l   look
		dot ui.Color
	}{{lookOK, d.Success}, {lookUntested, d.Attention}, {lookFail, d.Danger}, {lookSlow, d.Attention}, {lookNone, d.Idle}} {
		tt := ui.NewTester(func(c2 *ui.Context) {
			theme.Apply(c2, theme.FromSettings(core.Settings{Theme: "dark", Density: "compact", FontSize: 14, BodyFontSize: 14}, false))
			ui.Box(c2).Padding(20).Children(func() { statusLine(c2, line{"状态", c.l}) })
		}, 200, 100)
		r, ok := tt.Find("状态")
		if !ok {
			t.Fatal("no status line")
		}
		got, want := tt.Image().RGBAAt(int(r.X)-9, int(r.Y+r.H/2)), c.dot.SRGB() // the 6 px dot, 6 px before the text
		if dd := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); dd > 8 {
			t.Errorf("look %d dot: pixel %v, want dark %v", c.l, got, want)
		}
	}
}

// TestModelStatusFromTest: a failed test is stored, and the list and 用途 read it.
func TestModelStatusFromTest(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		e.LLM(t).Script(coretest.Turn{Status: 401})
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.editService("test")
	h.see("未测试")
	h.click("测试连接")
	h.see("Key 失效")
	h.click("取消")
	h.see("test · 401 · API Key 无效")
	h.see("生成用的 test 连不上（401 · API Key 无效）：总结、翻译、写信和 Wit 暂时不可用，判断也一样。")
	h.click("去修复")
	h.see("编辑 test")
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

func TestModelsGatewayChipsAndCaptions(t *testing.T) {
	h := withLLM(t)
	m := h.newService()
	h.see("生成 · 判断") // the tiles' captions
	h.click("OpenRouter 常用")
	if f := m.form; f.name != "OpenRouter" || f.baseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("form %+v", f)
	}
	h.fill("地址", "https://llm.example.com/v1") // the name follows the host
	h.until("named after the host", func() bool { return m.form.name == "llm.example.com" })
	h.fill("名称", "mine")
	h.click("LiteLLM 常用")
	if f := m.form; f.name != "mine" || f.baseURL != "http://127.0.0.1:4000" {
		t.Fatalf("form %+v: a typed name stays", f)
	}
	h.click("OpenAI") // a provider tile: no 常用
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
	h.see("拉取 /models 失败（500）")
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

// TestDeriveUsage: the 用途 card's status lines, alert, suggestion and
// disabled selects follow the defaults and the services' stored tests.
func TestDeriveUsage(t *testing.T) {
	ok := func(ms int) core.CapStatus { return core.CapStatus{State: "ok", LatencyMs: ms} }
	auth := core.CapStatus{State: "fail", Code: "auth", HTTPStatus: 401}
	oai := core.ModelService{ID: 1, Name: "OpenAI", Slug: "openai", Provider: core.ProviderOpenAI, Kind: "openai-responses", Models: []string{"[MODEL A]", "[MODEL B]"},
		JudgeFormat: "decisions", JudgeModel: "gpt-6-luna", Status: map[string]core.CapStatus{core.CapGen: ok(812), core.CapJudge: ok(143)}}
	ant := core.ModelService{ID: 2, Name: "Anthropic", Slug: "anthropic", Kind: "anthropic", Models: []string{"[MODEL D]"}}
	ts := core.ModelService{ID: 3, Name: "TypeSafe", Slug: "typesafe", JudgeFormat: "systemone", JudgeModel: "jev-latest"}
	broken := oai
	broken.Status = map[string]core.CapStatus{core.CapGen: auth, core.CapJudge: auth}
	labels := func(o []kit.Option[string]) (l []string) {
		for _, x := range o {
			l = append(l, x.Label)
		}
		return l
	}
	for _, c := range []struct {
		name            string
		st              core.Settings
		svcs            []core.ModelService
		gen, judge      line
		alert           string
		suggest, off    bool
		genOpts, judges []string
	}{
		{name: "normal", st: core.Settings{DefaultModel: "openai/[MODEL A]", JudgeService: "openai"}, svcs: []core.ModelService{oai, ant},
			gen: line{"OpenAI · 连接正常 · 812 ms", lookOK}, judge: line{"OpenAI · 连接正常 · 143 ms", lookOK},
			genOpts: []string{"OpenAI / [MODEL A]", "OpenAI / [MODEL B]", "Anthropic / [MODEL D]"}, judges: []string{"由生成模型兼任（较慢）", "OpenAI / gpt-6-luna"}},
		{name: "gen-only", st: core.Settings{DefaultModel: "anthropic/[MODEL D]"}, svcs: []core.ModelService{ant},
			gen: line{"Anthropic · 未测试", lookUntested}, judge: line{"用 Anthropic / [MODEL D] 回答判断题，约慢 10 倍", lookSlow}, suggest: true,
			genOpts: []string{"Anthropic / [MODEL D]"}, judges: []string{"由生成模型兼任（较慢）"}},
		{name: "broken", st: core.Settings{DefaultModel: "openai/[MODEL A]", JudgeService: "openai"}, svcs: []core.ModelService{broken},
			gen: line{"OpenAI · 401 · API Key 无效", lookFail}, judge: line{"OpenAI · 401 · API Key 无效", lookFail},
			alert:   "生成用的 OpenAI 连不上（401 · API Key 无效）：总结、翻译、写信和 Wit 暂时不可用，判断也一样。",
			genOpts: []string{"OpenAI / [MODEL A]", "OpenAI / [MODEL B]"}, judges: []string{"由生成模型兼任（较慢）", "OpenAI / gpt-6-luna"}},
		{name: "broken gen, judge fine", st: core.Settings{DefaultModel: "openai/[MODEL A]", JudgeService: "typesafe"}, svcs: []core.ModelService{broken, ts},
			gen: line{"OpenAI · 401 · API Key 无效", lookFail}, judge: line{"TypeSafe · 未测试", lookUntested},
			alert:   "生成用的 OpenAI 连不上（401 · API Key 无效）：总结、翻译、写信和 Wit 暂时不可用。",
			genOpts: []string{"OpenAI / [MODEL A]", "OpenAI / [MODEL B]"}, judges: []string{"由生成模型兼任（较慢）", "OpenAI / gpt-6-luna", "TypeSafe / jev-latest"}},
		{name: "judge only: nothing generates", st: core.Settings{JudgeService: "typesafe"}, svcs: []core.ModelService{ts},
			gen: line{"未配置，这些功能暂时不可用", lookNone}, judge: line{"未配置", lookNone}, off: true,
			genOpts: []string{"先添加一个服务"}, judges: []string{"先添加一个服务"}},
		{name: "missing default", st: core.Settings{DefaultModel: "ghost/x"}, svcs: []core.ModelService{ant},
			gen: line{"未找到的模型：ghost/x", lookFail}, judge: line{"未配置", lookNone},
			genOpts: []string{"未设置", "Anthropic / [MODEL D]"}, judges: []string{"由生成模型兼任（较慢）"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := deriveUsage(c.st, c.svcs)
			if u.gen != c.gen || u.judge != c.judge {
				t.Errorf("lines %+v %+v, want %+v %+v", u.gen, u.judge, c.gen, c.judge)
			}
			if u.alert != c.alert || u.suggest != c.suggest || u.disabled != c.off {
				t.Errorf("alert %q suggest %v disabled %v", u.alert, u.suggest, u.disabled)
			}
			if !slices.Equal(labels(u.genOpts), c.genOpts) || !slices.Equal(labels(u.judgeOpts), c.judges) {
				t.Errorf("options %q %q", labels(u.genOpts), labels(u.judgeOpts))
			}
			if c.off != (len(u.smallOpts) == 1) {
				t.Errorf("small options %q", labels(u.smallOpts))
			}
		})
	}
}

// TestServiceLook: the list's chip is Key 失效 on a refused key, 连接失败 on
// another failure, 未测试 while an enabled capability is untested.
func TestServiceLook(t *testing.T) {
	ok, auth, down := core.CapStatus{State: "ok"}, core.CapStatus{State: "fail", Code: "auth"}, core.CapStatus{State: "fail", Code: "timeout"}
	for _, c := range []struct {
		svc   core.ModelService
		look  look
		label string
	}{
		{core.ModelService{Kind: "openai", Status: map[string]core.CapStatus{core.CapGen: ok}}, lookOK, "正常"},
		{core.ModelService{Kind: "openai"}, lookUntested, "未测试"},
		{core.ModelService{Kind: "openai", JudgeFormat: "decisions", Status: map[string]core.CapStatus{core.CapGen: ok}}, lookUntested, "未测试"},
		{core.ModelService{Kind: "openai", JudgeFormat: "decisions", Status: map[string]core.CapStatus{core.CapGen: down, core.CapJudge: auth}}, lookFail, "Key 失效"},
		{core.ModelService{Kind: "openai", Status: map[string]core.CapStatus{core.CapGen: down}}, lookFail, "连接失败"},
		{core.ModelService{JudgeFormat: "systemone", Status: map[string]core.CapStatus{core.CapGen: auth, core.CapJudge: ok}}, lookOK, "正常"}, // generate off: its old status doesn't count
	} {
		if l, label := svcLook(c.svc); l != c.look || label != c.label {
			t.Errorf("%+v: %d %q, want %d %q", c.svc, l, label, c.look, c.label)
		}
	}
}

// TestModelFormRules: the design's blocked, hint and cantTest rules, in its order.
func TestModelFormRules(t *testing.T) {
	ready := func() *modelForm { f := newForm(core.ProviderOpenAI); f.key, f.models = "k", []string{"m"}; return f }
	for _, c := range []struct {
		name          string
		edit          func(f *modelForm)
		blocked, cant bool
		hint          string
	}{
		{"ready", func(*modelForm) {}, false, false, ""},
		{"no capability", func(f *modelForm) { f.genOn, f.judgeOn, f.baseURL = false, false, "" }, true, true, "至少开启一种能力"},
		{"no address", func(f *modelForm) { f.baseURL, f.name = " ", "" }, true, true, "填上地址"},
		{"no key on add", func(f *modelForm) { f.key, f.name = "", "" }, true, true, "填上 API Key 才能添加"},
		{"no key on edit: the stored one", func(f *modelForm) { f.id, f.key, f.hasKey = 1, "", true }, false, false, ""},
		{"other needs no key", func(f *modelForm) { f.provider, f.key = core.ProviderOther, "" }, false, false, ""},
		{"no name", func(f *modelForm) { f.name = "" }, true, false, "填上名称"},
		{"generate without a model", func(f *modelForm) { f.models = nil }, true, false, "至少选一个生成模型"},
		{"judge only needs no model", func(f *modelForm) { f.models, f.genOn = nil, false }, false, false, ""},
		{"a failed test doesn't block", func(f *modelForm) { f.judge = capTest{"fail", "x"} }, false, false, "有一项没通过测试，仍然可以添加，之后再改"},
		{"failed on edit", func(f *modelForm) { f.id, f.gen = 1, capTest{"fail", "x"} }, false, false, "有一项没通过测试，仍然可以保存，之后再改"},
		{"testing", func(f *modelForm) { f.gen = capTest{state: "testing"} }, false, true, ""},
	} {
		f := ready()
		c.edit(f)
		if f.blocked() != c.blocked || f.cantTest() != c.cant || f.hint() != c.hint {
			t.Errorf("%s: blocked %v cantTest %v hint %q, want %v %v %q", c.name, f.blocked(), f.cantTest(), f.hint(), c.blocked, c.cant, c.hint)
		}
	}
}

// TestModelFormDefaults: each provider's blank form, as the design's blank().
func TestModelFormDefaults(t *testing.T) {
	for _, c := range []struct {
		p, name, url, kind, judge string
		genOn, judgeOn            bool
	}{
		{core.ProviderOpenAI, "OpenAI", "https://api.openai.com/v1", ai.KindResponses, "decisions", true, true},
		{core.ProviderAnthropic, "Anthropic", "https://api.anthropic.com/v1", ai.KindAnthropic, "", true, false},
		{core.ProviderTypeSafe, "TypeSafe", "https://api.typesafe.ai/v1", "", "systemone", false, true},
		{core.ProviderOther, "", "", ai.KindOpenAI, "decisions", true, false}, // judge starts off for other
	} {
		f := newForm(c.p)
		if f.name != c.name || f.baseURL != c.url || f.kind != c.kind || f.judgeFormat != c.judge || f.genOn != c.genOn || f.judgeOn != c.judgeOn {
			t.Errorf("%s: %+v", c.p, f)
		}
		if c.judge != "" && f.judgeModel != ai.JudgeFormat(c.judge).Models()[0] {
			t.Errorf("%s: judge model %q", c.p, f.judgeModel)
		}
		if f.canResetURL() {
			t.Errorf("%s: 恢复默认 shows on the default address", c.p)
		}
	}
	f := newForm(core.ProviderAnthropic)
	f.baseURL = "https://api.anthropic.com" // as older Godwits stored it
	if f.canResetURL() {
		t.Error("恢复默认 shows for Anthropic's address without /v1")
	}
	f.baseURL = "https://proxy.example.com"
	if !f.canResetURL() {
		t.Error("no 恢复默认 for a changed address")
	}
}

// TestModelFormNameFollowsURL: other's name follows the address's host (or a
// preset's name) until the user types one.
func TestModelFormNameFollowsURL(t *testing.T) {
	f := newForm(core.ProviderOther)
	f.baseURL = "https://openrouter.ai/api/v1"
	f.follow()
	if f.name != "OpenRouter" {
		t.Fatalf("name %q", f.name)
	}
	f.baseURL = "https://llm.example.com/v1"
	f.follow()
	if f.name != "llm.example.com" {
		t.Fatalf("name %q", f.name)
	}
	f.pickGateway("LiteLLM", "http://127.0.0.1:4000")
	if f.name != "LiteLLM" || f.baseURL != "http://127.0.0.1:4000" {
		t.Fatalf("preset: %q %q", f.name, f.baseURL)
	}
	f.baseURL = "http://localhost:8080/v1"
	f.follow()
	if f.name != "本地网关" {
		t.Fatalf("name %q", f.name)
	}
	f.name = "mine"
	f.pickGateway("OpenRouter", "https://openrouter.ai/api/v1")
	f.baseURL = "https://other.example.com"
	f.follow()
	if f.name != "mine" || f.nameAuto {
		t.Fatalf("a typed name changed: %q", f.name)
	}
	p := newForm(core.ProviderOpenAI)
	p.baseURL = "https://openrouter.ai/api/v1"
	p.follow()
	if p.name != "OpenAI" {
		t.Fatalf("a provider's name followed the address: %q", p.name)
	}
}

// TestModelFormDecList: no judge models until there is a key, except for
// other, which lists its format's; a fetched list wins for a provider.
func TestModelFormDecList(t *testing.T) {
	f := newForm(core.ProviderTypeSafe)
	if l := f.decList(); l != nil {
		t.Fatalf("without a key: %q", l)
	}
	f.key = "k"
	if l := f.decList(); !slices.Equal(l, []string{"jev-latest", "jev-preview"}) {
		t.Fatalf("keyed, unfetched: %q", l)
	}
	f.judgeModels = []string{"jev-latest", "jev-preview", "jev-1.13.0"}
	if l := f.decList(); len(l) != 3 {
		t.Fatalf("fetched: %q", l)
	}
	f.key, f.id, f.hasKey, f.judgeModel = "", 1, true, "jev-old" // a stored key; a saved model no longer listed stays
	if l := f.decList(); len(l) != 4 || l[0] != "jev-old" {
		t.Fatalf("edit: %q", l)
	}
	o := newForm(core.ProviderOther) // no key: its format's own list, never a gateway's /models
	o.judgeFormat, o.judgeModel, o.judgeModels = "systemone", "jev-latest", []string{"gateway-chat-model"}
	if l := o.decList(); !slices.Equal(l, ai.FormatSystemOne.Models()) {
		t.Fatalf("other: %q", l)
	}
}

// TestModelFormStatusClears: a test's lines go when the address, key or a
// format changes, not when a capability is switched or the name edited.
func TestModelFormStatusClears(t *testing.T) {
	tested := func() *modelForm {
		f := newForm(core.ProviderOther)
		f.baseURL, f.judgeOn = "https://gw.example.com/v1", true
		f.edited()
		f.gen, f.judge = capTest{"ok", "a"}, capTest{"fail", "b"}
		return f
	}
	for _, c := range []struct {
		name  string
		edit  func(f *modelForm)
		clear bool
	}{
		{"address", func(f *modelForm) { f.baseURL = "https://gw2.example.com/v1" }, true},
		{"key", func(f *modelForm) { f.key = "k" }, true},
		{"generate format", func(f *modelForm) { f.kind = ai.KindAnthropic }, true},
		{"judge format", func(f *modelForm) { f.judgeFormat = "systemone" }, true},
		{"name", func(f *modelForm) { f.name = "x" }, false},
		{"switch", func(f *modelForm) { f.genOn = false }, false},
	} {
		f := tested()
		c.edit(f)
		f.edited()
		if cleared := f.gen.state == "" && f.judge.state == ""; cleared != c.clear {
			t.Errorf("%s: gen %+v judge %+v", c.name, f.gen, f.judge)
		}
	}
}

// TestModelFormOffWarns: turning off what the defaults use warns, naming
// where generation moves (the same alt dropRefs applies) or that nothing
// else generates.
func TestModelFormOffWarns(t *testing.T) {
	svcs := []core.ModelService{
		{ID: 1, Name: "OpenAI", Slug: "openai", Provider: core.ProviderOpenAI, Kind: ai.KindResponses, Models: []string{"a"}, JudgeFormat: "decisions", JudgeModel: "gpt-6-luna"},
		{ID: 2, Name: "TypeSafe", Slug: "typesafe", Provider: core.ProviderTypeSafe, JudgeFormat: "systemone", JudgeModel: "jev-latest"},
		{ID: 3, Name: "Anthropic", Slug: "anthropic", Provider: core.ProviderAnthropic, Kind: ai.KindAnthropic, Models: []string{"[MODEL D]"}},
	}
	st := core.Settings{DefaultModel: "openai/a", JudgeService: "openai"}
	m := &modelsView{}
	m.edit(svcs[0])
	f := m.form
	if g, j := offWarns(f, st, svcs); g != "" || j != "" {
		t.Fatalf("on: %q %q", g, j)
	}
	f.genOn, f.judgeOn = false, false
	g, j := offWarns(f, st, svcs)
	if g != "它是默认生成模型。关闭后，总结、翻译、写信和 Wit 改用 Anthropic / [MODEL D]。" || j != "它是默认判断模型。关闭后，判断改由生成模型兼任，会慢一些。" {
		t.Fatalf("off: %q %q", g, j)
	}
	if got, _ := dropRefs(st, core.ModelService{ID: 1, Slug: "openai"}, svcs); got.DefaultModel != "anthropic/[MODEL D]" {
		t.Fatalf("the save moves generation to %q, not the warning's", got.DefaultModel)
	}
	if g, _ := offWarns(f, st, svcs[:2]); g != "它是唯一能生成的服务，关闭后总结、翻译、写信和 Wit 都不可用。" {
		t.Fatalf("no alt: %q", g)
	}
	if g, j := offWarns(f, core.Settings{DefaultModel: "anthropic/[MODEL D]", JudgeService: "typesafe"}, svcs); g != "" || j != "" {
		t.Fatalf("not the default: %q %q", g, j)
	}
	n := newForm(core.ProviderOpenAI)
	n.genOn = false
	if g, _ := offWarns(n, st, svcs); g != "" {
		t.Fatalf("a new service warns: %q", g)
	}
}

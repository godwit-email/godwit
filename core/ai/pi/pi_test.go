package pi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testllm"
)

// The bundle's real provider code (pi-ai and the OpenAI SDK) runs on moejs
// against the fake service: this is the smoke test a moejs or pi upgrade
// must pass.

func generate(t *testing.T, ctx context.Context, srv *testllm.Server, req Request) (Result, error, []string) {
	t.Helper()
	req.BaseURL = srv.URL
	if req.Model == "" {
		req.Model = "m"
	}
	if req.API == "" {
		req.API = "openai-completions"
	}
	var got []string
	res, err := Generate(ctx, http.DefaultClient, req, func(kind, text string) { got = append(got, kind+":"+text) })
	return res, err, got
}

func TestGenerateStreamsTheReply(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Think: []string{"thinking"}, Text: []string{"Hello, ", "世界"}, Usage: [2]int{12, 3}, Cached: 8})
	res, err, got := generate(t, t.Context(), srv, Request{APIKey: "sk-test", System: "Be brief.", Prompt: "Summarize this mail.", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"reasoning:thinking", "text:Hello, ", "text:世界"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events %q, want %q", got, want)
	}
	if res.StopReason != "stop" || res.Input != 12 /* the cached 8 too */ || res.Output != 3 || res.Status != 200 {
		t.Errorf("result %+v", res)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Invalid != "" {
		t.Fatalf("requests %+v", reqs)
	}
	if h := reqs[0].Header.Get("Authorization"); h != "Bearer sk-test" {
		t.Errorf("Authorization %q", h)
	}
	if string(reqs[0].System) != `"Be brief."` || !strings.Contains(string(reqs[0].Messages), "Summarize this mail.") {
		t.Errorf("system %s, messages %s", reqs[0].System, reqs[0].Messages)
	}
	var body map[string]any
	json.Unmarshal(reqs[0].Body, &body)
	if body["model"] != "m" || body["stream"] != true || body["reasoning_effort"] != "low" {
		t.Errorf("body %s", reqs[0].Body)
	}
	if _, ok := body["store"]; ok {
		t.Errorf("store sent: %s", reqs[0].Body)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Errorf("max_tokens sent: %s", reqs[0].Body)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Errorf("max_completion_tokens sent: %s", reqs[0].Body)
	}
}

func TestGenerateReportsTheStatus(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Status: http.StatusUnauthorized})
	res, err, _ := generate(t, t.Context(), srv, Request{APIKey: "sk-secret", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "error" || res.Status != http.StatusUnauthorized {
		t.Errorf("result %+v", res)
	}
}

func TestGenerateReportsACutOffReply(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"cut"}, Finish: "length"})
	res, err, _ := generate(t, t.Context(), srv, Request{APIKey: "k", Prompt: "hi"})
	if err != nil || res.StopReason != "length" {
		t.Errorf("result %+v, %v", res, err)
	}
}

func TestGenerateReportsAnUnreachableService(t *testing.T) {
	res, err := Generate(t.Context(), http.DefaultClient, Request{API: "openai-completions", BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "k", Prompt: "hi"}, func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "error" || res.Transport == nil || res.Status != 0 {
		t.Errorf("result %+v", res)
	}
}

func TestGenerateEndsWithItsContext(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
	ctx, cancel := context.WithCancel(t.Context())
	start := time.Now()
	_, err := Generate(ctx, http.DefaultClient, Request{API: "openai-completions", BaseURL: srv.URL, Model: "m", APIKey: "k", Prompt: "hi"}, func(kind, text string) {
		if text == "partial" {
			cancel()
		}
	})
	if err != context.Canceled {
		t.Errorf("err %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestCompleteUTF8(t *testing.T) {
	s := []byte("a世") // 世 is 3 bytes
	for n, want := range map[int]int{1: 1, 2: 1, 3: 1, 4: 4} {
		if got := completeUTF8(s[:n]); got != want {
			t.Errorf("completeUTF8(%q) = %d, want %d", s[:n], got, want)
		}
	}
}

func sentBody(t *testing.T, r testllm.Request) map[string]any {
	t.Helper()
	if r.Invalid != "" {
		t.Fatalf("the fake rejected the request: %s", r.Invalid)
	}
	var b map[string]any
	if err := json.Unmarshal(r.Body, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGenerateAnthropic(t *testing.T) {
	srv := testllm.Start(t, "anthropic")
	srv.Script(testllm.Turn{Think: []string{"hmm"}, Text: []string{"Hi"}})
	res, err, got := generate(t, t.Context(), srv, Request{API: "anthropic-messages", APIKey: "sk-ant", System: "Be brief.", Prompt: "Summarize.",
		Effort: "low", ThinkingBudget: 2048, MaxTokens: 2048 + 4096})
	if err != nil {
		t.Fatal(err)
	}
	if want := "reasoning:hmm|text:Hi"; strings.Join(got, "|") != want {
		t.Errorf("events %q, want %q", got, want)
	}
	if res.StopReason != "stop" || res.Input != 1 || res.Output != 1 {
		t.Errorf("result %+v", res)
	}
	r := srv.Requests()[0]
	b := sentBody(t, r)
	if r.Header.Get("x-api-key") != "sk-ant" || r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") != "Godwit" {
		t.Errorf("headers %v", r.Header)
	}
	if h := r.Header.Get("anthropic-beta"); h != "" {
		t.Errorf("anthropic-beta %q: Genkit asks for no beta", h)
	}
	if b["max_tokens"] != float64(6144) || !strings.Contains(string(r.System), "Be brief.") || strings.Contains(string(r.Body), "cache_control") {
		t.Errorf("body %s", r.Body)
	}
	if th, _ := b["thinking"].(map[string]any); th["type"] != "enabled" || th["budget_tokens"] != float64(2048) || th["display"] != nil {
		t.Errorf("thinking %v", b["thinking"])
	}

	// No effort: no thinking, and Genkit's default max_tokens.
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	if _, err, _ := generate(t, t.Context(), srv, Request{API: "anthropic-messages", APIKey: "k", Prompt: "hi", MaxTokens: 4096}); err != nil {
		t.Fatal(err)
	}
	if b := sentBody(t, srv.Requests()[1]); b["thinking"] != nil || b["max_tokens"] != float64(4096) {
		t.Errorf("body %s", srv.Requests()[1].Body)
	}
}

// A keyless service is a local gateway: no credential goes out, not even one
// the environment holds.
func TestGenerateKeylessSendsNoCredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("OPENAI_API_KEY", "env-key")
	for kind, api := range map[string]string{"anthropic": "anthropic-messages", "openai": "openai-completions", "openai-responses": "openai-responses"} {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		res, err, _ := generate(t, t.Context(), srv, Request{API: api, Prompt: "hi", MaxTokens: 4096})
		if err != nil || res.StopReason != "stop" {
			t.Fatalf("%s: %+v, %v", api, res, err)
		}
		h := srv.Requests()[0].Header
		if h.Get("x-api-key") != "" || h.Get("Authorization") != "" {
			t.Errorf("%s sent a credential: %v", api, h)
		}
	}
}

func TestGenerateResponses(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Reasoning: "enc-1", Think: []string{"plan"}, Text: []string{"Hello"}, Usage: [2]int{7, 3}})
	res, err, got := generate(t, t.Context(), srv, Request{API: "openai-responses", APIKey: "sk-1", System: "Be brief.", Prompt: "Summarize.", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "reasoning:plan|text:Hello"; strings.Join(got, "|") != want {
		t.Errorf("events %q, want %q", got, want)
	}
	if res.StopReason != "stop" || res.Input != 7 || res.Output != 3 {
		t.Errorf("result %+v", res)
	}
	r := srv.Requests()[0]
	b := sentBody(t, r) // the fake checks store: false and the encrypted reasoning asked for
	if string(r.System) != `"Be brief."` || r.Header.Get("Authorization") != "Bearer sk-1" {
		t.Errorf("instructions %s, headers %v", r.System, r.Header)
	}
	if rs, _ := b["reasoning"].(map[string]any); rs["effort"] != "high" || rs["summary"] != "auto" {
		t.Errorf("reasoning %v", b["reasoning"])
	}
	if strings.Contains(string(r.Messages), "Be brief.") {
		t.Errorf("the system prompt is in the input too: %s", r.Messages)
	}

	// No effort: no reasoning params, the encrypted reasoning still asked for.
	srv = testllm.Start(t, "openai-responses") // a fresh fake: this one wants rs_0 sent back, as a conversation would
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	if _, err, _ := generate(t, t.Context(), srv, Request{API: "openai-responses", APIKey: "sk-1", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if b := sentBody(t, srv.Requests()[0]); b["reasoning"] != nil {
		t.Errorf("body %s", srv.Requests()[0].Body)
	}
}

func TestGenerateResponsesEnds(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	for _, c := range []struct {
		turn       testllm.Turn
		stop, code string
	}{
		{testllm.Turn{Text: []string{"partial"}, Incomplete: true}, "length", ""},
		{testllm.Turn{Refusal: "no"}, "blocked", ""},
		{testllm.Turn{FailCode: "subscription_sharing_usage_limit_exceeded"}, "error", "subscription_sharing_usage_limit_exceeded"},
		{testllm.Turn{Status: http.StatusForbidden, Code: "subscription_sharing_user_not_eligible"}, "error", "subscription_sharing_user_not_eligible"},
	} {
		srv.Script(c.turn)
		res, err, got := generate(t, t.Context(), srv, Request{API: "openai-responses", APIKey: "tok", Prompt: "hi"})
		srv.Advance()
		if err != nil || res.StopReason != c.stop || res.Code != c.code {
			t.Errorf("%+v: result %+v, %v", c.turn, res, err)
		}
		if c.stop == "blocked" && len(got) > 0 {
			t.Errorf("a refusal was shown: %q", got)
		}
	}
}

func TestGenerateCodex(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Text: []string{"OK"}})
	// The account ID's claim, in base64url with no padding, as a real token carries it.
	tok := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct~~"}}`)) + ".s"
	codex := Request{API: "openai-codex-responses", BaseURL: srv.URL + "/backend-api/codex", Model: "m", APIKey: tok, Prompt: "hi", Effort: "low"}
	res, err := Generate(t.Context(), http.DefaultClient, codex, func(string, string) {})
	if err != nil || res.StopReason != "stop" {
		t.Fatalf("result %+v, %v", res, err)
	}
	r := srv.Requests()[0]
	b := sentBody(t, r)
	if r.Path != "/backend-api/codex/responses" || r.Header.Get("chatgpt-account-id") != "acct~~" || r.Header.Get("originator") != "godwit" ||
		r.Header.Get("OpenAI-Beta") != "responses=experimental" || r.Header.Get("Authorization") != "Bearer "+tok {
		t.Errorf("path %s, headers %v", r.Path, r.Header)
	}
	if b["instructions"] != "You are a helpful assistant." || b["text"] != nil {
		t.Errorf("body %s", r.Body)
	}
	if rs, _ := b["reasoning"].(map[string]any); rs["effort"] != "low" || rs["summary"] != "auto" {
		t.Errorf("reasoning %v", b["reasoning"])
	}

	srv.Script(testllm.Turn{Status: http.StatusBadRequest, Code: "usage_not_included"})
	res, err = Generate(t.Context(), http.DefaultClient, codex, func(string, string) {})
	if err != nil || res.StopReason != "error" || res.Code != "usage_not_included" || res.Status != http.StatusBadRequest {
		t.Errorf("result %+v, %v", res, err)
	}
}

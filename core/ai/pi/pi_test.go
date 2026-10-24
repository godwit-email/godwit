package pi

import (
	"context"
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
	var got []string
	res, err := Generate(ctx, http.DefaultClient, req, func(kind, text string) { got = append(got, kind+":"+text) })
	return res, err, got
}

func TestGenerateStreamsTheReply(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Think: []string{"thinking"}, Text: []string{"Hello, ", "世界"}, Usage: [2]int{12, 3}})
	res, err, got := generate(t, t.Context(), srv, Request{APIKey: "sk-test", System: "Be brief.", Prompt: "Summarize this mail.", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"reasoning:thinking", "text:Hello, ", "text:世界"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events %q, want %q", got, want)
	}
	if res.StopReason != "stop" || res.Input != 12 || res.Output != 3 || res.Status != 200 {
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
	res, err := Generate(t.Context(), http.DefaultClient, Request{BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "k", Prompt: "hi"}, func(string, string) {})
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
	_, err := Generate(ctx, http.DefaultClient, Request{BaseURL: srv.URL, Model: "m", APIKey: "k", Prompt: "hi"}, func(kind, text string) {
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

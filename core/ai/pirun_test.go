package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/internal/testllm"
)

// ranOnPi: pi-ai identifies itself as Godwit; openai-go, on the Genkit path, does not.
func ranOnPi(t *testing.T, srv *testllm.Server) bool {
	t.Helper()
	reqs := srv.Requests()
	require.NotEmpty(t, reqs)
	return reqs[len(reqs)-1].Header.Get("User-Agent") == "Godwit"
}

// codexToken is a ChatGPT access token whose claims, in base64url, hold "-".
var codexToken = "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct~~"}}`)) + ".s"

func codexModel(srv *testllm.Server) ResolvedModel {
	m := modelFor(srv, KindResponses, codexToken)
	m.BaseURL = srv.URL + "/backend-api/codex"
	return m
}

func TestPiRunsAOneOff(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Think: []string{"hmm"}, Text: []string{"Fri", "day"}, Usage: [2]int{9, 2}})
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), System: "s", Prompt: "p", Pi: true, Reasoning: true})
	require.Nil(t, res.Err)
	assert.Equal(t, []Event{{Kind: "reasoning", Text: "hmm"}, {Kind: "text", Text: "Fri"}, {Kind: "text", Text: "day"}, {Kind: "done"}}, evs)
	assert.Equal(t, Usage{Input: 9, Output: 2, Last: 9}, res.Usage)
	assert.True(t, ranOnPi(t, srv))
}

// Every service kind, with and without a key (a local gateway), and both
// subscriptions: ChatGPT's Codex backend and a Responses service signed in to.
func TestPiRunsAOneOffOfEveryKind(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("OPENAI_API_KEY", "env-key")
	e := newEngine(t)
	for _, kind := range kinds {
		for _, key := range []string{"k", ""} {
			srv := testllm.Start(t, kind)
			srv.Script(testllm.Turn{Text: []string{"ok"}})
			evs, res := run(e, Request{Model: modelFor(srv, kind, key), System: "s", Prompt: "p", Pi: true})
			require.Nil(t, res.Err, kind)
			assert.Equal(t, []Event{{Kind: "text", Text: "ok"}, {Kind: "done"}}, evs, kind)
			assert.True(t, ranOnPi(t, srv), kind)
			r := srv.Requests()[0]
			require.Empty(t, r.Invalid, kind)
			assert.Contains(t, string(r.System), `"s"`, kind)
			if key == "" {
				assert.Empty(t, r.Header.Get("Authorization"), kind)
				assert.Empty(t, r.Header.Get("x-api-key"), kind)
			}
		}
	}
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	_, res := run(e, Request{Model: codexModel(srv), Prompt: "p", Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	r := srv.Requests()[0]
	assert.Equal(t, "/backend-api/codex/responses", r.Path)
	assert.Equal(t, "acct~~", r.Header.Get("chatgpt-account-id"))
	assert.Equal(t, "godwit", r.Header.Get("originator"))
	assert.Contains(t, string(r.Body), `"instructions":"You are a helpful assistant."`)
}

// effortConfig's mapping, per kind.
func TestPiAsksForTheEffort(t *testing.T) {
	e := newEngine(t)
	for kind, want := range map[string]string{
		"openai":      `"reasoning_effort":"medium"`,
		"anthropic":   `"max_tokens":12288`,
		KindResponses: `"reasoning":{"effort":"medium","summary":"auto"}`,
	} {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		_, res := run(e, Request{Model: modelFor(srv, kind, "k"), Prompt: "p", Pi: true, Effort: "medium"})
		require.Nil(t, res.Err, kind)
		body := string(srv.Requests()[0].Body)
		assert.Contains(t, body, want, kind)
		if kind == "anthropic" {
			assert.Contains(t, body, `"thinking":{"type":"enabled","budget_tokens":8192}`)
		}
	}
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	_, res := run(e, Request{Model: codexModel(srv), Prompt: "p", Pi: true, Effort: "high"})
	require.Nil(t, res.Err)
	assert.Contains(t, string(srv.Requests()[0].Body), `"reasoning":{"effort":"high","summary":"auto"}`)

	// A model that does not reason refuses the effort, as on Genkit.
	srv = testllm.Start(t, "anthropic")
	srv.Script(testllm.Turn{Status: http.StatusBadRequest})
	_, res = run(e, Request{Model: modelFor(srv, "anthropic", "k"), Prompt: "p", Pi: true, Effort: "low"})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeBadResponse, res.Err.Code)
}

func TestPiLeavesTheRestToGenkit(t *testing.T) {
	srv := testllm.Start(t, "openai")
	e := newEngine(t)
	for name, req := range map[string]Request{
		"off": {Model: modelFor(srv, "openai", "k"), Prompt: "p"},
		"tools": {Model: modelFor(srv, "openai", "k"), Prompt: "p", Pi: true,
			Tools: []ToolSpec{{Name: "t", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (any, error) { return "x", nil }}}},
		"plugin":  {Model: ResolvedModel{Kind: "openai", BaseURL: srv.URL, Model: "m-1", APIKey: "k", Client: http.DefaultClient}, Prompt: "p", Pi: true},
		"history": {Model: modelFor(srv, "openai", "k"), History: asking(nil, "p"), Pi: true},
	} {
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		_, res := run(e, req)
		require.Nil(t, res.Err, name)
		assert.False(t, ranOnPi(t, srv), name)
	}
}

func TestPiErrorsReadAsGenkitsDo(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Status: http.StatusUnauthorized})
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "sk-secret"), Prompt: "p", Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeUnauthorized, res.Err.Code)
	assert.Equal(t, "error", evs[len(evs)-1].Kind)
	assert.NotContains(t, res.Err.Msg, "sk-secret")

	srv.Script(testllm.Turn{Text: []string{"cut"}, Finish: "length"})
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), Prompt: "p", Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeIncomplete, res.Err.Code)

	_, res = run(newEngine(t), Request{Model: ResolvedModel{ServiceID: 1, Version: 1, Kind: "openai", BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "k"}, Prompt: "p", Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeUnavailable, res.Err.Code)
}

// The subscription codes respError reads, with its Details.
func TestPiSubscriptionErrorsReadAsGenkitsDo(t *testing.T) {
	e := newEngine(t)
	srv := testllm.Start(t, KindResponses)
	usage := func(name string, m ResolvedModel, turn testllm.Turn) {
		t.Helper()
		srv.Script(turn)
		evs, res := run(e, Request{Model: m, Prompt: "p", Pi: true})
		srv.Advance()
		require.NotNil(t, res.Err, name)
		assert.Equal(t, CodeRateLimited, res.Err.Code, name)
		assert.Equal(t, DetailChatGPTUsage, res.Err.Detail, name)
		assert.NotContains(t, res.Err.Msg, "UPSTREAM-BODY", name)
		assert.Equal(t, "error", last(t, evs).Kind, name)
		assert.True(t, ranOnPi(t, srv), name)
	}
	usage("codex 429", codexModel(srv), testllm.Turn{Status: http.StatusTooManyRequests})
	usage("codex usage_not_included", codexModel(srv), testllm.Turn{Status: http.StatusBadRequest, Code: "usage_not_included"})
	usage("failed limit", modelFor(srv, KindResponses, "tok"), testllm.Turn{FailCode: "subscription_sharing_usage_limit_exceeded"})
	usage("failed unavailable", modelFor(srv, KindResponses, "tok"), testllm.Turn{FailCode: "subscription_sharing_usage_unavailable", Fail: "UPSTREAM-BODY quota out"})
	usage("HTTP limit", modelFor(srv, KindResponses, "tok"), testllm.Turn{Status: http.StatusTooManyRequests, Code: "subscription_sharing_usage_limit_exceeded"})

	srv.Script(testllm.Turn{Status: http.StatusForbidden, Code: "subscription_sharing_user_not_eligible"})
	_, res := run(e, Request{Model: modelFor(srv, KindResponses, "tok-secret"), Prompt: "p", Pi: true})
	srv.Advance()
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeUnauthorized, res.Err.Code)
	assert.Equal(t, DetailChatGPTEligibility, res.Err.Detail)
	assert.NotContains(t, res.Err.Msg, "tok-secret")

	// Not the Codex backend: a 429 is an ordinary rate limit, with no plan to manage.
	srv.Script(testllm.Turn{Status: http.StatusTooManyRequests, Code: "rate_limit_exceeded"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "sk-1"), Prompt: "p", Pi: true})
	srv.Advance()
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeRateLimited, res.Err.Code)
	assert.Empty(t, res.Err.Detail)

	srv.Script(testllm.Turn{Fail: "x"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "tok"), Prompt: "p", Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeBadResponse, res.Err.Code)
	assert.Empty(t, res.Err.Detail)
}

// A refusal, a filtered reply and one cut off end as on Genkit.
func TestPiResponsesEndsReadAsGenkitsDo(t *testing.T) {
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"half"}, Incomplete: true})
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), Prompt: "p", Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, "the model's reply was cut off at its output limit", res.Err.Msg)

	srv.Script(testllm.Turn{Refusal: "I can't help with that."})
	filtered := sse(t, []string{
		ev("response.output_item.done", `{"output_index":0,"item":{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"no"}]}}`),
		ev("response.completed", `{"response":{"id":"r","status":"completed","output":[]}}`),
	}, []string{
		ev("response.incomplete", `{"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`),
	})
	for name, url := range map[string]string{"refusal events": srv.URL, "refusal content": filtered, "content filter": filtered} {
		evs, res := run(newEngine(t), Request{Model: respModel(url, "m1"), Prompt: "p", Pi: true})
		require.NotNil(t, res.Err, name)
		assert.Equal(t, CodeIncomplete, res.Err.Code, name)
		assert.Equal(t, "the model service withheld the reply", res.Err.Msg, name)
		assert.Empty(t, kindsOf(evs, "text"), name)
	}
}

func TestPiRunOfAChangedServiceIsStale(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		e := newEngine(t)
		e.Current = func(context.Context, int64) (int64, error) { return 2, nil }
		_, res := run(e, Request{Model: modelFor(srv, kind, "k"), Prompt: "p", Pi: true})
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeStale, res.Err.Code)
	})
}

func TestPiRunEndsWhenItsServiceIsForgotten(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
		e := newEngine(t)
		events, wait := e.Run(t.Context(), Request{Model: modelFor(srv, kind, "k"), Prompt: "p", Pi: true})
		assert.Equal(t, Event{Kind: "text", Text: "partial"}, <-events)
		e.Forget(1)
		_, res := collect(events, wait)
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeStale, res.Err.Code)
	})
}

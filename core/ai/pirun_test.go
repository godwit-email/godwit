package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync/atomic"
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
		"a Genkit history": {Model: modelFor(srv, "openai", "k"), Pi: true, // a new one goes on pi (#232)
			History: asking(must(LoadHistory([]StoredMessage{{Seq: 1, Data: []byte(`{"role":"user","content":[{"text":"hi"}]}`)}}, 2)), "p")},
	} {
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		_, res := run(e, req)
		require.Nil(t, res.Err, name)
		assert.False(t, ranOnPi(t, srv), name)
	}
}

// roundTrip is an http.RoundTripper of a func, as a plugin model's client has.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A plugin's model goes out through its own client, which prepares each
// request; it has no key, so none is sent.
func TestPiRunsAPluginModel(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("X-Plugin-Key", "secret")
		return http.DefaultTransport.RoundTrip(r)
	})}
	_, res := run(newEngine(t), Request{Model: ResolvedModel{ServiceID: -5, Kind: "openai", BaseURL: srv.URL, Model: "m-1", Client: c}, Prompt: "p", Pi: true})
	require.Nil(t, res.Err)
	require.True(t, ranOnPi(t, srv))
	h := srv.Requests()[0].Header
	assert.Equal(t, "secret", h.Get("X-Plugin-Key"))
	assert.Empty(t, h.Get("Authorization"))
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

var piSearch = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []any{"q"}}

func TestPiRunsAConversationWithReadTools(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"invoice"}`}}, Usage: [2]int{10, 2}},
		testllm.Turn{Think: []string{"ok"}, Text: []string{"It is ", "here."}, Usage: [2]int{30, 4}},
	)
	var callID string
	search := ToolSpec{Name: "search", Schema: piSearch,
		Gate: func(context.Context, json.RawMessage) (Decision, error) { return Allow, nil },
		Run: func(ctx context.Context, args json.RawMessage) (any, error) {
			callID = CallID(ctx)
			return map[string]any{"args": json.RawMessage(args), "id": json.Number("9007199254740993")}, nil
		}}
	sent := must(NewHistory().Send(1<<20, Summary{}, false)).Ask("Where is it?", "[quoted mail]")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), System: "s", History: sent, Tools: []ToolSpec{search}, Pi: true, Reasoning: true,
		Args: func(_ context.Context, a json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"q":"invoice OR bill"}`), nil
		}})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	assert.Equal(t, "c1", callID)
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	assert.Equal(t, []string{"tool_call", "tool_result", "reasoning", "text", "text", "done"}, kinds)
	assert.JSONEq(t, `{"q":"invoice OR bill"}`, string(evs[0].Call.Args))
	assert.JSONEq(t, `{"args":{"q":"invoice OR bill"},"id":9007199254740993}`, string(evs[1].Result.Output))
	assert.Equal(t, Usage{Input: 40, Output: 6, Last: 30}, res.Usage)
	assert.Contains(t, string(srv.Requests()[0].Messages), "[quoted mail]")

	h, ch, err := sent.Restore(res)
	require.NoError(t, err)
	require.Len(t, ch.Put, 4) // the question, the call, its result, the answer
	for _, p := range ch.Put {
		assert.True(t, isPiMessage(p.Data), string(p.Data))
	}
	assert.Contains(t, string(ch.Put[1].Data), `"q":"invoice"`) // what the model wrote
	h, err = LoadHistory(ch.Put, ch.Next)
	require.NoError(t, err)
	assert.True(t, h.Pi())
	msgs := h.Messages()
	require.Len(t, msgs, 4)
	assert.Equal(t, "Where is it?", msgs[0].Text) // the context stays out of sight
	assert.Equal(t, "c1", msgs[1].Calls[0].ID)
	assert.JSONEq(t, `{"args":{"q":"invoice OR bill"},"id":9007199254740993}`, string(msgs[2].Results[0].Output))
	assert.Equal(t, "It is here.", msgs[3].Text)
	assert.Equal(t, "ok", msgs[3].Reasoning)
	assert.Equal(t, []string{"[quoted mail]"}, must(h.Send(1<<20, Summary{}, false)).Contexts())

	// The next turn goes on pi too, the earlier one sent as pi saved it.
	srv.Script(testllm.Turn{Text: []string{"Again."}})
	next := must(h.Send(1<<20, Summary{}, false)).Ask("And now?", "")
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: next, Tools: []ToolSpec{search}, Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	last := srv.Requests()[len(srv.Requests())-1]
	assert.Contains(t, string(last.Messages), `"tool_call_id":"c1"`)
	h2, ch, err := next.Restore(res)
	require.NoError(t, err)
	assert.Len(t, ch.Put, 2)
	assert.Len(t, h2.Turns(), 2)
}

func TestPiAnswersAtTheToolCallLimit(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"a"}`}}},
		testllm.Turn{Calls: []testllm.Call{{ID: "c2", Name: "search", Args: `{"q":"b"}`}}},
		testllm.Turn{Text: []string{"What I found."}},
	)
	search := ToolSpec{Name: "search", Schema: piSearch, Run: func(context.Context, json.RawMessage) (any, error) { return "x", nil }}
	sent := asking(nil, "q")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: []ToolSpec{search}, MaxTurns: 1, Pi: true})
	require.Nil(t, res.Err)
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	assert.Contains(t, string(srv.Requests()[2].System), "tool-call limit")
	_, ch, err := sent.Restore(res)
	require.NoError(t, err)
	assert.Len(t, ch.Put, 4) // the refused round is gone

	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"a"}`}}},
		testllm.Turn{Calls: []testllm.Call{{ID: "c2", Name: "search", Args: `{"q":"b"}`}}},
		testllm.Turn{Calls: []testllm.Call{{ID: "c3", Name: "search", Args: `{"q":"c"}`}}},
	)
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "q"), Tools: []ToolSpec{search}, MaxTurns: 1, Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, errLimit(), res.Err)
}

// $defs in a plugin or MCP tool's schema are inlined: an Anthropic-compatible
// service refuses them.
func TestPiFlattensToolSchemas(t *testing.T) {
	srv := testllm.Start(t, "anthropic")
	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "send", Args: `{"to":"bob@x"}`}}},
		testllm.Turn{Text: []string{"ok"}},
	)
	schema := map[string]any{"type": "object", "$defs": map[string]any{"addr": map[string]any{"type": "string"}},
		"properties": map[string]any{"to": map[string]any{"$ref": "#/$defs/addr"}}}
	var got json.RawMessage
	tool := ToolSpec{Name: "send", Schema: schema, Run: func(_ context.Context, args json.RawMessage) (any, error) { got = args; return "sent", nil }}
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "anthropic", "k"), History: asking(nil, "q"), Tools: []ToolSpec{tool}, Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	assert.JSONEq(t, `{"to":"bob@x"}`, string(got))
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	assert.Empty(t, reqs[0].Invalid)
	tools := string(reqs[0].Tools)
	assert.NotContains(t, tools, "$defs")
	assert.NotContains(t, tools, "$ref")
	assert.Contains(t, tools, `"to":{"type":"string"}`)
}

func TestPiRefusesACallThatNeedsApproval(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "fetch", Args: `{}`}}},
		testllm.Turn{Text: []string{"ok"}},
	)
	tool := ToolSpec{Name: "fetch", Gate: func(context.Context, json.RawMessage) (Decision, error) { return Approve, nil },
		Run: func(context.Context, json.RawMessage) (any, error) { t.Error("ran"); return nil, nil }}
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "q"), Tools: []ToolSpec{tool}, Pi: true})
	require.Nil(t, res.Err)
	assert.Equal(t, errNeedsApproval.Error(), evs[1].Result.Error)
}

func TestPiCancelStopsTheModelAndTools(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"a"}`}}})
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	tool := ToolSpec{Name: "search", Schema: piSearch, Run: func(ctx context.Context, _ json.RawMessage) (any, error) {
		cancel()
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}}
	_, res := collect(newEngine(t).Run(ctx, Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "q"), Tools: []ToolSpec{tool}, Pi: true}))
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeCanceled, res.Err.Code)
	<-stopped
}

func TestGenkitHistoriesStayOnGenkit(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"one"}})
	sent := asking(nil, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent}) // the switch off
	require.Nil(t, res.Err)
	h, _, err := sent.Restore(res)
	require.NoError(t, err)
	assert.False(t, h.Pi())

	srv.Script(testllm.Turn{Text: []string{"two"}})
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(h, "second"), Pi: true})
	require.Nil(t, res.Err)
	assert.False(t, ranOnPi(t, srv))
}

func TestPiHistoryRunsOnGenkitWithTheSwitchOff(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"one"}})
	sent := asking(nil, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Pi: true})
	require.Nil(t, res.Err)
	h, _, err := sent.Restore(res)
	require.NoError(t, err)

	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"a"}`}}},
		testllm.Turn{Text: []string{"two"}},
	)
	search := ToolSpec{Name: "search", Schema: piSearch, Run: func(context.Context, json.RawMessage) (any, error) { return "x", nil }}
	next := asking(h, "second")
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: next, Tools: []ToolSpec{search}})
	require.Nil(t, res.Err)
	assert.False(t, ranOnPi(t, srv))
	h, ch, err := next.Restore(res)
	require.NoError(t, err)
	for _, p := range ch.Put {
		assert.True(t, isPiMessage(p.Data), string(p.Data))
	}
	h, err = LoadHistory(must(h.Rows()), h.Next())
	require.NoError(t, err)
	assert.Len(t, h.Turns(), 2)
	assert.Equal(t, "two", h.Messages()[len(h.Messages())-1].Text)
}

func TestPiConversationOnResponses(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.Script(
		testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"a"}`}}},
		testllm.Turn{Text: []string{"Found."}},
	)
	search := ToolSpec{Name: "search", Schema: piSearch, Run: func(context.Context, json.RawMessage) (any, error) { return "x", nil }}
	sent := asking(nil, "q")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), System: "Be brief.", History: sent, Tools: []ToolSpec{search}, Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	for _, r := range reqs {
		assert.Empty(t, r.Invalid)
		assert.Equal(t, `"Be brief."`, string(r.System), "the system prompt goes as instructions")
	}
	assert.Contains(t, string(reqs[1].Messages), `"call_id":"c1"`)
	assert.Contains(t, string(reqs[0].Tools), `"strict":false`, "a function without strict is strict on Responses")
	_, ch, err := sent.Restore(res)
	require.NoError(t, err)
	assert.Len(t, ch.Put, 4)
}

// A pi conversation run on Genkit (the switch off) can be interrupted for an
// approval and resumed: the call that ran before the interrupt keeps its
// output through the pi messages.
func TestPiHistoryResumesAGenkitInterrupt(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"hi"}},
		testllm.Turn{Calls: []testllm.Call{{ID: "r1", Name: "read"}, {ID: "a1", Name: "write", Args: `{"n":1}`}}},
		testllm.Turn{Text: []string{"done"}})
	sent := asking(nil, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Pi: true})
	require.Nil(t, res.Err)
	h := after(t, sent, res)
	require.True(t, h.Pi())

	var readRuns atomic.Int64
	sent = asking(h, "go")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	require.Equal(t, "interrupted", last(t, evs).Kind)
	h = after(t, sent, res)
	stored := must(LoadHistory(must(h.Rows()), h.Next()))
	require.True(t, stored.Pi())
	evs, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: resuming(stored), Tools: mixedTools(&readRuns),
		Responses: []ToolResult{{CallID: "a1", Name: "write", Output: json.RawMessage(`{"draft":7}`)}}})
	require.Nil(t, res.Err)
	assert.Equal(t, "done", last(t, evs).Kind)
	assert.EqualValues(t, 1, readRuns.Load(), "the read tool must not run again")
	assert.Contains(t, string(srv.Requests()[2].Messages), "read-result")
}

// A Responses reasoning item a pi turn kept goes back as Genkit's plugin
// replays it, encrypted content and ID, when the conversation goes on on Genkit.
func TestPiResponsesReasoningGoesOnOnGenkit(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Reasoning: "enc-1", Think: []string{"thinking"}, Text: []string{"one"}},
		testllm.Turn{Reasoning: "enc-2", Think: []string{"more"}, Text: []string{"two"}},
		testllm.Turn{Text: []string{"three"}})
	sent := asking(nil, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Pi: true, Effort: "low"})
	require.Nil(t, res.Err)
	h := must(LoadHistory(must(after(t, sent, res).Rows()), 3))
	// Genkit's own reasoning reply is saved as a pi message too, and goes back as it came.
	for _, q := range []string{"second", "third"} {
		sent = asking(h, q)
		_, res = run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Effort: "low"})
		require.Nil(t, res.Err, q)
		h = after(t, sent, res)
		h = must(LoadHistory(must(h.Rows()), h.Next()))
	}
	reqs := srv.Requests()
	require.Len(t, reqs, 3)
	assert.False(t, ranOnPi(t, srv))
	for _, r := range reqs[1:] {
		assert.Empty(t, r.Invalid)
	}
	assert.Contains(t, string(reqs[2].Messages), "enc-1")
	assert.Contains(t, string(reqs[2].Messages), "enc-2")
}

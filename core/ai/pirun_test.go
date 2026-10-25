package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/internal/testllm"
)

// ranOnPi: pi-ai identifies itself as Godwit (openai-go, on the Genkit path, did not).
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
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), System: "s", Prompt: "p", Reasoning: true})
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
			evs, res := run(e, Request{Model: modelFor(srv, kind, key), System: "s", Prompt: "p"})
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
	_, res := run(e, Request{Model: codexModel(srv), Prompt: "p"})
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
		_, res := run(e, Request{Model: modelFor(srv, kind, "k"), Prompt: "p", Effort: "medium"})
		require.Nil(t, res.Err, kind)
		body := string(srv.Requests()[0].Body)
		assert.Contains(t, body, want, kind)
		if kind == "anthropic" {
			assert.Contains(t, body, `"thinking":{"type":"enabled","budget_tokens":8192}`)
		}
	}
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	_, res := run(e, Request{Model: codexModel(srv), Prompt: "p", Effort: "high"})
	require.Nil(t, res.Err)
	assert.Contains(t, string(srv.Requests()[0].Body), `"reasoning":{"effort":"high","summary":"auto"}`)

	// A model that does not reason refuses the effort, as on Genkit.
	srv = testllm.Start(t, "anthropic")
	srv.Script(testllm.Turn{Status: http.StatusBadRequest})
	_, res = run(e, Request{Model: modelFor(srv, "anthropic", "k"), Prompt: "p", Effort: "low"})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeBadResponse, res.Err.Code)
}

// What used to fall back to Genkit runs on pi (#236): a one-off with tools
// (the first question of a new conversation) and a conversation that did not
// convert to pi messages.
func TestPiRunsWhatGenkitRan(t *testing.T) {
	srv := testllm.Start(t, "openai")
	e := newEngine(t)
	for name, req := range map[string]Request{
		"tools": {Model: modelFor(srv, "openai", "k"), Prompt: "p",
			Tools: []ToolSpec{{Name: "t", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (any, error) { return "x", nil }}}},
		"a Genkit history": {Model: modelFor(srv, "openai", "k"),
			History: asking(must(LoadHistory([]StoredMessage{{Seq: 1, Data: []byte(`{"role":"user","content":[{"text":"hi"}]}`)}}, 2)), "p")},
	} {
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		_, res := run(e, req)
		require.Nil(t, res.Err, name)
		assert.True(t, ranOnPi(t, srv), name)
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
	_, res := run(newEngine(t), Request{Model: ResolvedModel{ServiceID: -5, Kind: "openai", BaseURL: srv.URL, Model: "m-1", Client: c}, Prompt: "p"})
	require.Nil(t, res.Err)
	require.True(t, ranOnPi(t, srv))
	h := srv.Requests()[0].Header
	assert.Equal(t, "secret", h.Get("X-Plugin-Key"))
	assert.Empty(t, h.Get("Authorization"))
}

func TestPiErrorsReadAsGenkitsDo(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Status: http.StatusUnauthorized})
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "sk-secret"), Prompt: "p"})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeUnauthorized, res.Err.Code)
	assert.Equal(t, "error", evs[len(evs)-1].Kind)
	assert.NotContains(t, res.Err.Msg, "sk-secret")

	srv.Script(testllm.Turn{Text: []string{"cut"}, Finish: "length"})
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), Prompt: "p"})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeIncomplete, res.Err.Code)

	_, res = run(newEngine(t), Request{Model: ResolvedModel{ServiceID: 1, Version: 1, Kind: "openai", BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "k"}, Prompt: "p"})
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
		evs, res := run(e, Request{Model: m, Prompt: "p"})
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
	_, res := run(e, Request{Model: modelFor(srv, KindResponses, "tok-secret"), Prompt: "p"})
	srv.Advance()
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeUnauthorized, res.Err.Code)
	assert.Equal(t, DetailChatGPTEligibility, res.Err.Detail)
	assert.NotContains(t, res.Err.Msg, "tok-secret")

	// Not the Codex backend: a 429 is an ordinary rate limit, with no plan to manage.
	srv.Script(testllm.Turn{Status: http.StatusTooManyRequests, Code: "rate_limit_exceeded"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "sk-1"), Prompt: "p"})
	srv.Advance()
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeRateLimited, res.Err.Code)
	assert.Empty(t, res.Err.Detail)

	srv.Script(testllm.Turn{Fail: "x"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "tok"), Prompt: "p"})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeBadResponse, res.Err.Code)
	assert.Empty(t, res.Err.Detail)
}

// A refusal, a filtered reply and one cut off end as on Genkit.
func TestPiResponsesEndsReadAsGenkitsDo(t *testing.T) {
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"half"}, Incomplete: true})
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), Prompt: "p"})
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
		evs, res := run(newEngine(t), Request{Model: respModel(url, "m1"), Prompt: "p"})
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
		_, res := run(e, Request{Model: modelFor(srv, kind, "k"), Prompt: "p"})
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeStale, res.Err.Code)
	})
}

func TestPiRunEndsWhenItsServiceIsForgotten(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
		e := newEngine(t)
		events, wait := e.Run(t.Context(), Request{Model: modelFor(srv, kind, "k"), Prompt: "p"})
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
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), System: "s", History: sent, Tools: []ToolSpec{search}, Reasoning: true,
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
		assert.True(t, IsPiMessage(p.Data), string(p.Data))
	}
	assert.Contains(t, string(ch.Put[1].Data), `"q":"invoice"`) // what the model wrote
	h, err = LoadHistory(ch.Put, ch.Next)
	require.NoError(t, err)
	assert.True(t, h.pi)
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
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: next, Tools: []ToolSpec{search}})
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
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: []ToolSpec{search}, MaxTurns: 1})
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
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "q"), Tools: []ToolSpec{search}, MaxTurns: 1})
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
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "anthropic", "k"), History: asking(nil, "q"), Tools: []ToolSpec{tool}})
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

// A round with a call that needs approval runs none of its calls and asks
// the model nothing more; the run ends interrupted as on Genkit, and the
// saved state resumes on pi after a restart: the held-back call runs then,
// once.
func TestPiMixedInterrupt(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{
			{ID: "r1", Name: "read"},
			{ID: "a1", Name: "write", Args: `{"n":1}`},
			{ID: "a2", Name: "write", Args: `{"n":2}`},
		}}, testllm.Turn{Text: []string{"all done"}})
		var readRuns atomic.Int64
		sent := asking(nil, "go")
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "k"), History: sent, Tools: mixedTools(&readRuns)})
		require.Nil(t, res.Err)
		assert.True(t, ranOnPi(t, srv))
		assert.Zero(t, readRuns.Load(), "nothing runs before the approval")
		end := last(t, evs)
		require.Equal(t, "interrupted", end.Kind)
		require.Len(t, end.Interrupts, 2)
		assert.Equal(t, []string{"a1", "a2"}, []string{callRef(end.Interrupts[0].ID), callRef(end.Interrupts[1].ID)})
		assert.JSONEq(t, `{"n":2}`, string(end.Interrupts[1].Args))
		assert.Equal(t, end.Interrupts, res.Interrupts)
		assert.Empty(t, kindsOf(evs, "tool_result"))
		require.Len(t, srv.Requests(), 1)

		h := after(t, sent, res)
		stored := must(LoadHistory(must(h.Rows()), h.Next()))
		require.True(t, stored.pi)
		var open []string
		for _, c := range stored.OpenCalls() {
			open = append(open, callRef(c.CallID))
		}
		assert.Equal(t, []string{"r1", "a1", "a2"}, open, "the read call was held back")
		evs, res = run(newEngine(t), Request{Model: modelFor(srv, kind, "k"), History: resuming(stored), Tools: mixedTools(&readRuns),
			Responses: []ToolResult{{CallID: end.Interrupts[0].ID, Name: "write", Output: json.RawMessage(`{"draft":7}`)},
				{CallID: end.Interrupts[1].ID, Name: "write", Error: "user rejected"}}})
		require.Nil(t, res.Err)
		assert.True(t, ranOnPi(t, srv))
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.EqualValues(t, 1, readRuns.Load())
		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		calls, results := toolIDs(t, kind, reqs[1].Messages)
		assert.Equal(t, []string{"r1", "a1", "a2"}, calls)
		assert.Equal(t, calls, results)
		for _, s := range []string{"read-result", "draft", "user rejected"} {
			assert.Contains(t, string(reqs[1].Messages), s)
		}
		h, ch, err := resuming(stored).Restore(res)
		require.NoError(t, err)
		assert.Len(t, ch.Put, 4, "three results and the answer")
		h = must(LoadHistory(must(h.Rows()), h.Next()))
		assert.Empty(t, h.OpenCalls())
		assert.Equal(t, "all done", h.Messages()[len(h.Messages())-1].Text)
	})
}

// callRef is the model's ID of a call pi-ai named: a Responses call's ID
// adds its item's.
func callRef(id string) string { ref, _, _ := strings.Cut(id, "|"); return ref }

// A gate decides each call once, on the arguments the Args hook rewrote,
// before any call of the round runs; one it denies does not run either, and
// is decided again when the round resumes.
func TestPiGatesTheWholeRoundFirst(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{
		{ID: "o1", Name: "open", Args: `{"q":"a"}`}, {ID: "d1", Name: "denied"}, {ID: "g1", Name: "gated", Args: `{"q":"b"}`},
	}}, testllm.Turn{Text: []string{"ok"}})
	var gates, runs atomic.Int64
	var seen []string
	count := func(context.Context, json.RawMessage) (any, error) { runs.Add(1); return "ran", nil }
	gate := func(d Decision) func(context.Context, json.RawMessage) (Decision, error) {
		return func(ctx context.Context, args json.RawMessage) (Decision, error) {
			gates.Add(1)
			seen = append(seen, CallID(ctx)+string(args))
			return d, nil
		}
	}
	tools := []ToolSpec{
		{Name: "open", Schema: piSearch, Run: count, Gate: gate(Allow)},
		{Name: "denied", Run: count, NeedsApproval: true, Gate: gate(Deny)},
		{Name: "gated", Schema: piSearch, Run: count, Gate: gate(Approve)},
	}
	rewrite := func(_ context.Context, a json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(strings.ReplaceAll(string(a), `"b"`, `"b2"`)), nil
	}
	sent := asking(nil, "go")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: tools, Args: rewrite})
	require.Nil(t, res.Err)
	require.Equal(t, "interrupted", last(t, evs).Kind)
	assert.EqualValues(t, 3, gates.Load())
	assert.Zero(t, runs.Load())
	assert.Contains(t, seen, `g1{"q":"b2"}`)
	assert.JSONEq(t, `{"q":"b"}`, string(res.Interrupts[0].Args), "the state keeps what the model wrote")

	h := after(t, sent, res)
	evs, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: resuming(h), Tools: tools, Args: rewrite,
		Responses: []ToolResult{{CallID: "g1", Name: "gated", Output: json.RawMessage(`"approved"`)}}})
	require.Nil(t, res.Err)
	assert.EqualValues(t, 5, gates.Load(), "the held-back calls are decided again")
	assert.EqualValues(t, 1, runs.Load())
	errs := map[string]string{}
	for _, ev := range kindsOf(evs, "tool_result") {
		errs[ev.Result.CallID] = ev.Result.Error
	}
	assert.Equal(t, map[string]string{"o1": "", "d1": "tool call denied"}, errs)
}

// A tool that interrupts as it runs (an MCP form) ends the run once its
// round is done; a call that ran with it keeps its result and does not run
// again on resume.
func TestPiToolInterruptsAsItRuns(t *testing.T) {
	srv := testllm.Start(t, "anthropic")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "r1", Name: "read"}, {ID: "f1", Name: "form"}}},
		testllm.Turn{Text: []string{"thanks"}})
	var readRuns atomic.Int64
	tools := append(mixedTools(&readRuns), ToolSpec{Name: "form", Run: func(context.Context, json.RawMessage) (any, error) { return nil, ErrInterrupt }})
	sent := asking(nil, "go")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "anthropic", "k"), History: sent, Tools: tools})
	require.Nil(t, res.Err)
	require.Equal(t, "interrupted", last(t, evs).Kind)
	require.Len(t, res.Interrupts, 1)
	assert.Equal(t, "f1", res.Interrupts[0].ID)
	assert.EqualValues(t, 1, readRuns.Load())
	assert.Len(t, srv.Requests(), 1)

	h := after(t, sent, res)
	h = must(LoadHistory(must(h.Rows()), h.Next()))
	assert.Equal(t, []ToolResult{{CallID: "f1", Name: "form"}}, h.OpenCalls())
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "anthropic", "k"), History: resuming(h), Tools: tools,
		Responses: []ToolResult{{CallID: "f1", Name: "form", Output: json.RawMessage(`{"answer":"yes"}`)}}})
	require.Nil(t, res.Err)
	assert.EqualValues(t, 1, readRuns.Load(), "the read tool must not run again")
	for _, s := range []string{"read-result", "answer"} {
		assert.Contains(t, string(srv.Requests()[1].Messages), s)
	}
}

// Closing a held round (a cancel) answers the calls held back with it too.
func TestPiCloseAnswersHeldBackCalls(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "r1", Name: "read"}, {ID: "a1", Name: "write"}}},
		testllm.Turn{Text: []string{"fine"}})
	var readRuns atomic.Int64
	sent := asking(nil, "go")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: mixedTools(&readRuns)})
	require.Len(t, res.Interrupts, 1)
	h := must(LoadHistory(must(after(t, sent, res).Rows()), after(t, sent, res).Next()))
	closed, _, err := h.Close([]ToolResult{{CallID: "a1", Name: "write", Error: "cancelled"}})
	require.NoError(t, err)
	var results []ToolResult
	for _, m := range closed.Messages()[2:] { // a pi history keeps one message per result
		results = append(results, m.Results...)
	}
	assert.Equal(t, []ToolResult{{CallID: "r1", Name: "read", Output: json.RawMessage(`{"error":"` + notRun + `"}`)},
		{CallID: "a1", Name: "write", Output: json.RawMessage(`{"error":"cancelled"}`)}}, results)
	assert.Zero(t, readRuns.Load())

	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(closed, "next"), Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	calls, ids := toolIDs(t, "openai", srv.Requests()[1].Messages)
	assert.Equal(t, calls, ids)
}

// A resume must answer exactly the interrupted calls; the history stays open.
func TestPiResumeMatchesTheInterrupts(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "r1", Name: "read"}, {ID: "a1", Name: "write"}}})
	var readRuns atomic.Int64
	sent := asking(nil, "go")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: mixedTools(&readRuns)})
	require.Len(t, res.Interrupts, 1)
	h := after(t, sent, res)
	for _, resps := range [][]ToolResult{
		{{CallID: "r1", Name: "read", Output: json.RawMessage(`"x"`)}},
		{{CallID: "a1", Name: "read", Output: json.RawMessage(`"x"`)}},
		{{CallID: "a1", Name: "write", Output: json.RawMessage(`"x"`)}, {CallID: "a1", Name: "write", Output: json.RawMessage(`"x"`)}},
	} {
		_, r := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: resuming(h), Tools: mixedTools(&readRuns), Responses: resps})
		require.NotNil(t, r.Err)
		assert.Equal(t, CodeInternal, r.Err.Code)
	}
	assert.Len(t, srv.Requests(), 1, "no model request")
	assert.Zero(t, readRuns.Load())
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
	_, res := collect(newEngine(t).Run(ctx, Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "q"), Tools: []ToolSpec{tool}}))
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeCanceled, res.Err.Code)
	<-stopped
}

func TestUnconvertedHistoriesGoOnOnPi(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"one"}})
	sent := asking(nil, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent})
	require.Nil(t, res.Err)
	h, _, err := sent.Restore(res)
	require.NoError(t, err)
	assert.True(t, h.pi, "a new conversation is saved as pi messages (#234)")

	// One the startup conversion could not convert goes on on pi, saved as it is.
	old, _, _, err := LegacyHistory([]byte(`[{"role":"user","content":[{"text":"first"}]},{"role":"model","content":[{"text":"one"}]}]`), nil)
	require.NoError(t, err)
	assert.False(t, old.pi)
	srv.Script(testllm.Turn{Text: []string{"two"}})
	sent = asking(old, "second")
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	assert.Contains(t, string(srv.Requests()[1].Messages), "one")
	h, ch, err := sent.Restore(res)
	require.NoError(t, err)
	for _, p := range ch.Put {
		assert.False(t, IsPiMessage(p.Data), string(p.Data))
	}
	h = must(LoadHistory(must(h.Rows()), h.Next()))
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
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), System: "Be brief.", History: sent, Tools: []ToolSpec{search}})
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

// A Responses reasoning item a turn kept goes back, encrypted content and ID,
// in every later turn.
func TestPiResponsesReasoningGoesOn(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Reasoning: "enc-1", Think: []string{"thinking"}, Text: []string{"one"}},
		testllm.Turn{Reasoning: "enc-2", Think: []string{"more"}, Text: []string{"two"}},
		testllm.Turn{Text: []string{"three"}})
	sent := asking(nil, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Effort: "low"})
	require.Nil(t, res.Err)
	h := must(LoadHistory(must(after(t, sent, res).Rows()), 3))
	for _, q := range []string{"second", "third"} {
		sent = asking(h, q)
		_, res = run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Effort: "low"})
		require.Nil(t, res.Err, q)
		h = after(t, sent, res)
		h = must(LoadHistory(must(h.Rows()), h.Next()))
	}
	reqs := srv.Requests()
	require.Len(t, reqs, 3)
	for _, r := range reqs[1:] {
		assert.Empty(t, r.Invalid)
	}
	assert.Contains(t, string(reqs[2].Messages), "enc-1")
	assert.Contains(t, string(reqs[2].Messages), "enc-2")
}

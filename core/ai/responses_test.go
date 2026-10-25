package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kindResp = "openai-responses"

func respModel(url, model string) ResolvedModel {
	return ResolvedModel{ServiceID: 1, Version: 1, Kind: kindResp, BaseURL: url, Model: model, APIKey: "sekret-key-8"}
}

// body decodes a raw Responses request.
func body(t *testing.T, r testllm.Request) map[string]any {
	t.Helper()
	var b map[string]any
	require.NoError(t, json.Unmarshal(r.Body, &b))
	return b
}

func TestResponsesStreamsText(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Text: []string{"Hel", "lo"}})
	evs, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), System: "be brief", Prompt: "hi"})
	require.Nil(t, res.Err)
	assert.Len(t, kindsOf(evs, "text"), 2)
	assert.Equal(t, Message{Role: "model", Text: "Hello"}, res.Messages[len(res.Messages)-1], "text is not assembled twice")
	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	require.Empty(t, reqs[0].Invalid)
	b := body(t, reqs[0])
	assert.Equal(t, false, b["store"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, b["include"])
	assert.Equal(t, "be brief", b["instructions"])
	assert.Equal(t, "m1", b["model"])
	assert.Equal(t, "/responses", reqs[0].Path)
}

func TestResponsesToolCall(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "call_x", Name: "lookup", Args: `{"q":"é"}`}}}, testllm.Turn{Text: []string{"done"}})
	var got atomic.Value
	tools := []ToolSpec{{Name: "lookup", Run: func(_ context.Context, a json.RawMessage) (any, error) {
		got.Store(string(a))
		return map[string]any{"n": 1}, nil
	}}}
	evs, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x", Tools: tools})
	require.Nil(t, res.Err)
	assert.JSONEq(t, `{"q":"é"}`, got.Load().(string))
	calls := kindsOf(evs, "tool_call")
	require.Len(t, calls, 1)
	assert.Equal(t, "call_x", callRef(calls[0].Call.ID), "the call_id first, then pi-ai's fc_ item id")
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	require.Empty(t, reqs[1].Invalid)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(reqs[1].Messages, &items))
	var fc, out map[string]any
	for _, it := range items {
		switch it["type"] {
		case "function_call":
			fc = it
		case "function_call_output":
			out = it
		}
	}
	require.NotNil(t, fc)
	require.NotNil(t, out)
	assert.Equal(t, "call_x", fc["call_id"])
	assert.Equal(t, "lookup", fc["name"])
	assert.JSONEq(t, `{"q":"é"}`, fc["arguments"].(string))
	assert.Equal(t, "call_x", out["call_id"])
	assert.JSONEq(t, `{"n":1}`, out["output"].(string))
}

// sse answers the nth request with the nth list of SSE events (the last one
// once they run out).
func sse(t *testing.T, turns ...[]string) string {
	t.Helper()
	var n atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range turns[min(int(n.Add(1))-1, len(turns)-1)] {
			fmt.Fprint(w, e+"\n\n")
		}
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func ev(typ string, data string) string {
	return "event: " + typ + "\ndata: " + strings.Replace(data, "{", `{"type":"`+typ+`",`, 1)
}

func TestResponsesParallelTools(t *testing.T) {
	fc := func(id, call, args, status string) string {
		return fmt.Sprintf(`{"type":"function_call","id":%q,"call_id":%q,"name":"lookup","arguments":%q,"status":%q}`, id, call, args, status)
	}
	url := sse(t, []string{
		ev("response.created", `{"response":{"id":"r","status":"in_progress","output":[]}}`),
		ev("response.output_item.added", `{"output_index":0,"item":`+fc("fc_a", "call_a", "", "in_progress")+`}`),
		ev("response.output_item.added", `{"output_index":1,"item":`+fc("fc_b", "call_b", "", "in_progress")+`}`),
		ev("response.function_call_arguments.delta", `{"item_id":"fc_b","output_index":1,"delta":"{\"q\":"}`),
		ev("response.function_call_arguments.delta", `{"item_id":"fc_a","output_index":0,"delta":"{\"q\":"}`),
		ev("response.function_call_arguments.delta", `{"item_id":"fc_a","output_index":0,"delta":"\"a\"}"}`),
		ev("response.function_call_arguments.delta", `{"item_id":"fc_b","output_index":1,"delta":"\"b\"}"}`),
		ev("response.output_item.done", `{"output_index":1,"item":`+fc("fc_b", "call_b", `{"q":"b"}`, "completed")+`}`),
		ev("response.output_item.done", `{"output_index":0,"item":`+fc("fc_a", "call_a", `{"q":"a"}`, "completed")+`}`),
		ev("response.completed", `{"response":{"id":"r","status":"completed","output":[]}}`),
	}, []string{
		ev("response.output_text.delta", `{"item_id":"m","output_index":0,"content_index":0,"delta":"fine"}`),
		ev("response.completed", `{"response":{"id":"r2","status":"completed","output":[]}}`),
	})
	var runs atomic.Int64
	tools := []ToolSpec{{Name: "lookup", Run: func(context.Context, json.RawMessage) (any, error) {
		runs.Add(1)
		return "ok", nil
	}}}
	evs, res := run(newEngine(t), Request{Model: respModel(url, "m1"), Prompt: "x", Tools: tools})
	require.Nil(t, res.Err)
	calls := kindsOf(evs, "tool_call")
	require.Len(t, calls, 2)
	byID := map[string]string{}
	for _, c := range calls {
		byID[callRef(c.Call.ID)] = string(c.Call.Args)
	}
	assert.JSONEq(t, `{"q":"a"}`, byID["call_a"])
	assert.JSONEq(t, `{"q":"b"}`, byID["call_b"])
	assert.EqualValues(t, 2, runs.Load())
}

func TestResponsesReasoningCarried(t *testing.T) {
	var logs bytes.Buffer
	e, err := NewEngine(slog.New(slog.NewTextHandler(&logs, nil)), nil)
	require.NoError(t, err)
	t.Cleanup(e.Close)
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Reasoning: "ENC-SECRET-1", Text: []string{"let me look"}, Calls: []testllm.Call{{ID: "call_r", Name: "read"}}},
		testllm.Turn{Reasoning: "ENC-SECRET-2", Text: []string{"answer"}},
		testllm.Turn{Text: []string{"more"}})
	var readRuns atomic.Int64
	sent := asking(nil, "x")
	evs, res := run(e, Request{Model: respModel(srv.URL, "m1"), History: sent, Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	assert.Equal(t, "done", last(t, evs).Kind)
	// the history goes through a fresh engine
	evs2, res2 := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), History: asking(after(t, sent, res), "again"), Tools: mixedTools(&readRuns)})
	require.Nil(t, res2.Err)
	reqs := srv.Requests()
	require.Len(t, reqs, 3)
	for i, r := range reqs {
		assert.Empty(t, r.Invalid, i)
	}
	assert.Contains(t, string(reqs[1].Body), "ENC-SECRET-1")
	assert.Contains(t, string(reqs[1].Body), `"rs_0"`)
	assert.Contains(t, string(reqs[2].Body), "ENC-SECRET-2")
	for _, s := range []string{fmt.Sprint(evs), fmt.Sprint(evs2), fmt.Sprint(res.Messages), fmt.Sprint(res2.Messages), logs.String()} {
		assert.NotContains(t, s, "ENC-SECRET")
	}
}

func TestResponsesInterruptResume(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Reasoning: "ENC-R", Calls: []testllm.Call{{ID: "call_r", Name: "read"}, {ID: "call_w", Name: "write", Args: `{"n":1}`}}},
		testllm.Turn{Text: []string{"all ", "done"}})
	var readRuns atomic.Int64
	sent := asking(nil, "go")
	_, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), History: sent, Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	require.Len(t, res.Interrupts, 1)
	id := res.Interrupts[0].ID
	assert.Equal(t, "call_w", callRef(id))

	evs, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), History: resuming(after(t, sent, res)), Tools: mixedTools(&readRuns),
		Responses: []ToolResult{{CallID: id, Name: "write", Output: json.RawMessage(`{"draft":7}`)}}})
	require.Nil(t, res.Err)
	assert.Equal(t, "done", last(t, evs).Kind)
	assert.EqualValues(t, 1, readRuns.Load())
	assert.Equal(t, "all done", res.Messages[len(res.Messages)-1].Text)
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	require.Empty(t, reqs[1].Invalid)
	calls, outs := toolIDs(t, kindResp, reqs[1].Messages)
	assert.Equal(t, []string{"call_r", "call_w"}, calls)
	assert.ElementsMatch(t, []string{"call_r", "call_w"}, outs, "each call answered exactly once")
	assert.Contains(t, string(reqs[1].Messages), "ENC-R")
}

func TestResponsesTerminalStates(t *testing.T) {
	for name, c := range map[string]struct {
		turn testllm.Turn
		code Code
	}{
		"failed":    {testllm.Turn{Fail: "UPSTREAM-BODY boom"}, CodeBadResponse},
		"truncated": {testllm.Turn{Truncate: true}, CodeBadResponse},
	} {
		t.Run(name, func(t *testing.T) {
			srv := testllm.Start(t, kindResp)
			c.turn.Text = []string{"half"}
			c.turn.Calls = []testllm.Call{{ID: "call_r", Name: "read"}}
			srv.Script(c.turn)
			var readRuns atomic.Int64
			evs, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x", Tools: mixedTools(&readRuns)})
			require.NotNil(t, res.Err)
			assert.Equal(t, c.code, res.Err.Code)
			assert.NotContains(t, res.Err.Msg, "UPSTREAM-BODY")
			assert.Nil(t, res.out, "nothing is kept")
			assert.Zero(t, readRuns.Load())
			assert.Empty(t, kindsOf(evs, "tool_call"))
			assert.Empty(t, kindsOf(evs, "done"))
			assert.Equal(t, "error", last(t, evs).Kind)
		})
	}
}

// A reply cut off at the output limit runs none of its calls, whose
// arguments may be cut too: pi-agent-core refuses them and the model goes on.
func TestResponsesIncompleteCallsDoNotRun(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Text: []string{"half"}, Calls: []testllm.Call{{ID: "call_r", Name: "read"}}, Incomplete: true},
		testllm.Turn{Text: []string{"sorry"}})
	var readRuns atomic.Int64
	evs, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x", Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	assert.Zero(t, readRuns.Load())
	rs := kindsOf(evs, "tool_result")
	require.Len(t, rs, 1)
	assert.Contains(t, rs[0].Result.Error, "output token limit")
}

func TestResponsesErrors(t *testing.T) {
	for status, code := range map[int]Code{401: CodeUnauthorized, 403: CodeUnauthorized, 429: CodeRateLimited, 500: CodeUnavailable} {
		srv := testllm.Start(t, kindResp)
		srv.Script(testllm.Turn{Status: status})
		_, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x"})
		require.NotNil(t, res.Err, status)
		assert.Equal(t, code, res.Err.Code, status)
		assert.NotContains(t, res.Err.Msg, "sekret-key-8", status)
		assert.NotContains(t, res.Err.Msg, "rejected key", status)
	}

	inStream := sse(t, []string{ev("response.created", `{"response":{"id":"r","status":"in_progress","output":[]}}`),
		`event: error` + "\n" + `data: {"type":"error","code":"server_error","message":"UPSTREAM-BODY sekret-key-8","param":null}`})
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example/steal?UPSTREAM-LOCATION", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	for name, url := range map[string]string{"sse error": inStream, "redirect": redirect.URL} {
		evs, res := run(newEngine(t), Request{Model: respModel(url, "m1"), Prompt: "x"})
		require.NotNil(t, res.Err, name)
		assert.Equal(t, CodeBadResponse, res.Err.Code, name)
		for _, s := range []string{"sekret-key-8", "UPSTREAM", "evil.example"} {
			assert.NotContains(t, res.Err.Msg, s, name)
		}
		assert.Equal(t, "error", last(t, evs).Kind, name)
	}
}

func TestResponsesTwoModelsOneEngine(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	e := newEngine(t)
	for _, m := range []string{"m1", "vendor/m2"} {
		_, res := run(e, Request{Model: respModel(srv.URL, m), Prompt: "x"})
		require.Nil(t, res.Err, m)
	}
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "m1", reqs[0].Model)
	assert.Equal(t, "vendor/m2", reqs[1].Model)
	assert.EqualValues(t, 1, e.inits.Load())
}

func TestResponsesStrictFlag(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	noop := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	strictSchema := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}, "additionalProperties": false}
	_, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x", Tools: []ToolSpec{
		{Name: "plugin_tool", Description: "from a plugin", Run: noop},
		{Name: "builtin", Description: "strict", Strict: true, Schema: strictSchema, Run: noop},
	}})
	require.Nil(t, res.Err)
	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	require.Empty(t, reqs[0].Invalid)
	strict := map[string]any{}
	for _, tl := range body(t, reqs[0])["tools"].([]any) {
		m := tl.(map[string]any)
		assert.Equal(t, "function", m["type"])
		strict[m["name"].(string)] = m["strict"]
	}
	assert.Equal(t, map[string]any{"plugin_tool": false, "builtin": true}, strict)
}

// A strict tool gets additionalProperties:false on every object, as the Chat
// Completions path does; a non-strict tool's schema goes out as given.
func TestResponsesStrictSchema(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	noop := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	schema := func() map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{
			"inner": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
			"list":  map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{}}},
		}}
	}
	given := schema()
	_, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x", Tools: []ToolSpec{
		{Name: "builtin", Strict: true, Schema: given, Run: noop},
		{Name: "plugin_tool", Schema: schema(), Run: noop},
	}})
	require.Nil(t, res.Err)
	assert.Equal(t, schema(), given, "the caller's schema is not modified")
	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	var b struct {
		Tools []struct {
			Name       string
			Strict     bool
			Parameters json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(reqs[0].Body, &b))
	require.Len(t, b.Tools, 2)
	tools := map[string]int{}
	for i, tl := range b.Tools {
		tools[tl.Name] = i
	}
	st, pl := b.Tools[tools["builtin"]], b.Tools[tools["plugin_tool"]]
	var strict map[string]any
	require.NoError(t, json.Unmarshal(st.Parameters, &strict))
	assert.True(t, st.Strict)
	assert.Equal(t, false, strict["additionalProperties"])
	props := strict["properties"].(map[string]any)
	assert.Equal(t, false, props["inner"].(map[string]any)["additionalProperties"])
	assert.Equal(t, false, props["list"].(map[string]any)["items"].(map[string]any)["additionalProperties"])

	assert.False(t, pl.Strict)
	want, _ := json.Marshal(schema())
	assert.JSONEq(t, string(want), string(pl.Parameters), "a non-strict schema goes out unchanged")
}

// Final whole-branch review #2: a refusal is a withheld reply (as a chat
// content_filter), never a finished empty one: from the refusal events and
// from a refusal in the message content alone.
func TestResponsesRefusal(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Refusal: "I can't help with that."})
	only := sse(t, []string{
		ev("response.output_item.done", `{"output_index":0,"item":{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"no"}]}}`),
		ev("response.completed", `{"response":{"id":"r","status":"completed","output":[]}}`),
	})
	for name, url := range map[string]string{"events": srv.URL, "content": only} {
		t.Run(name, func(t *testing.T) {
			evs, res := run(newEngine(t), Request{Model: respModel(url, "m1"), Prompt: "x", MaxTurns: 1})
			require.NotNil(t, res.Err)
			assert.Equal(t, CodeIncomplete, res.Err.Code)
			assert.Equal(t, "the model service withheld the reply", res.Err.Msg)
			assert.Nil(t, res.out, "nothing is kept")
			assert.Empty(t, kindsOf(evs, "done"))
		})
	}
}

// A Responses run reports the tokens each model call took, summed, and the
// last call's input.
func TestResponsesUsage(t *testing.T) {
	srv := testllm.Start(t, kindResp)
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "call_r", Name: "read"}}, Usage: [2]int{40, 6}},
		testllm.Turn{Text: []string{"answer"}, Usage: [2]int{60, 9}})
	var readRuns atomic.Int64
	_, res := run(newEngine(t), Request{Model: respModel(srv.URL, "m1"), Prompt: "x", Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	assert.Equal(t, Usage{Input: 100, Output: 15, Last: 60}, res.Usage)
}

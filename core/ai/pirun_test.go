package ai

import (
	"context"
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

func TestPiRunsAOneOff(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Think: []string{"hmm"}, Text: []string{"Fri", "day"}, Usage: [2]int{9, 2}})
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), System: "s", Prompt: "p", Pi: true, Reasoning: true})
	require.Nil(t, res.Err)
	assert.Equal(t, []Event{{Kind: "reasoning", Text: "hmm"}, {Kind: "text", Text: "Fri"}, {Kind: "text", Text: "day"}, {Kind: "done"}}, evs)
	assert.Equal(t, Usage{Input: 9, Output: 2, Last: 9}, res.Usage)
	assert.True(t, ranOnPi(t, srv))
}

func TestPiLeavesTheRestToGenkit(t *testing.T) {
	srv := testllm.Start(t, "openai")
	e := newEngine(t)
	for name, req := range map[string]Request{
		"off":    {Model: modelFor(srv, "openai", "k"), Prompt: "p"},
		"no key": {Model: modelFor(srv, "openai", ""), Prompt: "p", Pi: true},
		"tools": {Model: modelFor(srv, "openai", "k"), Prompt: "p", Pi: true,
			Tools: []ToolSpec{{Name: "t", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (any, error) { return "x", nil }}}},
		"plugin": {Model: ResolvedModel{Kind: "openai", BaseURL: srv.URL, Model: "m-1", APIKey: "k", Client: http.DefaultClient}, Prompt: "p", Pi: true},
	} {
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		_, res := run(e, req)
		require.Nil(t, res.Err, name)
		assert.False(t, ranOnPi(t, srv), name)
	}
	srv2 := testllm.Start(t, "anthropic")
	srv2.Script(testllm.Turn{Text: []string{"ok"}})
	_, res := run(e, Request{Model: modelFor(srv2, "anthropic", "k"), Prompt: "p", Pi: true})
	require.Nil(t, res.Err)
	assert.False(t, ranOnPi(t, srv2))
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

func TestPiRunOfAChangedServiceIsStale(t *testing.T) {
	srv := testllm.Start(t, "openai")
	e := newEngine(t)
	e.Current = func(context.Context, int64) (int64, error) { return 2, nil }
	_, res := run(e, Request{Model: modelFor(srv, "openai", "k"), Prompt: "p", Pi: true})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeStale, res.Err.Code)
}

func TestPiRunEndsWhenItsServiceIsForgotten(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
	e := newEngine(t)
	events, wait := e.Run(t.Context(), Request{Model: modelFor(srv, "openai", "k"), Prompt: "p", Pi: true})
	assert.Equal(t, Event{Kind: "text", Text: "partial"}, <-events)
	e.Forget(1)
	_, res := collect(events, wait)
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeStale, res.Err.Code)
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

func TestPiFlattensToolSchemas(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	schema := map[string]any{"type": "object", "$defs": map[string]any{"addr": map[string]any{"type": "string"}},
		"properties": map[string]any{"to": map[string]any{"$ref": "#/$defs/addr"}}}
	tool := ToolSpec{Name: "send", Schema: schema, Run: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "q"), Tools: []ToolSpec{tool}, Pi: true})
	require.Nil(t, res.Err)
	tools := string(srv.Requests()[0].Tools)
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

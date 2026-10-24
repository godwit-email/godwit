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

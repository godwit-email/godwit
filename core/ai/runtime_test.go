package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/firebase/genkit/go/core/logger"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var kinds = []string{"openai", "openai-responses", "anthropic"}

func eachKind(t *testing.T, f func(t *testing.T, kind string)) {
	for _, k := range kinds {
		t.Run(k, func(t *testing.T) { f(t, k) })
	}
}

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, err)
	t.Cleanup(e.Close)
	return e
}

func modelFor(srv *testllm.Server, kind, key string) ResolvedModel {
	return ResolvedModel{ServiceID: 1, Version: 1, Kind: kind, BaseURL: srv.URL, Model: "m-1", APIKey: key}
}

func collect(events <-chan Event, wait func() Result) ([]Event, Result) {
	var evs []Event
	for ev := range events {
		evs = append(evs, ev)
	}
	return evs, wait()
}

func run(e *Engine, req Request) ([]Event, Result) { return collect(e.Run(context.Background(), req)) }

func last(t *testing.T, evs []Event) Event {
	t.Helper()
	require.NotEmpty(t, evs)
	return evs[len(evs)-1]
}

func kindsOf(evs []Event, kind string) []Event {
	var out []Event
	for _, ev := range evs {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// toolIDs returns the IDs of the tool calls and of the tool results in a raw
// request's messages.
func toolIDs(t *testing.T, kind string, raw json.RawMessage) (calls, results []string) {
	t.Helper()
	var msgs []map[string]any
	require.NoError(t, json.Unmarshal(raw, &msgs))
	for _, m := range msgs {
		if kind == "openai-responses" { // input items
			switch m["type"] {
			case "function_call":
				calls = append(calls, m["call_id"].(string))
			case "function_call_output":
				results = append(results, m["call_id"].(string))
			}
			continue
		}
		if kind == "openai" {
			if id, ok := m["tool_call_id"].(string); ok {
				results = append(results, id)
			}
			tcs, _ := m["tool_calls"].([]any)
			for _, tc := range tcs {
				calls = append(calls, tc.(map[string]any)["id"].(string))
			}
			continue
		}
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			pm := p.(map[string]any)
			switch pm["type"] {
			case "tool_use":
				calls = append(calls, pm["id"].(string))
			case "tool_result":
				results = append(results, pm["tool_use_id"].(string))
			}
		}
	}
	return calls, results
}

func TestRunStreamsText(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"Hel", "lo"}})
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), System: "be brief", Prompt: "hi there"})
		require.Nil(t, res.Err)
		var text strings.Builder
		for _, ev := range kindsOf(evs, "text") {
			text.WriteString(ev.Text)
		}
		assert.Equal(t, "Hello", text.String())
		assert.GreaterOrEqual(t, len(kindsOf(evs, "text")), 2, "streamed in pieces")
		assert.Equal(t, "done", last(t, evs).Kind)
		require.NotEmpty(t, res.Messages)
		assert.Equal(t, Message{Role: "model", Text: "Hello"}, res.Messages[len(res.Messages)-1])
		assert.NotEmpty(t, res.State)
		reqs := srv.Requests()
		require.Len(t, reqs, 1)
		assert.Contains(t, string(reqs[0].System), "be brief")
		assert.Contains(t, string(reqs[0].Messages), "hi there")

		// the state continues the conversation, with one system prompt
		srv.Script(testllm.Turn{Text: []string{"again"}})
		_, res2 := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), System: "be brief", State: res.State, Prompt: "more"})
		require.Nil(t, res2.Err)
		reqs = srv.Requests()
		assert.Contains(t, string(reqs[1].Messages), "hi there")
		assert.Contains(t, string(reqs[1].Messages), "Hel")
		system := string(reqs[1].Messages) // openai: the system prompt is the first message
		if kind != "openai" {
			system += string(reqs[1].System)
		}
		assert.Equal(t, 1, strings.Count(system, "be brief"))
	})
}

func TestRunToolLoop(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"looking"}, Calls: []testllm.Call{
			{ID: "c1", Name: "lookup", Args: `{"q":"x"}`},
			{ID: "c2", Name: "broken", Args: `{}`},
		}}, testllm.Turn{Text: []string{"found it"}})
		var gotArgs atomic.Value
		tools := []ToolSpec{
			{Name: "lookup", Description: "look up", Schema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
				Run: func(ctx context.Context, args json.RawMessage) (any, error) {
					gotArgs.Store(string(args))
					return map[string]any{"answer": 42}, nil
				}},
			{Name: "broken", Description: "fails", Run: func(context.Context, json.RawMessage) (any, error) {
				return nil, errors.New("disk on fire")
			}},
		}
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "find x", Tools: tools})
		require.Nil(t, res.Err)
		args, _ := gotArgs.Load().(string)
		assert.JSONEq(t, `{"q":"x"}`, args)
		calls := kindsOf(evs, "tool_call")
		require.Len(t, calls, 2)
		results := map[string]*ToolResult{}
		for _, ev := range kindsOf(evs, "tool_result") {
			results[ev.Result.CallID] = ev.Result
		}
		require.Len(t, results, 2)
		assert.JSONEq(t, `{"answer":42}`, string(results["c1"].Output))
		assert.Equal(t, "lookup", results["c1"].Name)
		assert.Equal(t, "disk on fire", results["c2"].Error)
		assert.Equal(t, "done", last(t, evs).Kind)

		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		assert.Contains(t, string(reqs[1].Messages), "42")
		assert.Contains(t, string(reqs[1].Messages), "disk on fire")
		_, ids := toolIDs(t, kind, reqs[1].Messages)
		assert.ElementsMatch(t, []string{"c1", "c2"}, ids)
		assert.Contains(t, string(reqs[0].Tools), "look up")
	})
}

func mixedTools(readRuns *atomic.Int64) []ToolSpec {
	return []ToolSpec{
		{Name: "read", Description: "reads", Run: func(context.Context, json.RawMessage) (any, error) {
			readRuns.Add(1)
			return "read-result", nil
		}},
		{Name: "write", Description: "writes", NeedsApproval: true, Run: func(context.Context, json.RawMessage) (any, error) {
			panic("an approval tool must not run")
		}},
	}
}

func TestRunMixedInterrupt(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{
			{ID: "r1", Name: "read"},
			{ID: "a1", Name: "write", Args: `{"n":1}`},
			{ID: "a2", Name: "write", Args: `{"n":2}`},
		}}, testllm.Turn{Text: []string{"all done"}})
		var readRuns atomic.Int64
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "go", Tools: mixedTools(&readRuns)})
		require.Nil(t, res.Err)
		assert.EqualValues(t, 1, readRuns.Load())
		end := last(t, evs)
		require.Equal(t, "interrupted", end.Kind)
		require.Len(t, end.Interrupts, 2)
		assert.Equal(t, "a1", end.Interrupts[0].ID)
		assert.Equal(t, "a2", end.Interrupts[1].ID)
		assert.Equal(t, "write", end.Interrupts[0].Name)
		assert.JSONEq(t, `{"n":2}`, string(end.Interrupts[1].Args))
		assert.Equal(t, end.Interrupts, res.Interrupts)
		require.Len(t, srv.Requests(), 1)

		// a restart: the state goes through JSON and a fresh engine
		var stored json.RawMessage
		require.NoError(t, json.Unmarshal(must(json.Marshal(json.RawMessage(res.State))), &stored))
		evs, res = run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), State: stored, Tools: mixedTools(&readRuns),
			Responses: []ToolResult{{CallID: "a1", Name: "write", Output: json.RawMessage(`{"draft":7}`)}, {CallID: "a2", Name: "write", Error: "user rejected"}}})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.EqualValues(t, 1, readRuns.Load(), "the read tool must not run again")
		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		_, ids := toolIDs(t, kind, reqs[1].Messages)
		assert.ElementsMatch(t, []string{"r1", "a1", "a2"}, ids)
		for _, s := range []string{"read-result", `draft`, "user rejected"} {
			assert.Contains(t, string(reqs[1].Messages), s)
		}
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRunCancelMidStream(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events, wait := newEngine(t).Run(ctx, Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x"})
		var evs []Event
		for ev := range events {
			evs = append(evs, ev)
			if ev.Kind == "text" {
				cancel()
			}
		}
		res := wait()
		end := last(t, evs)
		require.Equal(t, "error", end.Kind)
		assert.True(t, errors.Is(end.Err, context.Canceled))
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeCanceled, res.Err.Code)
	})
}

func TestRunConsumerGone(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"partial"}, Hang: true})
		ctx, cancel := context.WithCancel(context.Background())
		_, wait := newEngine(t).Run(ctx, Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x"})
		time.Sleep(100 * time.Millisecond)
		cancel()
		done := make(chan Result)
		go func() { done <- wait() }()
		select {
		case res := <-done:
			require.NotNil(t, res.Err)
			assert.Equal(t, CodeCanceled, res.Err.Code)
		case <-time.After(time.Second):
			t.Fatal("wait did not return")
		}
	})
}

func TestRunErrorCodes(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		for status, code := range map[int]Code{401: CodeUnauthorized, 429: CodeRateLimited, 503: CodeUnavailable} {
			srv := testllm.Start(t, kind)
			for range 5 { // the SDKs retry some statuses
				srv.Script(testllm.Turn{Status: status})
			}
			evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "sekret-key-1"), Prompt: "x"})
			require.NotNil(t, res.Err, status)
			assert.Equal(t, code, res.Err.Code, status)
			assert.NotContains(t, res.Err.Msg, "sekret-key-1")
			assert.NotContains(t, res.Err.Msg, "rejected key")
			end := last(t, evs)
			assert.Equal(t, "error", end.Kind)
			assert.Equal(t, res.Err, end.Err)
		}
	})
}

func TestRunIgnoresEnv(t *testing.T) {
	var decoyHits atomic.Int64
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { decoyHits.Add(1) }))
	defer decoy.Close()
	env := map[string]string{
		"OPENAI_API_KEY": "env-openai-key", "OPENAI_BASE_URL": decoy.URL,
		"ANTHROPIC_API_KEY": "env-anthropic-key", "ANTHROPIC_AUTH_TOKEN": "env-auth-token", "ANTHROPIC_BASE_URL": decoy.URL,
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	for _, key := range []string{"req-key", ""} {
		eachKind(t, func(t *testing.T, kind string) {
			srv := testllm.Start(t, kind)
			_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, key), Prompt: "x"})
			require.Nil(t, res.Err)
			hs := srv.Headers()
			require.Len(t, hs, 1)
			h := hs[0]
			for name, vals := range h {
				for _, v := range vals {
					for _, ev := range env {
						assert.NotContains(t, v, ev, name)
					}
				}
			}
			wantAuth, wantXKey := "", ""
			if key != "" && kind != "anthropic" {
				wantAuth = "Bearer " + key
			}
			if key != "" && kind == "anthropic" {
				wantXKey = key
			}
			assert.Equal(t, wantAuth, h.Get("Authorization"))
			assert.Equal(t, wantXKey, h.Get("X-Api-Key"))
		})
	}
	assert.Zero(t, decoyHits.Load())
}

func TestEngineLifecycle(t *testing.T) {
	srv := testllm.Start(t, "openai")
	warm := newEngine(t) // one-time global setup is not a leak
	run(warm, Request{Model: modelFor(srv, "openai", "test-key-1"), Prompt: "x"})
	warm.Close()
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	e, err := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, err)
	m := modelFor(srv, "openai", "test-key-1")
	for range 100 {
		_, res := run(e, Request{Model: m, Prompt: "x"})
		require.Nil(t, res.Err)
	}
	assert.EqualValues(t, 1, e.inits.Load())
	e.Forget(m.ServiceID)
	run(e, Request{Model: m, Prompt: "x"})
	assert.EqualValues(t, 2, e.inits.Load())
	m.Version++
	run(e, Request{Model: m, Prompt: "x"})
	assert.EqualValues(t, 3, e.inits.Load(), "a new service version gets a new instance")
	e.Close()

	_, res := run(e, Request{Model: m, Prompt: "x"})
	require.NotNil(t, res.Err, "runs after Close fail")

	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > base+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), base+2)
}

func TestEngineRefusesDevEnv(t *testing.T) {
	t.Setenv("GENKIT_ENV", "dev")
	_, err := NewEngine(slog.Default(), nil)
	assert.Error(t, err)
}

func TestRedactingLogger(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	e, err := NewEngine(l, func() []string { return []string{"sekret-1"} })
	require.NoError(t, err)
	defer e.Close()
	srv := testllm.Start(t, "anthropic")
	_, res := run(e, Request{Model: modelFor(srv, "anthropic", "inst-key-9"), Prompt: "x"})
	require.Nil(t, res.Err)

	ctx := e.logCtx(context.Background())
	logger.Warn(ctx, "warning sekret-1")
	logger.Error(ctx, "failed with sekret-1", "err", fmt.Errorf("bad key %s", "inst-key-9"), slog.Group("g", "k", "sekret-1"))
	out := buf.String()
	assert.NotContains(t, out, "sekret-1")
	assert.NotContains(t, out, "inst-key-9")
	assert.NotContains(t, out, "warning", "below Error is dropped")
	assert.Equal(t, 3, strings.Count(out, "[redacted]"), out)
}

// TestRedactsSecretAddedLater: a secret that joins the set after the engine is
// created (a plugin installed later) is redacted from Genkit's logs, in the
// message and in plain, error and grouped attributes.
func TestRedactsSecretAddedLater(t *testing.T) {
	var buf bytes.Buffer
	var secrets atomic.Pointer[[]string]
	secrets.Store(&[]string{})
	e, err := NewEngine(slog.New(slog.NewJSONHandler(&buf, nil)), func() []string { return *secrets.Load() })
	require.NoError(t, err)
	defer e.Close()
	secrets.Store(&[]string{"plugin-sekret-42"})

	ctx := e.logCtx(context.Background())
	logger.Error(ctx, "call failed: plugin-sekret-42", "url", "https://x/?k=plugin-sekret-42",
		"err", fmt.Errorf("denied %s", "plugin-sekret-42"), slog.Group("req", "auth", "Bearer plugin-sekret-42"))
	out := buf.String()
	assert.NotContains(t, out, "plugin-sekret-42")
	assert.Equal(t, 4, strings.Count(out, "[redacted]"), out)
}

func TestCloseState(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{
			{ID: "a1", Name: "write"}, {ID: "r1", Name: "read"}, {ID: "a2", Name: "write"},
		}}, testllm.Turn{Text: []string{"fine"}})
		var readRuns atomic.Int64
		tools := mixedTools(&readRuns)
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "go", Tools: tools})
		require.Len(t, res.Interrupts, 2)

		_, err := CloseState(res.State, []ToolResult{{CallID: "a1", Name: "write", Error: "cancelled"}})
		assert.Error(t, err, "every pending call needs a result")
		closed, err := CloseState(res.State, []ToolResult{{CallID: "a1", Name: "write", Error: "cancelled"}, {CallID: "a2", Name: "write", Output: json.RawMessage(`"skipped"`)}})
		require.NoError(t, err)
		assert.Len(t, srv.Requests(), 1, "CloseState sends nothing")

		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), State: closed, Prompt: "next", Tools: tools})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		calls, results := toolIDs(t, kind, reqs[1].Messages)
		assert.ElementsMatch(t, []string{"a1", "r1", "a2"}, calls)
		assert.ElementsMatch(t, calls, results)
		assert.Contains(t, string(reqs[1].Messages), "read-result")
		assert.Contains(t, string(reqs[1].Messages), "next")
		assert.EqualValues(t, 1, readRuns.Load())
	})
}

func TestDuplicateToolNames(t *testing.T) {
	srv := testllm.Start(t, "openai")
	noop := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	for name, tools := range map[string][]ToolSpec{
		"duplicate": {{Name: "x", Run: noop}, {Name: "x", Run: noop}},
		"bad name":  {{Name: "has space", Run: noop}},
		"too long":  {{Name: strings.Repeat("a", 65), Run: noop}},
	} {
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "test-key-1"), Prompt: "x", Tools: tools})
		require.NotNil(t, res.Err, name)
		assert.Equal(t, CodeInternal, res.Err.Code, name)
		assert.Equal(t, "error", last(t, evs).Kind, name)
	}
	assert.Empty(t, srv.Requests())
}

func TestTerminalEventAlwaysDelivered(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		var texts []string
		for i := range 100 {
			texts = append(texts, fmt.Sprintf("t%d ", i))
		}
		srv.Script(testllm.Turn{Text: texts, Hang: true})
		ctx, cancel := context.WithCancel(context.Background())
		events, wait := newEngine(t).Run(ctx, Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x"})
		time.Sleep(300 * time.Millisecond) // nobody reads; the buffer fills
		cancel()
		done := make(chan Result)
		go func() { done <- wait() }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("wait blocked")
		}
		var evs []Event
		for ev := range events {
			evs = append(evs, ev)
		}
		terminal := 0
		for _, ev := range evs {
			if ev.Kind == "done" || ev.Kind == "interrupted" || ev.Kind == "error" {
				terminal++
			}
		}
		assert.Equal(t, 1, terminal)
		assert.Equal(t, "error", last(t, evs).Kind)
		assert.GreaterOrEqual(t, len(evs), 64)
	})
}

func TestToolErrorRedacted(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "leaky"}}}, testllm.Turn{Text: []string{"ok"}})
		tools := []ToolSpec{{Name: "leaky", Run: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("auth failed for sekret-key-5")
		}}}
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "sekret-key-5"), Prompt: "x", Tools: tools})
		require.Nil(t, res.Err)
		rs := kindsOf(evs, "tool_result")
		require.Len(t, rs, 1)
		assert.Equal(t, "auth failed for [redacted]", rs[0].Result.Error)
		assert.NotContains(t, fmt.Sprint(evs), "sekret-key-5")
		assert.NotContains(t, string(res.State), "sekret-key-5")
		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		assert.NotContains(t, string(reqs[1].Messages), "sekret-key-5")
		assert.Contains(t, string(reqs[1].Messages), "[redacted]")
	})
}

func TestDuplicateCallIDs(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "d", Name: "read"}, {ID: "d", Name: "write"}}})
		var readRuns atomic.Int64
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x", Tools: mixedTools(&readRuns)})
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeBadResponse, res.Err.Code)
		assert.Equal(t, "error", last(t, evs).Kind)
		assert.Zero(t, readRuns.Load())
		assert.Empty(t, kindsOf(evs, "tool_call"))
	})
}

func TestResponsesMatchOneToOne(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "r1", Name: "read"}, {ID: "a1", Name: "write"}, {ID: "a2", Name: "write"}}})
		var readRuns atomic.Int64
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x", Tools: mixedTools(&readRuns)})
		require.Len(t, res.Interrupts, 2)
		ok := func(id string) ToolResult { return ToolResult{CallID: id, Name: "write", Output: json.RawMessage(`1`)} }
		for name, resps := range map[string][]ToolResult{
			"missing":       {ok("a1")},
			"extra":         {ok("a1"), ok("a2"), ok("zz")},
			"duplicate":     {ok("a1"), ok("a1"), ok("a2")},
			"name mismatch": {ok("a1"), {CallID: "a2", Name: "read", Output: json.RawMessage(`1`)}},
			"no name":       {ok("a1"), {CallID: "a2", Output: json.RawMessage(`1`)}},
			"already ran":   {ok("a1"), ok("a2"), {CallID: "r1", Name: "read", Output: json.RawMessage(`1`)}},
		} {
			evs, r := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), State: res.State, Tools: mixedTools(&readRuns), Responses: resps})
			require.NotNil(t, r.Err, name)
			assert.Equal(t, CodeInternal, r.Err.Code, name)
			assert.Equal(t, "error", last(t, evs).Kind, name)
			_, err := CloseState(res.State, resps)
			assert.Error(t, err, name)
		}
		assert.Len(t, srv.Requests(), 1)
		_, err := CloseState(res.State, []ToolResult{ok("a1"), ok("a2")})
		assert.NoError(t, err)
	})
}

func TestMalformedState(t *testing.T) {
	srv := testllm.Start(t, "openai")
	var readRuns atomic.Int64
	for _, st := range []string{
		`[null]`,
		`[{"role":"user"}]`,
		`[{"role":"user","content":[]}]`,
		`[{"role":"model","content":[null]}]`,
		`[{"role":"model","content":[{"toolRequest":{}}]}]`,
		`[{"role":"model","content":[{"toolRequest":{"name":"write"}}]}]`,
		`[{"role":"model","content":[{"toolRequest":{"ref":"a1"}}]}]`,
		`[{"role":"tool","content":[{"toolResponse":{}}]}]`,
		`[{"role":"robot","content":[{"text":"hi"}]}]`,
		`{"not":"a list"}`,
	} {
		assert.NotPanics(t, func() {
			_, err := decodeState([]byte(st))
			assert.Error(t, err, st)
			_, err = CloseState([]byte(st), []ToolResult{{CallID: "a1", Name: "write"}})
			assert.Error(t, err, st)
			_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "test-key-1"), State: []byte(st), Prompt: "x"})
			require.NotNil(t, res.Err, st)
			assert.Equal(t, CodeInternal, res.Err.Code, st)
			_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "test-key-1"), State: []byte(st), Tools: mixedTools(&readRuns),
				Responses: []ToolResult{{CallID: "a1", Name: "write"}}})
			require.NotNil(t, res.Err, st)
		}, st)
	}
	assert.Empty(t, srv.Requests())
}

func TestOpenCallsNeedResponses(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "a1", Name: "write"}}})
		var readRuns atomic.Int64
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x", Tools: mixedTools(&readRuns)})
		require.Len(t, res.Interrupts, 1)
		for _, prompt := range []string{"", "go on"} {
			evs, r := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), State: res.State, Prompt: prompt, Tools: mixedTools(&readRuns)})
			require.NotNil(t, r.Err, prompt)
			assert.Equal(t, CodeInternal, r.Err.Code, prompt)
			assert.Equal(t, "error", last(t, evs).Kind, prompt)
		}
		assert.Len(t, srv.Requests(), 1, "no model request")
	})
}

func TestRedactionIgnoresShortSecrets(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "leaky"}}})
	tools := []ToolSpec{{Name: "leaky", Run: func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("abc failed, key 12345678 rejected")
	}}}
	e, err := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), func() []string { return []string{"12345678"} })
	require.NoError(t, err)
	defer e.Close()
	evs, res := run(e, Request{Model: modelFor(srv, "openai", "abc"), Prompt: "x", Tools: tools})
	require.Nil(t, res.Err)
	rs := kindsOf(evs, "tool_result")
	require.Len(t, rs, 1)
	assert.Equal(t, "abc failed, key [redacted] rejected", rs[0].Result.Error)

	var buf bytes.Buffer
	h := &redactor{slog.NewTextHandler(&buf, nil), func() []string { return []string{"abc", "12345678"} }}
	slog.New(h).Error("abc and 12345678")
	assert.Contains(t, buf.String(), "abc and [redacted]")
}

func TestRunGate(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{
			{ID: "g1", Name: "gated"}, {ID: "d1", Name: "denied"}, {ID: "e1", Name: "failing"}, {ID: "o1", Name: "open"},
		}})
		var runs atomic.Int64
		count := func(context.Context, json.RawMessage) (any, error) { runs.Add(1); return "ran", nil }
		gate := func(d Decision, err error) func(context.Context, json.RawMessage) (Decision, error) {
			return func(context.Context, json.RawMessage) (Decision, error) { return d, err }
		}
		tools := []ToolSpec{
			{Name: "gated", Run: count, Gate: gate(Approve, nil)},
			{Name: "denied", Run: count, NeedsApproval: true, Gate: gate(Deny, nil)},
			{Name: "failing", Run: count, Gate: gate(Allow, errors.New("audit failed sekret-key-6"))},
			{Name: "open", Run: count, Gate: gate(Allow, nil)},
		}
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "sekret-key-6"), Prompt: "go", Tools: tools})
		require.Nil(t, res.Err)
		end := last(t, evs)
		require.Equal(t, "interrupted", end.Kind)
		require.Len(t, end.Interrupts, 1)
		assert.Equal(t, "g1", end.Interrupts[0].ID)
		assert.EqualValues(t, 1, runs.Load(), "only the allowed tool runs")
		errs := map[string]string{}
		for _, ev := range kindsOf(evs, "tool_result") {
			errs[ev.Result.CallID] = ev.Result.Error
		}
		assert.Equal(t, map[string]string{"d1": "tool call denied", "e1": "audit failed [redacted]", "o1": ""}, errs)
	})
}

func TestInvalidToolArgsGoBackToModel(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"limit":50}`}}},
			testllm.Turn{Calls: []testllm.Call{{ID: "c2", Name: "search", Args: `{"limit":5}`}}},
			testllm.Turn{Text: []string{"found"}})
		var runs atomic.Int64
		tools := []ToolSpec{{Name: "search", Description: "search",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "maximum": 20}}},
			Run:    func(context.Context, json.RawMessage) (any, error) { runs.Add(1); return "ok", nil }}}
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "find", Tools: tools})
		require.Nil(t, res.Err, "an invalid argument does not end the run")
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.Equal(t, int64(1), runs.Load(), "the invalid call never reaches the tool")
		results := kindsOf(evs, "tool_result")
		require.Len(t, results, 2)
		assert.Equal(t, "c1", results[0].Result.CallID)
		assert.Equal(t, "search", results[0].Result.Name)
		assert.Contains(t, results[0].Result.Error, "invalid arguments")
		assert.Empty(t, results[1].Result.Error)
		require.Len(t, kindsOf(evs, "tool_call"), 2)

		reqs := srv.Requests()
		require.Len(t, reqs, 3)
		assert.Contains(t, string(reqs[1].Messages), "invalid arguments", "the model is told why")
		calls, ids := toolIDs(t, kind, reqs[2].Messages)
		assert.ElementsMatch(t, calls, ids)
		assert.Contains(t, string(reqs[0].Tools), "maximum", "the model still sees the real schema")
	})
}

// Final review #3: a reply the service cut off or withheld is an error, not
// a finished turn: no done, and the state stays as it was.
func TestAbnormalFinishIsIncomplete(t *testing.T) {
	cases := map[string][]string{"openai": {"length", "content_filter", "error"}, "anthropic": {"max_tokens"}}
	eachKind(t, func(t *testing.T, kind string) {
		for _, reason := range cases[kind] {
			t.Run(reason, func(t *testing.T) {
				e := newEngine(t)
				srv := testllm.Start(t, kind)
				srv.Script(testllm.Turn{Text: []string{"half a transla"}, Finish: reason})
				evs, res := run(e, Request{Model: modelFor(srv, kind, "k"), Prompt: "translate", MaxTurns: 1})
				require.NotNil(t, res.Err)
				assert.Equal(t, CodeIncomplete, res.Err.Code)
				assert.Nil(t, res.State)
				assert.Equal(t, "error", last(t, evs).Kind)
				assert.Empty(t, kindsOf(evs, "done"))
			})
		}
	})
}

// Residual R2: a finish reason Genkit does not recognise is a normal end.
func TestUnknownFinishIsDone(t *testing.T) {
	cases := map[string]string{"openai": "something_new", "anthropic": "pause_turn"}
	eachKind(t, func(t *testing.T, kind string) {
		e := newEngine(t)
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"fine"}, Finish: cases[kind]})
		evs, res := run(e, Request{Model: modelFor(srv, kind, "k"), Prompt: "hi", MaxTurns: 1})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.NotNil(t, res.State)
	})
}

// Residual R1: Forget ends a run even when its reader stopped reading and
// the event buffer is full.
func TestForgetEndsRunWithStalledReader(t *testing.T) {
	e := newEngine(t)
	srv := testllm.Start(t, "openai")
	chunks := make([]string, eventBuffer+20)
	for i := range chunks {
		chunks[i] = "x"
	}
	srv.Script(testllm.Turn{Text: chunks, Hang: true})
	m := modelFor(srv, "openai", "k")
	events, wait := e.Run(context.Background(), Request{Model: m, Prompt: "hi", MaxTurns: 1})
	require.Eventually(t, func() bool { return len(events) >= eventBuffer }, 10*time.Second, 5*time.Millisecond)
	e.Forget(m.ServiceID)
	done := make(chan Result, 1)
	go func() { done <- wait() }()
	select {
	case res := <-done:
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeStale, res.Err.Code)
	case <-time.After(5 * time.Second):
		srv.Release()
		t.Fatal("the run did not end after Forget")
	}
	for range events { // the terminal event is still delivered
	}
}

// Final whole-branch review #5: an escaped "/" in the base path reaches the
// service as it is shown in the settings preview (ai.Endpoint), for every kind.
func TestEscapedBasePathKept(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		uris := make(chan string, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uris <- r.RequestURI
			http.Error(w, "no", http.StatusBadRequest) // not retried
		}))
		t.Cleanup(srv.Close)
		base := srv.URL + "/a%2Fb/v1"
		_, res := run(newEngine(t), Request{Model: ResolvedModel{ServiceID: 1, Version: 1, Kind: kind, BaseURL: base, Model: "m", APIKey: "k"}, Prompt: "hi", MaxTurns: 1})
		require.NotNil(t, res.Err)
		assert.Equal(t, strings.TrimPrefix(Endpoint(kind, base), srv.URL), <-uris)
	})
}

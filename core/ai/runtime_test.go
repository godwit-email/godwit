package ai

import (
	"context"
	"encoding/base64"
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

// asking is the view of a run asking prompt after h (nil: a new conversation).
func asking(h *History, prompt string) *Sent {
	if h == nil {
		h = NewHistory()
	}
	return must(h.Send(1<<20, Summary{}, false)).Ask(prompt, "")
}

// resuming is the view of a run resuming h.
func resuming(h *History) *Sent { return must(h.Send(1<<20, Summary{}, false)) }

// after is the history a run that sent s ended with.
func after(t *testing.T, s *Sent, res Result) *History {
	t.Helper()
	h, _, err := s.Restore(res)
	require.NoError(t, err)
	return h
}

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
		first := asking(nil, "hi there")
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), System: "be brief", History: first})
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
		assert.NotEmpty(t, res.out)
		reqs := srv.Requests()
		require.Len(t, reqs, 1)
		assert.Contains(t, string(reqs[0].System), "be brief")
		assert.Contains(t, string(reqs[0].Messages), "hi there")

		// the history continues the conversation, with one system prompt
		srv.Script(testllm.Turn{Text: []string{"again"}})
		_, res2 := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), System: "be brief", History: asking(after(t, first, res), "more")})
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
			results[callRef(ev.Result.CallID)] = ev.Result
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

// TestRedactsSecretAddedLater: a secret that joins the set after the engine is
// created (a plugin installed later) is redacted from what a run reports.
func TestRedactsSecretAddedLater(t *testing.T) {
	var secrets atomic.Pointer[[]string]
	secrets.Store(&[]string{})
	e, err := NewEngine(slog.Default(), func() []string { return *secrets.Load() })
	require.NoError(t, err)
	defer e.Close()
	secrets.Store(&[]string{"plugin-sekret-42"})
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "leaky"}}}, testllm.Turn{Text: []string{"ok"}})
	tools := []ToolSpec{{Name: "leaky", Run: func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("denied plugin-sekret-42")
	}}}
	evs, res := run(e, Request{Model: modelFor(srv, "openai", "k"), Prompt: "x", Tools: tools})
	require.Nil(t, res.Err)
	assert.NotContains(t, fmt.Sprint(evs), "plugin-sekret-42")
	assert.Equal(t, "denied [redacted]", kindsOf(evs, "tool_result")[0].Result.Error)
}

func TestCloseHistory(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{
			{ID: "a1", Name: "write"}, {ID: "r1", Name: "read"}, {ID: "a2", Name: "write"},
		}}, testllm.Turn{Text: []string{"fine"}})
		var readRuns atomic.Int64
		tools := mixedTools(&readRuns)
		sent := asking(nil, "go")
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), History: sent, Tools: tools})
		require.Len(t, res.Interrupts, 2)
		h := after(t, sent, res)
		a1, a2 := res.Interrupts[0].ID, res.Interrupts[1].ID

		_, _, err := h.Close([]ToolResult{{CallID: a1, Name: "write", Error: "cancelled"}})
		assert.Error(t, err, "every interrupted call needs a result")
		closed, _, err := h.Close([]ToolResult{{CallID: a1, Name: "write", Error: "cancelled"}, {CallID: a2, Name: "write", Output: json.RawMessage(`"skipped"`)}})
		require.NoError(t, err)
		assert.Len(t, srv.Requests(), 1, "Close sends nothing")

		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), History: asking(closed, "next"), Tools: tools})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		calls, results := toolIDs(t, kind, reqs[1].Messages)
		assert.ElementsMatch(t, []string{"a1", "r1", "a2"}, calls)
		assert.ElementsMatch(t, calls, results)
		assert.Contains(t, string(reqs[1].Messages), notRun, "the read held back with them never ran")
		assert.Contains(t, string(reqs[1].Messages), "next")
		assert.Zero(t, readRuns.Load())
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
		defer cancel()
		events, wait := newEngine(t).Run(ctx, Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "x"})
		require.Eventually(t, func() bool { return len(events) >= eventBuffer }, 10*time.Second, 5*time.Millisecond)
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
		assert.NotContains(t, string(must(json.Marshal(res.out))), "sekret-key-5")
		reqs := srv.Requests()
		require.Len(t, reqs, 2)
		assert.NotContains(t, string(reqs[1].Messages), "sekret-key-5")
		assert.Contains(t, string(reqs[1].Messages), "[redacted]")
	})
}

// A round that repeats a call ID runs none of it. (pi-ai's Chat Completions
// parser joins the deltas of calls with one ID into one call.)
func TestDuplicateCallIDs(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		if kind == "openai" {
			t.Skip("one call to pi-ai")
		}
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
		sent := asking(nil, "x")
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), History: sent, Tools: mixedTools(&readRuns)})
		require.Len(t, res.Interrupts, 2)
		a1, a2 := res.Interrupts[0].ID, res.Interrupts[1].ID
		h := after(t, sent, res)
		r1 := h.OpenCalls()[0].CallID
		ok := func(id string) ToolResult { return ToolResult{CallID: id, Name: "write", Output: json.RawMessage(`1`)} }
		for name, resps := range map[string][]ToolResult{
			"missing":       {ok(a1)},
			"extra":         {ok(a1), ok(a2), ok("zz")},
			"duplicate":     {ok(a1), ok(a1), ok(a2)},
			"name mismatch": {ok(a1), {CallID: a2, Name: "read", Output: json.RawMessage(`1`)}},
			"no name":       {ok(a1), {CallID: a2, Output: json.RawMessage(`1`)}},
			"held back":     {ok(a1), ok(a2), {CallID: r1, Name: "read", Output: json.RawMessage(`1`)}},
		} {
			evs, r := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), History: resuming(h), Tools: mixedTools(&readRuns), Responses: resps})
			require.NotNil(t, r.Err, name)
			assert.Equal(t, CodeInternal, r.Err.Code, name)
			assert.Equal(t, "error", last(t, evs).Kind, name)
			if name != "held back" { // Close may answer a call held back with them
				_, _, err := h.Close(resps)
				assert.Error(t, err, name)
			}
		}
		assert.Len(t, srv.Requests(), 1)
		_, _, err := h.Close([]ToolResult{ok(a1), ok(a2)})
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
			_, _, _, err = LegacyHistory([]byte(st), nil)
			assert.Error(t, err, st)
			if strings.HasPrefix(st, "[") && st != "[null]" {
				_, err = LoadHistory([]StoredMessage{{Seq: 1, Data: []byte(st[1 : len(st)-1])}}, 2)
				assert.Error(t, err, st)
			}
			_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "test-key-1"), Tools: mixedTools(&readRuns),
				Responses: []ToolResult{{CallID: "a1", Name: "write"}}})
			require.NotNil(t, res.Err, st)
		}, st)
	}
	assert.Empty(t, srv.Requests())
}

func TestStateKeepsLargeIntegers(t *testing.T) {
	st := `[{"role":"model","content":[{"toolRequest":{"name":"read","ref":"a1","input":{"thread":1278455344230334865}}}]},` +
		`{"role":"tool","content":[{"toolResponse":{"name":"read","ref":"a1","output":{"thread":1278455344230334865}}}]}]`
	msgs, err := decodeState([]byte(st))
	require.NoError(t, err)
	b, err := json.Marshal(msgs)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(b), `{"thread":1278455344230334865}`), string(b))
}

func TestOpenCallsNeedResponses(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "a1", Name: "write"}}})
		var readRuns atomic.Int64
		sent := asking(nil, "x")
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), History: sent, Tools: mixedTools(&readRuns)})
		require.Len(t, res.Interrupts, 1)
		h := after(t, sent, res)
		for _, prompt := range []string{"", "go on"} {
			view := resuming(h)
			if prompt != "" {
				view = asking(h, prompt)
			}
			evs, r := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), History: view, Tools: mixedTools(&readRuns)})
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
}

// Gates decide each call of a round before any runs: one asking for approval
// holds the round; on the resume the others' gates decide again.
func TestRunGate(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{
			{ID: "g1", Name: "gated"}, {ID: "d1", Name: "denied"}, {ID: "e1", Name: "failing"}, {ID: "o1", Name: "open"},
		}}, testllm.Turn{Text: []string{"done"}})
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
		sent := asking(nil, "go")
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "sekret-key-6"), History: sent, Tools: tools})
		require.Nil(t, res.Err)
		end := last(t, evs)
		require.Equal(t, "interrupted", end.Kind)
		require.Len(t, end.Interrupts, 1)
		assert.Equal(t, "g1", callRef(end.Interrupts[0].ID))
		assert.Zero(t, runs.Load(), "nothing runs before the approval")

		evs, res = run(newEngine(t), Request{Model: modelFor(srv, kind, "sekret-key-6"), History: resuming(after(t, sent, res)), Tools: tools,
			Responses: []ToolResult{{CallID: end.Interrupts[0].ID, Name: "gated", Output: json.RawMessage(`"approved"`)}}})
		require.Nil(t, res.Err)
		assert.EqualValues(t, 1, runs.Load(), "only the allowed tool runs")
		errs := map[string]string{}
		for _, ev := range kindsOf(evs, "tool_result") {
			errs[callRef(ev.Result.CallID)] = ev.Result.Error
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
		assert.Equal(t, "c1", callRef(results[0].Result.CallID))
		assert.Equal(t, "search", results[0].Result.Name)
		assert.Contains(t, results[0].Result.Error, "Validation failed")
		assert.Empty(t, results[1].Result.Error)
		require.Len(t, kindsOf(evs, "tool_call"), 2)

		reqs := srv.Requests()
		require.Len(t, reqs, 3)
		assert.Contains(t, string(reqs[1].Messages), "must be <= 20", "the model is told why")
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
				assert.Nil(t, res.out)
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
		assert.Equal(t, "fine", res.Messages[1].Text)
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
		uri, _, _ := strings.Cut(<-uris, "?") // pi-ai asks Anthropic's ?beta=true
		assert.Equal(t, strings.TrimPrefix(Endpoint(kind, base), srv.URL), uri)
	})
}

// An Anthropic base entered with "/v1" reaches the same /v1/messages as one without.
func TestAnthropicBaseWithV1(t *testing.T) {
	e, err := NewEngine(nil, nil)
	require.NoError(t, err)
	defer e.Close()
	srv := testllm.Start(t, "anthropic")
	m := modelFor(srv, "anthropic", "k")
	m.BaseURL += "/v1"
	_, res := run(e, Request{Model: m, Prompt: "x"})
	require.Nil(t, res.Err)
	require.Len(t, srv.Requests(), 1)
	ids, err := ListModels(context.Background(), nil, KindAnthropic, m.BaseURL, "k")
	require.NoError(t, err)
	assert.NotNil(t, ids)
}

// TestRunContextHidden: Context goes to the model in the user's turn, before
// the prompt, and stays in the state, but Messages show only the prompt.
func TestInstanceKeyChangeKeepsOldRuns(t *testing.T) {
	e := newEngine(t)
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"first"}, Hang: true}, testllm.Turn{Text: []string{"second"}})
	m1 := modelFor(srv, KindResponses, "token-1")
	events1, wait1 := e.Run(context.Background(), Request{Model: m1, Prompt: "one", MaxTurns: 1})
	require.Eventually(t, func() bool { return len(srv.Requests()) == 1 }, 5*time.Second, 5*time.Millisecond)

	m2 := m1
	m2.APIKey = "token-2"
	evs2, res2 := run(e, Request{Model: m2, Prompt: "two", MaxTurns: 1})
	require.Nil(t, res2.Err)
	assert.Equal(t, "done", last(t, evs2).Kind)
	assert.EqualValues(t, 2, e.inits.Load())
	e.mu.Lock()
	var old *instance
	for in := range e.retired {
		old = in
	}
	e.mu.Unlock()
	require.NotNil(t, old, "the replaced instance waits for its run")
	assert.NoError(t, old.ctx.Err())

	srv.Release()
	evs1, res1 := collect(events1, wait1)
	require.Nil(t, res1.Err)
	assert.Equal(t, "done", last(t, evs1).Kind)
	assert.Error(t, old.ctx.Err(), "and goes once that run is over")
	e.mu.Lock()
	assert.Empty(t, e.retired)
	e.mu.Unlock()
}

func TestForgetCancelsRetiredInstance(t *testing.T) {
	e := newEngine(t)
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"first"}, Hang: true}, testllm.Turn{Text: []string{"second"}})
	m1 := modelFor(srv, KindResponses, "token-1")
	events1, wait1 := e.Run(context.Background(), Request{Model: m1, Prompt: "one", MaxTurns: 1})
	require.Eventually(t, func() bool { return len(srv.Requests()) == 1 }, 5*time.Second, 5*time.Millisecond)
	m2 := m1
	m2.APIKey = "token-2"
	_, res2 := run(e, Request{Model: m2, Prompt: "two", MaxTurns: 1})
	require.Nil(t, res2.Err)

	e.Forget(m1.ServiceID) // the service changed or went: the run on its older key ends too
	_, res1 := collect(events1, wait1)
	require.NotNil(t, res1.Err)
	assert.Equal(t, CodeStale, res1.Err.Code)
	e.mu.Lock()
	assert.Empty(t, e.retired)
	e.mu.Unlock()
}

func TestCodexRequestShape(t *testing.T) {
	e := newEngine(t)
	srv := testllm.Start(t, KindResponses)
	srv.Script(testllm.Turn{Text: []string{"OK"}})
	tok := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-9"}}`)) + ".s"
	m := modelFor(srv, KindResponses, tok)
	m.BaseURL = srv.URL + "/backend-api/codex"
	_, res := run(e, Request{Model: m, Prompt: "hi", MaxTurns: 1})
	require.Nil(t, res.Err)
	req := srv.Requests()[0]
	assert.Equal(t, "/backend-api/codex/responses", req.Path)
	assert.Equal(t, "acct-9", req.Header.Get("chatgpt-account-id"))
	assert.Equal(t, "godwit", req.Header.Get("originator"))
	assert.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
	assert.Contains(t, string(req.Body), `"instructions":"You are a helpful assistant."`)
	assert.Contains(t, string(req.Body), `"store":false`)

	for _, turn := range []testllm.Turn{{Status: http.StatusTooManyRequests}, {Status: http.StatusBadRequest, Code: "usage_not_included"}} {
		srv.Script(turn)
		_, res = run(e, Request{Model: m, Prompt: "hi", MaxTurns: 1})
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeRateLimited, res.Err.Code)
		assert.Equal(t, DetailChatGPTUsage, res.Err.Detail)
	}
	// Not the Codex backend: a 429 is an ordinary rate limit, with no plan to manage.
	srv.Script(testllm.Turn{Status: http.StatusTooManyRequests, Code: "rate_limit_exceeded"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "sk-1"), Prompt: "hi", MaxTurns: 1})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeRateLimited, res.Err.Code)
	assert.Empty(t, res.Err.Detail)
}

func TestUsageLimitIsRateLimitedWithDetail(t *testing.T) {
	e := newEngine(t)
	srv := testllm.Start(t, KindResponses)
	for _, turn := range []testllm.Turn{
		{FailCode: "subscription_sharing_usage_limit_exceeded"},
		{FailCode: "subscription_sharing_usage_unavailable", Fail: "UPSTREAM-BODY quota out"},
	} {
		srv.Script(turn)
		evs, res := run(e, Request{Model: modelFor(srv, KindResponses, "token-1"), Prompt: "hi", MaxTurns: 1})
		require.NotNil(t, res.Err, turn.FailCode)
		assert.Equal(t, CodeRateLimited, res.Err.Code, turn.FailCode)
		assert.Equal(t, DetailChatGPTUsage, res.Err.Detail, turn.FailCode)
		assert.NotContains(t, res.Err.Msg, "UPSTREAM-BODY")
		assert.Equal(t, "error", last(t, evs).Kind)
	}

	// The same codes as HTTP errors, and the account that may not share its plan at all.
	srv.Script(testllm.Turn{Status: http.StatusTooManyRequests, Code: "subscription_sharing_usage_limit_exceeded"})
	_, res := run(e, Request{Model: modelFor(srv, KindResponses, "token-1"), Prompt: "hi", MaxTurns: 1})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeRateLimited, res.Err.Code)
	assert.Equal(t, DetailChatGPTUsage, res.Err.Detail)
	srv.Script(testllm.Turn{Status: http.StatusForbidden, Code: "subscription_sharing_user_not_eligible"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "token-1"), Prompt: "hi", MaxTurns: 1})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeUnauthorized, res.Err.Code)
	assert.Equal(t, DetailChatGPTEligibility, res.Err.Detail)
	assert.NotContains(t, res.Err.Msg, "token-1")

	srv.Script(testllm.Turn{Fail: "x"})
	_, res = run(e, Request{Model: modelFor(srv, KindResponses, "token-1"), Prompt: "hi", MaxTurns: 1})
	require.NotNil(t, res.Err)
	assert.Equal(t, CodeBadResponse, res.Err.Code)
	assert.Empty(t, res.Err.Detail)
}

func TestRequestArgsRewriteBeforeValidation(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "open", Args: `{"id":12345678901,"url":"ref:1"}`}}},
			testllm.Turn{Text: []string{"opened"}})
		var got atomic.Value
		tools := []ToolSpec{{Name: "open", Description: "open",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string", "pattern": "^https://"}}},
			Run:    func(_ context.Context, args json.RawMessage) (any, error) { got.Store(string(args)); return "ok", nil }}}
		rewrite := func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(strings.ReplaceAll(string(args), "ref:1", "https://a.com/x")), nil
		}
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "go", Tools: tools, Args: rewrite})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.JSONEq(t, `{"id":12345678901,"url":"https://a.com/x"}`, got.Load().(string), "the schema saw the rewritten value")
		assert.Contains(t, got.Load().(string), `"id":12345678901`, "an integer is not re-encoded as a float")
		assert.Contains(t, string(srv.Requests()[1].Messages), "ref:1", "the state keeps what the model wrote")
	})
}

func TestRequestArgsErrorRefusesCall(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "open", Args: `{"url":"x"}`}}},
			testllm.Turn{Text: []string{"ok"}})
		var runs atomic.Int64
		tools := []ToolSpec{{Name: "open", Description: "open",
			Run: func(context.Context, json.RawMessage) (any, error) { runs.Add(1); return "ok", nil }}}
		refuse := func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("no such link")
		}
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "test-key-1"), Prompt: "go", Tools: tools, Args: refuse})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.Zero(t, runs.Load(), "the tool never runs")
		results := kindsOf(evs, "tool_result")
		require.Len(t, results, 1)
		assert.Equal(t, "no such link", results[0].Result.Error)
		assert.Contains(t, string(srv.Requests()[1].Messages), "no such link", "the model is told why")
	})
}

// toolChoice is a raw request's tool_choice: "none" for Anthropic's {"type":"none"}.
func toolChoice(t *testing.T, body json.RawMessage) string {
	t.Helper()
	var b struct {
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	require.NoError(t, json.Unmarshal(body, &b))
	var s string
	if json.Unmarshal(b.ToolChoice, &s) == nil {
		return s
	}
	var o struct{ Type string }
	json.Unmarshal(b.ToolChoice, &o)
	return o.Type
}

func limitTurns(text string, calls ...string) []testllm.Turn {
	var turns []testllm.Turn
	for _, id := range calls {
		turns = append(turns, testllm.Turn{Calls: []testllm.Call{{ID: id, Name: "lookup", Args: `{}`}}, Usage: [2]int{1, 1}})
	}
	return append(turns, testllm.Turn{Text: []string{text}, Usage: [2]int{1, 1}})
}

func lookupTool(runs *atomic.Int64) []ToolSpec {
	return []ToolSpec{{Name: "lookup", Description: "look up", Run: func(context.Context, json.RawMessage) (any, error) {
		runs.Add(1)
		return "looked-up", nil
	}}}
}

// #219: at the tool-call limit the run asks once more, with tools off, and
// ends with that answer instead of an error.
func TestRunAnswersAtToolLimit(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(limitTurns("what I found so far", "c1", "c2", "c3")...)
		var runs atomic.Int64
		sent := asking(nil, "find it")
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "k"), System: "be brief", History: sent, Tools: lookupTool(&runs), MaxTurns: 2})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.EqualValues(t, 2, runs.Load(), "the third call never runs")

		reqs := srv.Requests()
		require.Len(t, reqs, 4)
		final := reqs[3]
		assert.Empty(t, final.Invalid)
		assert.Equal(t, "none", toolChoice(t, final.Body))
		assert.Contains(t, string(final.Tools), "look up")
		system := string(final.System)
		if kind == "openai" {
			system = string(final.Messages)
		}
		assert.Contains(t, system, "be brief")
		assert.Contains(t, system, "tool-call limit")
		calls, results := toolIDs(t, kind, final.Messages)
		assert.Equal(t, []string{"c1", "c2"}, calls)
		assert.Equal(t, []string{"c1", "c2"}, results)

		var got []string
		for _, m := range after(t, sent, res).Messages() {
			for _, c := range m.Calls {
				got = append(got, "call "+callRef(c.ID))
			}
			for _, r := range m.Results {
				got = append(got, "result "+callRef(r.CallID))
			}
			if m.Text != "" {
				got = append(got, m.Role+": "+m.Text)
			}
		}
		assert.Equal(t, []string{"user: find it", "call c1", "result c1", "call c2", "result c2", "model: what I found so far"}, got)
		assert.Equal(t, 4, res.Usage.Input, "the closing call counts too")
	})
}

// #219: a model that still calls a tool when asked to answer fails the run as
// incomplete; nothing runs and nothing is kept, but its calls are counted.
func TestRunToolLimitWithoutAnswer(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(limitTurns("unused", "c1", "c2", "c3", "c4")...)
		var runs atomic.Int64
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "k"), History: asking(nil, "find it"), Tools: lookupTool(&runs), MaxTurns: 2})
		require.NotNil(t, res.Err)
		assert.Equal(t, CodeIncomplete, res.Err.Code)
		assert.Equal(t, "the model reached its tool-call limit without answering", res.Err.Msg)
		assert.Equal(t, "error", last(t, evs).Kind)
		assert.EqualValues(t, 2, runs.Load())
		assert.Nil(t, res.out)
		assert.Len(t, srv.Requests(), 4)
		assert.Equal(t, 4, res.Usage.Input)
	})
}

// #219: a resumed run counts its rounds from the resume, the answered round
// not one of them (#233), and at its limit answers and restores as a resume
// that ended normally does.
func TestResumedRunAnswersAtToolLimit(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "a1", Name: "write", Args: `{}`}}})
		var runs atomic.Int64
		tools := append(lookupTool(&runs), ToolSpec{Name: "write", Description: "writes", NeedsApproval: true})
		sent := asking(nil, "write it")
		_, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "k"), History: sent, Tools: tools, MaxTurns: 2})
		require.Len(t, res.Interrupts, 1)

		srv.Script(limitTurns("written, then looked up", "c1", "c2")...)
		resumed := resuming(after(t, sent, res))
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, kind, "k"), History: resumed, Tools: tools, MaxTurns: 1,
			Responses: []ToolResult{{CallID: res.Interrupts[0].ID, Name: "write", Output: json.RawMessage(`"ok"`)}}})
		require.Nil(t, res.Err)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.EqualValues(t, 1, runs.Load(), "c2 is past the limit")
		msgs := after(t, resumed, res).Messages()
		assert.Equal(t, "written, then looked up", msgs[len(msgs)-1].Text)
		reqs := srv.Requests()
		require.Len(t, reqs, 4)
		assert.Equal(t, "none", toolChoice(t, reqs[3].Body))
		_, results := toolIDs(t, kind, reqs[3].Messages)
		assert.Equal(t, []string{"a1", "c1"}, results)
	})
}

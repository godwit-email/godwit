package ai

import (
	"context"
	"encoding/json"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type modelTestTransport func(*http.Request) (*http.Response, error)

func (f modelTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPreparingClient(t *testing.T) {
	originals := []string{}
	base := &http.Client{Transport: modelTestTransport(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, `{"tools":["real","placeholder"]}`, string(b))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}, Request: r}, nil
	})}
	c := PreparingClient(base, func(ctx context.Context, r *http.Request) (*http.Request, error) {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		originals = append(originals, string(b))
		return http.NewRequestWithContext(ctx, "POST", r.URL.String(), strings.NewReader(`{"tools":["real","placeholder"]}`))
	})
	req, err := http.NewRequest("POST", "https://example.com", strings.NewReader(`{"tools":["real"]}`))
	require.NoError(t, err)
	for range 2 {
		res, err := c.Do(req)
		require.NoError(t, err)
		res.Body.Close()
	}
	require.Equal(t, []string{`{"tools":["real"]}`, `{"tools":["real"]}`}, originals)
	b, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"tools":["real"]}`, string(b))
}

func TestPluginModelsProtocolIsolation(t *testing.T) {
	e := newEngine(t)
	a := ResolvedModel{Provider: "plugin", Version: 1, Kind: KindOpenAI, BaseURL: "https://example.com/v1", Model: "a"}
	b := a
	b.Kind = KindResponses
	first, err := e.instance(t.Context(), a)
	require.NoError(t, err)
	second, err := e.instance(t.Context(), b)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.NoError(t, first.ctx.Err())
	e.ForgetProvider("plugin")
	require.Error(t, first.ctx.Err())
	require.Error(t, second.ctx.Err())
}
func TestPluginModelSessionStable(t *testing.T) {
	e := newEngine(t)
	var sessions []ModelSession
	e.ClientFor = func(_ ResolvedModel, _ *http.Client) *http.Client {
		return &http.Client{Transport: modelTestTransport(func(r *http.Request) (*http.Response, error) {
			sessions = append(sessions, ModelSessionFromContext(r.Context()))
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}, Request: r}, nil
		})}
	}
	_, _ = run(e, Request{Model: ResolvedModel{Provider: "p", Kind: KindOpenAI, BaseURL: "https://example.com", Model: "a"}, Session: ModelSession{ID: "persistent", CreatedAt: 123}, Prompt: "hi"})
	require.NotEmpty(t, sessions)
	for _, s := range sessions {
		require.Equal(t, ModelSession{ID: "persistent", CreatedAt: 123}, s)
	}
}

func TestPluginModelPlaceholderDenied(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		e := newEngine(t)
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "real", Args: `{}`}, {Name: "placeholder", Args: `{}`}}})
		var executions atomic.Int64
		_, result := run(e, Request{Model: modelFor(srv, kind, ""), Prompt: "hi", Tools: []ToolSpec{{Name: "real", Description: "real", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (any, error) { executions.Add(1); return "ok", nil }}}})
		require.NotNil(t, result.Err)
		require.Zero(t, executions.Load())
		require.Empty(t, result.State)
	})
}

func TestPluginModelsBaseURLIsolation(t *testing.T) {
	e := newEngine(t)
	a := ResolvedModel{Provider: "plugin", Version: 1, Kind: KindOpenAI, BaseURL: "https://a.example/v1", Model: "a"}
	b := a
	b.Model, b.BaseURL = "b", "https://b.example/v1"
	first, err := e.instance(t.Context(), a)
	require.NoError(t, err)
	second, err := e.instance(t.Context(), b)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.NoError(t, first.ctx.Err(), "another model's base URL must not cancel this run")
	e.ForgetProvider("plugin")
	require.Error(t, first.ctx.Err())
	require.Error(t, second.ctx.Err())
}

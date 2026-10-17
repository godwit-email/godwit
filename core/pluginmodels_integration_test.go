package core

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/require"
)

func TestPluginModelsEndToEnd(t *testing.T) {
	t.Run("script", func(t *testing.T) {
		e := newApprovalEnv(t)
		backend := testllm.Start(t, ai.KindOpenAI)
		backend.Script(testllm.Turn{Calls: []testllm.Call{{ID: "test-call", Name: "echo", Args: `{}`}}}, testllm.Turn{Text: []string{"done"}})
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(": keep-alive\n\n"))
			w.(http.Flusher).Flush()
			backend.Mux.ServeHTTP(w, r)
		}))
		defer server.Close()
		e.s.pluginNet = plugins.NetOptions{Resolver: modelIntegrationDNS{}, AllowLoopback: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		}}
		dir, err := filepath.Abs("coretest/testdata/models-demo")
		require.NoError(t, err)
		_, err = e.s.InstallPlugin(bg, dir)
		require.NoError(t, err)
		require.NoError(t, e.s.GrantPermission(bg, "models-demo"))
		require.NoError(t, e.s.SetPluginEnabled(bg, "models-demo", true))
		e.s.running["models-demo"].modelNet.Transport().TLSClientConfig.InsecureSkipVerify = true // transport is pinned to the loopback fixture
		catalog, err := e.s.ListModels(bg)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(catalog), 2)
		settings, err := e.s.GetSettings(bg)
		require.NoError(t, err)
		settings.DefaultModel = "models-demo/m-1"
		settings.JudgeModel = "models-demo/judge"
		_, err = e.s.SetSettings(bg, settings)
		require.NoError(t, err)
		model, err := e.s.resolveModel(bg, "")
		require.NoError(t, err)
		engine, err := e.s.aiEngine()
		require.NoError(t, err)
		var calls atomic.Int64
		stream, wait := engine.Run(bg, ai.Request{Model: model, Prompt: "test", Tools: []ai.ToolSpec{{Name: "echo", Description: "Public fixture", Run: func(context.Context, json.RawMessage) (any, error) { calls.Add(1); return "fixed", nil }}}})
		for range stream {
		}
		require.Nil(t, wait().Err)
		require.Equal(t, int64(1), calls.Load())
		judge, ref, gen, err := e.s.resolveJudge(bg)
		require.NoError(t, err)
		require.Equal(t, "models-demo/judge", ref)
		require.False(t, gen)
		answers, err := judge.Ask(bg, "public fixture", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "Is it a fixture?"}})
		require.NoError(t, err)
		require.Len(t, answers, 1)
	})
	t.Run("native", func(t *testing.T) {
		s, _ := nativeAssetService(t)
		status, err := s.PrepareModel(bg, "assetjudge/a")
		var me *ModelError
		require.ErrorAs(t, err, &me)
		require.Equal(t, "update_required", status.Error.Code)
		s.assets = &fakeModelAssets{}
		_, err = s.PrepareModel(bg, "assetjudge/a")
		require.NoError(t, err)
		judge, ref, gen, err := s.resolveJudge(bg)
		require.NoError(t, err)
		require.Equal(t, "assetjudge/a", ref)
		require.False(t, gen)
		answers, err := judge.Ask(bg, "public fixture", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "Is it a fixture?"}})
		require.NoError(t, err)
		require.Len(t, answers, 1)
	})
}

package core

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/require"
)

func TestZenFreeVM(t *testing.T) {
	dist := os.Getenv("GODWIT_ZEN_FREE_DIST")
	if dist == "" {
		t.Skip("set GODWIT_ZEN_FREE_DIST to the built plugin")
	}
	e := newApprovalEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"free"}]}`))
		case strings.HasSuffix(r.URL.Path, "api.json"):
			w.Write([]byte(`{"opencode":{"npm":"@ai-sdk/openai-compatible","models":{"free":{"name":"Free","cost":{"input":0,"output":0}}}}}`))
		default:
			w.Write([]byte("| Free | free | `https://opencode.ai/inference/v1/chat/completions` | sdk |\n| Free | Free | Free |"))
		}
	}))
	defer srv.Close()
	e.s.pluginNet = plugins.NetOptions{Resolver: modelIntegrationDNS{}, AllowLoopback: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	_, err := e.s.InstallPlugin(bg, dist)
	require.NoError(t, err)
	require.NoError(t, e.s.GrantPermission(bg, "zen-free"))
	require.NoError(t, e.s.SetPluginEnabled(bg, "zen-free", true))
	e.s.running["zen-free"].modelNet.Transport().TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // fixture transport is pinned to the loopback server
	rows, err := e.s.ListModels(bg)
	require.NoError(t, err)
	rows = slices.DeleteFunc(rows, func(m ListedModel) bool { return !strings.HasPrefix(m.Ref, "zen-free/") })
	require.Len(t, rows, 1)
	require.Equal(t, "zen-free/free", rows[0].Ref)
	snap, err := e.s.modelCatalog(bg, "zen-free", false)
	require.NoError(t, err)
	req, err := http.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", strings.NewReader(`{"model":"free","stream":true,"messages":[]}`))
	require.NoError(t, err)
	prepared, err := e.s.prepareModelHTTP(bg, snap, ai.ModelSession{ID: "opaque-random-session", CreatedAt: 1792240000000}, req)
	require.NoError(t, err)
	require.Equal(t, "Bearer public", prepared.Header.Get("Authorization"))
	require.Regexp(t, `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`, prepared.Header.Get("x-opencode-session"))
	var body map[string]any
	require.NoError(t, json.NewDecoder(prepared.Body).Decode(&body))
	require.Len(t, body["tools"], 5)
}

// Live tests use only public test prompts and an inert, fixed-result tool.
func TestZenFreeLive(t *testing.T) {
	if os.Getenv("GODWIT_ZEN_LIVE") != "1" {
		t.Skip("opt in with GODWIT_ZEN_LIVE=1")
	}
	dist := os.Getenv("GODWIT_ZEN_FREE_DIST")
	require.NotEmpty(t, dist)
	e := newApprovalEnv(t)
	ctx, cancel := context.WithTimeout(bg, 90*time.Second)
	defer cancel()
	_, err := e.s.InstallPlugin(ctx, dist)
	require.NoError(t, err)
	require.NoError(t, e.s.GrantPermission(ctx, "zen-free"))
	require.NoError(t, e.s.SetPluginEnabled(ctx, "zen-free", true))
	rows, err := e.s.ListModels(ctx)
	require.NoError(t, err)
	rows = slices.DeleteFunc(rows, func(m ListedModel) bool { return !strings.HasPrefix(m.Ref, "zen-free/") })
	require.NotEmpty(t, rows)
	for _, row := range rows {
		t.Logf("catalog: %s %s", row.Ref, row.Model.Protocol)
	}
	m, _, err := e.s.resolveRef(ctx, "zen-free/big-pickle")
	require.NoError(t, err)
	engine, err := e.s.aiEngine()
	require.NoError(t, err)
	for _, withTool := range []bool{false, true} {
		t.Run(fmt.Sprintf("tools=%v", withTool), func(t *testing.T) {
			req := ai.Request{Model: m, Prompt: "Reply with exactly ZEN_OK.", MaxTurns: 3}
			called := false
			if withTool {
				req.Prompt = "Call zen_echo exactly once with value ZEN_TOOL_OK, then repeat its result. Do not call any other tool."
				req.Tools = []ai.ToolSpec{{Name: "zen_echo", Description: "Returns the provided public test value; has no side effects.", Schema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []string{"ZEN_TOOL_OK"}}}, "required": []string{"value"}, "additionalProperties": false}, Run: func(context.Context, json.RawMessage) (any, error) { called = true; return "ZEN_TOOL_OK", nil }}}
			}
			events, wait := engine.Run(ctx, req)
			var text strings.Builder
			for ev := range events {
				if ev.Kind == "tool_call" && ev.Call != nil {
					t.Logf("tool: %s", ev.Call.Name)
				}
				if ev.Kind == "text" {
					text.WriteString(ev.Text)
				}
			}
			result := wait()
			require.Nil(t, result.Err, "live result: %+v", result.Err)
			if withTool {
				require.True(t, called)
				require.Contains(t, text.String(), "ZEN_TOOL_OK")
			} else {
				require.Contains(t, text.String(), "ZEN_OK")
			}
			t.Logf("verified %s tools=%v", m.Model, withTool)
		})
	}
	st, err := e.s.GetSettings(ctx)
	require.NoError(t, err)
	st.JudgeModel = "zen-free/jev-1.13-free"
	_, err = e.s.SetSettings(ctx, st)
	require.NoError(t, err)
	judge, ref, byGen, err := e.s.resolveJudge(ctx)
	require.NoError(t, err)
	require.Equal(t, st.JudgeModel, ref)
	require.False(t, byGen)
	answers, err := judge.Ask(ctx, "The public test value is blue.", []ai.Question{{Name: "blue", Type: ai.Predicate, Instructions: "Is the test value blue?"}})
	require.NoError(t, err)
	require.Len(t, answers, 1)
	require.NotNil(t, answers[0].Probability)
	require.Greater(t, *answers[0].Probability, 0.5)
	t.Log("free judge verified through selection: jev-1.13-free")
}

package core

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/require"
)

type modelIntegrationDNS struct{}

func (modelIntegrationDNS) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

func runPluginHTTPIntegration(t *testing.T, kind string, truncate bool) ([]ai.Event, ai.Result) {
	t.Helper()
	e := newApprovalEnv(t)
	backend := testllm.Start(t, kind)
	backend.Script(testllm.Turn{Text: []string{"hello"}})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		if kind != "anthropic" {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
		}
		recorder := httptest.NewRecorder()
		backend.Mux.ServeHTTP(recorder, r)
		data := recorder.Body.String()
		if kind == ai.KindOpenAI {
			data = ": keep-alive\n\n" + data
		}
		if truncate {
			switch kind {
			case ai.KindOpenAI:
				if i := strings.Index(data, "data: [DONE]"); i >= 0 {
					data = data[:i]
				}
			case ai.KindAnthropic:
				if i := strings.Index(data, "event: message_stop"); i >= 0 {
					data = data[:i]
				}
			case ai.KindResponses:
				if i := strings.Index(data, "event: response.completed"); i >= 0 {
					data = data[:i]
				}
			}
		}
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		w.Write([]byte(data))
	}))
	t.Cleanup(srv.Close)
	port := strings.Split(srv.Listener.Addr().String(), ":")[1]
	e.s.pluginNet.Resolver = modelIntegrationDNS{}
	e.s.pluginNet.AllowLoopback = true
	protocol := map[string]string{ai.KindOpenAI: "openai-completions", ai.KindResponses: "openai-responses", ai.KindAnthropic: "anthropic-message"}[kind]
	startTestPlugin(t, e.s, "httpmodel", fmt.Sprintf(`{"net":["example.com:%s"]},"capabilities":["models"]`, port), fmt.Sprintf(`export default function(ctx){ctx.models.register({listModels:()=>({models:[{id:"m-1",name:"M",transport:"http",protocol:%q,baseURL:%q}]}),prepareRequest:call=>call.request})}`, protocol, "https://example.com:"+port+"/v1"))
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	e.s.running["httpmodel"].modelNet.Transport().TLSClientConfig.RootCAs = pool
	_, err := e.s.ListModels(bg)
	require.NoError(t, err)
	model, _, err := e.s.resolveRef(bg, "httpmodel/m-1")
	require.NoError(t, err)
	engine, err := e.s.aiEngine()
	require.NoError(t, err)
	events, wait := engine.Run(bg, ai.Request{Model: model, Prompt: "hi"})
	var got []ai.Event
	for ev := range events {
		got = append(got, ev)
	}
	return got, wait()
}
func TestPluginModelsHTTPIntegration(t *testing.T) {
	for _, kind := range []string{ai.KindOpenAI, ai.KindResponses, ai.KindAnthropic} {
		t.Run(kind, func(t *testing.T) {
			events, result := runPluginHTTPIntegration(t, kind, false)
			require.Nil(t, result.Err, "%+v", result.Err)
			require.NotEmpty(t, result.State)
			var text string
			for _, ev := range events {
				if ev.Kind == "text" {
					text += ev.Text
				}
			}
			require.Equal(t, "hello", text)
		})
	}
}
func TestPluginModelTruncatedStream(t *testing.T) {
	for _, kind := range []string{ai.KindOpenAI, ai.KindResponses, ai.KindAnthropic} {
		t.Run(kind, func(t *testing.T) {
			_, result := runPluginHTTPIntegration(t, kind, true)
			require.NotNil(t, result.Err)
			require.Empty(t, result.State)
		})
	}
}

func TestPluginModelsHTTPJudgeIntegration(t *testing.T) {
	for _, protocol := range []string{"typesafe-systemone", "openai-decisions"} {
		t.Run(protocol, func(t *testing.T) {
			e := newApprovalEnv(t)
			var sessions []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer plugin-public", r.Header.Get("Authorization"))
				sessions = append(sessions, r.Header.Get("x-test-session"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "j", body["model"])
				w.Header().Set("Content-Type", "application/json")
				if protocol == "typesafe-systemone" {
					require.Equal(t, "/v1/systemone", r.URL.Path)
					require.Equal(t, "public fixture", body["state"])
					w.Write([]byte(`{"answers":{"q":{"type":"noul","noul":0.8}}}`))
				} else {
					require.Equal(t, "/v1/decisions", r.URL.Path)
					require.Equal(t, "public fixture", body["input"])
					w.Write([]byte(`{"answers":[{"name":"q","type":"predicate","probability":0.8}]}`))
				}
			}))
			defer server.Close()
			e.s.pluginNet = plugins.NetOptions{Resolver: modelIntegrationDNS{}, AllowLoopback: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}}
			startTestPlugin(t, e.s, "httpjudge", `{"net":["example.com"]},"capabilities":["models"]`, fmt.Sprintf(`export default function(ctx){ctx.models.register({listModels:()=>({models:[{id:"j",name:"J",transport:"http",protocol:%q,baseURL:"https://example.com/v1"}]}),prepareRequest(call){call.request.headers.Authorization="Bearer plugin-public";call.request.headers["x-test-session"]=call.sessionId;return call.request}})}`, protocol))
			e.s.running["httpjudge"].modelNet.Transport().TLSClientConfig.InsecureSkipVerify = true // pinned to the loopback fixture
			_, err := e.s.ListModels(bg)
			require.NoError(t, err)
			settings, err := e.s.GetSettings(bg)
			require.NoError(t, err)
			settings.JudgeModel = "httpjudge/j"
			_, err = e.s.SetSettings(bg, settings)
			require.NoError(t, err)
			judge, _, _, err := e.s.resolveJudge(bg)
			require.NoError(t, err)
			for range 2 {
				answers, err := judge.Ask(bg, "public fixture", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "Is this a fixture?"}})
				require.NoError(t, err)
				require.Len(t, answers, 1)
				require.Equal(t, 0.8, *answers[0].Probability)
			}
			require.Len(t, sessions, 2)
			require.NotEmpty(t, sessions[0])
			require.NotEqual(t, sessions[0], sessions[1])
		})
	}
}

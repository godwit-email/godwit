package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/require"
)

func modelHTTPTest(t *testing.T, prepare string) (*Service, modelSnapshot) {
	t.Helper()
	e := newApprovalEnv(t)
	startTestPlugin(t, e.s, "httpmodel", `{"net":["example.com"]},"capabilities":["models"]`, `export default function(ctx){ctx.models.register({listModels:()=>({models:[{id:"m",name:"M",transport:"http",protocol:"openai-completions",baseURL:"https://example.com/v1"}]}),prepareRequest(call){`+prepare+`}})}`)
	snap, err := e.s.modelCatalog(bg, "httpmodel", false)
	require.NoError(t, err)
	require.Len(t, snap.models, 1)
	return e.s, snap
}
func TestPluginModelRetryStartsFresh(t *testing.T) {
	s, snap := modelHTTPTest(t, `if(call.request.body.tools.length!==1) throw new Error("adaptation accumulated");call.request.body.tools.push({type:"function",function:{name:"placeholder",parameters:{type:"object"}}});call.request.headers["x-request"]=call.requestId;return call.request`)
	req, err := http.NewRequest("POST", "https://example.com/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[],"tools":[{"type":"function","function":{"name":"real","parameters":{"type":"object"}}}]}`))
	require.NoError(t, err)
	a, err := s.prepareModelHTTP(bg, snap, ai.ModelSession{ID: "session", CreatedAt: 123}, req)
	require.NoError(t, err)
	b, err := s.prepareModelHTTP(bg, snap, ai.ModelSession{ID: "session", CreatedAt: 123}, req)
	require.NoError(t, err)
	require.NotEmpty(t, a.Header.Get("x-request"))
	require.NotEqual(t, a.Header.Get("x-request"), b.Header.Get("x-request"))
	a.Body.Close()
	b.Body.Close()
	held, err := s.holdsMail(bg, "httpmodel")
	require.NoError(t, err)
	require.True(t, held)
}
func TestPluginModelInputLimit(t *testing.T) {
	s, snap := modelHTTPTest(t, `throw new Error("must not be called")`)
	req, err := http.NewRequest("POST", "https://example.com/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[],"padding":"`+strings.Repeat("x", 1<<20)+`"}`))
	require.NoError(t, err)
	_, err = s.prepareModelHTTP(bg, snap, ai.ModelSession{}, req)
	require.ErrorIs(t, err, plugins.ErrTooLarge)
	held, err := s.holdsMail(bg, "httpmodel")
	require.NoError(t, err)
	require.False(t, held)
}
func TestPluginModelStructuralProtection(t *testing.T) {
	for _, mutation := range []string{`call.request.body.model="other"`, `call.request.body.stream=false`, `delete call.request.body.messages`, `call.request.body.tools=[]`, `call.request.body.tools[0].function.parameters={type:"string"}`, `call.request.headers["bad\\r\\nkey"]="x"`, `call.request.url="https://evil.example/v1"`} {
		t.Run(mutation, func(t *testing.T) {
			s, snap := modelHTTPTest(t, mutation+`;return call.request`)
			req, _ := http.NewRequest("POST", "https://example.com/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[],"tools":[{"type":"function","function":{"name":"real","parameters":{"type":"object"}}}]}`))
			_, err := s.prepareModelHTTP(bg, snap, ai.ModelSession{}, req)
			require.Error(t, err)
		})
	}
}
func TestPluginModelHeaders(t *testing.T) {
	s, snap := modelHTTPTest(t, `call.request.headers["User-Agent"]="zen";call.request.headers["Content-Length"]="999999";call.request.headers["Connection"]="x-secret";call.request.headers["x-secret"]="drop";return call.request`)
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "https://example.com/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	got, err := s.prepareModelHTTP(bg, snap, ai.ModelSession{}, req)
	require.NoError(t, err)
	defer got.Body.Close()
	require.Equal(t, "zen", got.UserAgent())
	require.Empty(t, got.Header.Get("Content-Length"))
	require.Empty(t, got.Header.Get("x-secret"))
	b, err := io.ReadAll(got.Body)
	require.NoError(t, err)
	require.Equal(t, int64(len(b)), got.ContentLength)
}

func TestPluginModelsHTTPProtocols(t *testing.T) {
	s, base := modelHTTPTest(t, `return call.request`)
	for _, tc := range []struct{ protocol, path string }{{"openai-completions", "/chat/completions"}, {"openai-responses", "/responses"}, {"anthropic-message", "/messages"}, {"typesafe-systemone", "/systemone"}, {"openai-decisions", "/decisions"}} {
		t.Run(tc.protocol, func(t *testing.T) {
			snap := base
			snap.models = append([]ProviderModel(nil), base.models...)
			snap.models[0].Protocol = tc.protocol
			snap.models[0].BaseURL = "https://example.com/inference/v1"
			req, _ := http.NewRequest("POST", "https://example.com/inference/v1/v1/messages", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
			got, err := s.prepareModelHTTP(bg, snap, ai.ModelSession{ID: "s", CreatedAt: 1}, req)
			require.NoError(t, err)
			defer got.Body.Close()
			require.Equal(t, "/inference/v1"+tc.path, got.URL.Path)
		})
	}
}
func TestPluginModelSessionResume(t *testing.T) {
	e := newApprovalEnv(t)
	conv, err := e.s.NewConversation(bg, "ask", 0)
	require.NoError(t, err)
	a, err := e.s.conversationModelSession(bg, conv.ID)
	require.NoError(t, err)
	require.NotEmpty(t, a.ID)
	require.Positive(t, a.CreatedAt)
	b, err := e.s.conversationModelSession(bg, conv.ID)
	require.NoError(t, err)
	require.Equal(t, a, b)
	conv2, err := e.s.NewConversation(bg, "ask", 0)
	require.NoError(t, err)
	c, err := e.s.conversationModelSession(bg, conv2.ID)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, c.ID)
}

func TestPluginModelSSEHeartbeats(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		truncated         bool
	}{
		{"heartbeat", ": keep-alive\n\ndata: {\"text\":\"hello\"}\n\n: ping\n\ndata: [DONE]\n\n", "data: {\"text\":\"hello\"}\n\ndata: [DONE]\n\n", false},
		{"crlf", ": ping\r\n\r\nevent: unused\r\n\r\ndata: [DONE]\r\n\r\n", "data: [DONE]\r\n\r\n", false},
		{"large", ":" + strings.Repeat("x", 10000) + "\n\ndata: " + strings.Repeat("y", 10000) + "\n\ndata: [DONE]\n\n", "data: " + strings.Repeat("y", 10000) + "\n\ndata: [DONE]\n\n", false},
		{"no-space-done", "data:[DONE]\n\n", "data:[DONE]\n\n", false},
		{"no-space-crlf", "data:[DONE]\r\n\r\n", "data:[DONE]\r\n\r\n", false},
		{"heartbeat-only", ": keep-alive\n\n", "", true},
		{"truncated", "data: {}\n\n: keep-alive\n\n", "data: {}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &modelSSEBody{ReadCloser: io.NopCloser(strings.NewReader(tc.input))}
			var got strings.Builder
			p := make([]byte, 3)
			for {
				n, err := body.Read(p)
				got.Write(p[:n])
				if err != nil {
					if tc.truncated {
						require.ErrorIs(t, err, io.ErrUnexpectedEOF)
					} else {
						require.ErrorIs(t, err, io.EOF)
					}
					break
				}
			}
			require.Equal(t, tc.want, got.String())
		})
	}
}

func TestPluginModelSSEStreamsAndCloses(t *testing.T) {
	r, w := io.Pipe()
	body := &modelSSEBody{ReadCloser: r}
	defer body.Close()
	written := make(chan struct{})
	go func() {
		defer close(written)
		_, _ = io.WriteString(w, ": ping\n\ndata: {}\n\n")
	}()
	defer w.Close()
	result := make(chan error, 1)
	go func() {
		data := make([]byte, len("data: {}\n\n"))
		_, err := io.ReadFull(body, data)
		if err == nil && string(data) != "data: {}\n\n" {
			err = fmt.Errorf("unexpected first event: %q", data)
		}
		result <- err
	}()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("first event waited for EOF")
	}
	<-written
	go func() { _, err := body.Read(make([]byte, 1)); result <- err }()
	require.NoError(t, body.Close())
	select {
	case err := <-result:
		require.ErrorIs(t, err, io.ErrClosedPipe)
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel Read")
	}
}

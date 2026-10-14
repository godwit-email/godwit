package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsErrorKinds(t *testing.T) {
	for _, c := range []struct {
		status int
		want   error
		text   string
	}{
		{401, ErrModelAuth, ""}, {403, ErrModelAuth, ""},
		{404, ErrNoModelList, ""}, {405, ErrNoModelList, ""}, {501, ErrNoModelList, ""},
		{500, nil, "list models: HTTP 500"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte("body-secret"))
		}))
		_, err := ListModels(context.Background(), nil, KindResponses, srv.URL, "sk-secret")
		srv.Close()
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) || (c.text != "" && err.Error() != c.text) {
			t.Errorf("%d: %v", c.status, err)
			continue
		}
		if strings.Contains(err.Error(), "sk-secret") || strings.Contains(err.Error(), "body-secret") {
			t.Errorf("%d leaks: %v", c.status, err)
		}
	}
}

func TestListModelsReturnsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"text-embedding-3-small"},{"id":"gpt-5"}]}`))
	}))
	defer srv.Close()
	ids, err := ListModels(context.Background(), nil, KindOpenAI, srv.URL, "")
	if err != nil || len(ids) != 2 || ids[0] != "gpt-5" || ids[1] != "text-embedding-3-small" {
		t.Errorf("%v %v", ids, err)
	}
}

func TestListModelsRequestShape(t *testing.T) {
	for _, c := range []struct{ kind, path, hdr, val string }{
		{KindOpenAI, "/models", "Authorization", "Bearer k"},
		{KindResponses, "/models", "Authorization", "Bearer k"},
		{KindAnthropic, "/v1/models", "x-api-key", "k"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != c.path || r.Header.Get(c.hdr) != c.val {
				t.Errorf("%s: %s %s=%q", c.kind, r.URL.Path, c.hdr, r.Header.Get(c.hdr))
			}
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
		}))
		ids, err := ListModels(context.Background(), nil, c.kind, srv.URL, "k")
		srv.Close()
		if err != nil || len(ids) != 1 {
			t.Errorf("%s: %v %v", c.kind, ids, err)
		}
	}
}

func TestListModelsVisibility(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.SetModels("gpt-a", "gpt-b")
	srv.SetHidden("gpt-b")
	ids, err := ListModels(context.Background(), nil, KindOpenAI, srv.URL, "k")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-a"}, ids)
}

func TestModelsURL(t *testing.T) {
	var hits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{"tag_name":"rust-v0.161.2","prerelease":false,"draft":false}`))
	}))
	defer gh.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 503) }))
	defer bad.Close()
	reset := func() { codexVersion.version, codexVersion.until = "", time.Time{} }
	urls := codexVersionURLs
	t.Cleanup(func() { reset(); codexVersionURLs = urls })
	ctx := context.Background()

	reset()
	codexVersionURLs = []string{bad.URL, gh.URL}
	assert.Equal(t, "http://x/backend-api/codex/models?client_version=0.161.2", modelsURL(ctx, nil, "http://x/backend-api/codex"), "the first lookup that answers")
	modelsURL(ctx, nil, "http://x/backend-api/codex")
	assert.Equal(t, 1, hits, "cached")

	reset()
	codexVersionURLs = []string{bad.URL}
	assert.Equal(t, "http://x/backend-api/codex/models?client_version="+codexFallbackVersion, modelsURL(ctx, nil, "http://x/backend-api/codex"), "every lookup failed")

	assert.Equal(t, "https://api.x.ai/v1/language-models", modelsURL(ctx, nil, "https://api.x.ai/v1"), "xAI: text models only")
	assert.Equal(t, "https://api.openai.com/v1/models", modelsURL(ctx, nil, "https://api.openai.com/v1"))
}

func TestListModelsCodex(t *testing.T) {
	t.Cleanup(func() { codexVersion.version, codexVersion.until = "", time.Time{} })
	codexVersion.version, codexVersion.until = "0.159.0", time.Now().Add(time.Hour) // no lookup off the network

	srv := testllm.Start(t, "openai-responses")
	srv.SetModels("gpt-6-luna", "gpt-5.6-luna")
	tok := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-9"}}`)) + ".s"
	ms, err := ListModelsNamed(context.Background(), nil, KindResponses, srv.URL+"/backend-api/codex", tok)
	require.NoError(t, err)
	assert.Equal(t, []Model{{ID: "gpt-5.6-luna"}, {ID: "gpt-6-luna"}}, ms)
	last := srv.Headers()[len(srv.Headers())-1]
	assert.Equal(t, "Bearer "+tok, last.Get("Authorization"))
	assert.Equal(t, "acct-9", last.Get("chatgpt-account-id"), "the token's account, as the responses carry")
	_, err = ListModels(context.Background(), nil, KindResponses, srv.URL+"/backend-api/codex/", tok)
	require.NoError(t, err, "a trailing slash is fine")
}

func TestListModelsXAIShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"id":"grok-4.7","input_modalities":["text","image"],"output_modalities":["text"]},{"id":"grok-4.6"}]}`))
	}))
	defer srv.Close()
	ids, err := ListModels(context.Background(), nil, KindResponses, srv.URL, "k")
	require.NoError(t, err)
	assert.Equal(t, []string{"grok-4.6", "grok-4.7"}, ids)
}

func TestListModelsChatGPTShape(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.SetModels("gpt-5.2", "gpt-5.2-mini", "gpt-5.2-codex")
	srv.SetHidden("gpt-5.2-codex")
	srv.SetDisplayNames(map[string]string{"gpt-5.2": "GPT-5.2", "gpt-5.2-mini": "GPT-5.2 mini"})
	ms, err := ListModelsNamed(context.Background(), nil, KindResponses, srv.URL, "k")
	require.NoError(t, err)
	assert.Equal(t, []Model{{"gpt-5.2", "GPT-5.2"}, {"gpt-5.2-mini", "GPT-5.2 mini"}}, ms)
	ids, err := ListModels(context.Background(), nil, KindResponses, srv.URL, "k")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-5.2", "gpt-5.2-mini"}, ids)
}

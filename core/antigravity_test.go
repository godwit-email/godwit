package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAntigravityFlow struct {
	authURL string
	grant   auth.Grant
	err     error
}

func (f *fakeAntigravityFlow) AuthURL() string { return f.authURL }
func (f *fakeAntigravityFlow) Wait(context.Context) (auth.Grant, error) {
	return f.grant, f.err
}
func (f *fakeAntigravityFlow) Close() {}

type fakeAntigravity struct {
	authURL  string
	email    string
	token    string
	genText  string
	genErr   error
	judgeAns []ai.Answer
	judgeErr error
	models   []ai.Model
	calls    int
	lastTok  string
}

func (f *fakeAntigravity) StartOAuth(_ context.Context) (flow, error) {
	u := f.authURL
	if u == "" {
		u = "https://accounts.google.com/o/oauth2/v2/auth?client_id=test&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Foauth-callback&response_type=code&state=abc"
	}
	tok := f.token
	if tok == "" {
		tok = `{"refresh_token":"rt-isolated","client_id":"cid","client_secret":"csec"}`
	}
	em := f.email
	if em == "" {
		em = "user@gmail.com"
	}
	return &fakeAntigravityFlow{
		authURL: u,
		grant: auth.Grant{
			Email:        em,
			RefreshToken: tok,
			AccessToken:  tok,
		},
	}, nil
}

func (f *fakeAntigravity) ListModels(_ context.Context, tokenJSON string) ([]ai.Model, error) {
	f.lastTok = tokenJSON
	if strings.TrimSpace(tokenJSON) == "" {
		return nil, ai.ErrModelAuth
	}
	if len(f.models) > 0 {
		return f.models, nil
	}
	return []ai.Model{
		{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"},
		{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)"},
		{ID: "gemini-4-argon", Name: "Gemini 4 Argon"},
	}, nil
}

func (f *fakeAntigravity) Generate(_ context.Context, tokenJSON, model, prompt string, onChunk func(string)) (string, error) {
	f.calls++
	f.lastTok = tokenJSON
	if strings.TrimSpace(tokenJSON) == "" {
		return "", ai.ErrModelAuth
	}
	if f.genErr != nil {
		return "", f.genErr
	}
	res := f.genText
	if res == "" {
		res = "Antigravity answer for: " + prompt
	}
	if onChunk != nil {
		onChunk(res)
	}
	return res, nil
}

func (f *fakeAntigravity) Judge(_ context.Context, tokenJSON, model, input string, qs []ai.Question) ([]ai.Answer, error) {
	f.calls++
	f.lastTok = tokenJSON
	if strings.TrimSpace(tokenJSON) == "" {
		return nil, ai.ErrModelAuth
	}
	if f.judgeErr != nil {
		return nil, f.judgeErr
	}
	if f.judgeAns != nil {
		return f.judgeAns, nil
	}
	answers := make([]ai.Answer, len(qs))
	for i, q := range qs {
		answers[i] = ai.Answer{Name: q.Name, Type: q.Type}
		if q.Type == ai.Predicate {
			p := 0.92
			answers[i].Probability = &p
		}
	}
	return answers, nil
}

func TestAntigravityRequiresOAuthSignIn(t *testing.T) {
	e := newSkillEnv(t)
	fake := &fakeAntigravity{}
	e.s.opts.Antigravity = fake

	// Checking or adding without signing in first must return ErrModelLogin
	_, err := e.s.CheckModelService(t.Context(), 0, ModelServiceInput{
		Name:     "Antigravity",
		Provider: ProviderAntigravity,
		Kind:     ai.KindOpenAI,
	})
	require.ErrorIs(t, err, ErrModelLogin)

	_, err = e.s.AddModelService(t.Context(), ModelServiceInput{
		Name:     "Antigravity",
		Provider: ProviderAntigravity,
		Kind:     ai.KindOpenAI,
		Models:   []string{"gemini-3.8-flash"},
	})
	require.ErrorIs(t, err, ErrModelLogin)
}

func TestAntigravityOAuthCheckAndAdd(t *testing.T) {
	e := newSkillEnv(t)
	fake := &fakeAntigravity{
		email:   "alice@gmail.com",
		token:   `{"refresh_token":"rt-alice","client_id":"cid","client_secret":"csec"}`,
		genText: "OK",
	}
	e.s.opts.Antigravity = fake

	// 1. Begin OAuth
	flowID, authURL, err := e.s.BeginOAuth(t.Context(), ProviderAntigravity, "", OAuthClient{})
	require.NoError(t, err)
	assert.NotEmpty(t, flowID)
	assert.Contains(t, authURL, "accounts.google.com")

	// 2. Await OAuth
	email, err := e.s.AwaitOAuth(t.Context(), flowID)
	require.NoError(t, err)
	assert.Equal(t, "alice@gmail.com", email)

	// 3. CheckModelService using the completed OAuthFlowID
	chk, err := e.s.CheckModelService(t.Context(), 0, ModelServiceInput{
		Name:        "Antigravity",
		Provider:    ProviderAntigravity,
		Kind:        ai.KindOpenAI,
		JudgeFormat: string(ai.FormatDecisions),
		OAuthFlowID: flowID,
	})
	require.NoError(t, err)
	assert.True(t, slices.Contains(chk.Models, "gemini-3.8-flash"))
	assert.True(t, slices.Contains(chk.JudgeModels, "gemini-3.8-flash"))
	assert.Contains(t, fake.lastTok, "rt-alice")

	// 4. AddModelService
	ms, err := e.s.AddModelService(t.Context(), ModelServiceInput{
		Name:        "Antigravity",
		Provider:    ProviderAntigravity,
		Kind:        ai.KindOpenAI,
		Models:      []string{"gemini-3.8-flash"},
		JudgeFormat: string(ai.FormatDecisions),
		JudgeModel:  "gemini-3.8-flash",
		OAuthFlowID: flowID,
	})
	require.NoError(t, err)
	assert.Equal(t, "Antigravity", ms.Name)
	assert.Equal(t, ProviderAntigravity, ms.Provider)
	assert.Equal(t, "alice@gmail.com", ms.Email)

	// 5. Resolve stored model & test capabilities
	rm, ref, err := e.s.resolveRef(t.Context(), ms.Slug+"/gemini-3.8-flash")
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash", rm.Model)
	assert.Equal(t, ms.Slug+"/gemini-3.8-flash", ref)
	assert.Contains(t, rm.APIKey, "rt-alice")

	genSt, err := e.s.TestCapability(t.Context(), ms.ID, ModelServiceInput{
		Name:        ms.Name,
		Provider:    ProviderAntigravity,
		Kind:        ai.KindOpenAI,
		Models:      []string{"gemini-3.8-flash"},
		JudgeFormat: string(ai.FormatDecisions),
		JudgeModel:  "gemini-3.8-flash",
	}, CapGen)
	require.NoError(t, err)
	assert.Equal(t, "ok", genSt.State)

	judgeSt, err := e.s.TestCapability(t.Context(), ms.ID, ModelServiceInput{
		Name:        ms.Name,
		Provider:    ProviderAntigravity,
		Kind:        ai.KindOpenAI,
		Models:      []string{"gemini-3.8-flash"},
		JudgeFormat: string(ai.FormatDecisions),
		JudgeModel:  "gemini-3.8-flash",
	}, CapJudge)
	require.NoError(t, err)
	assert.Equal(t, "ok", judgeSt.State)
}

func TestAntigravityIsolatedProfilePreparation(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "should-be-stripped")
	t.Setenv("AGY_AUTH_TOKEN", "should-be-stripped")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/tmp/secret.json")

	profileDir := t.TempDir()
	rt := antigravityRuntime{
		ExecutablePath: "/tmp/agy_acp_server.par",
		HarnessPath:    "/tmp/localharness_external",
		InstallDir:     "/tmp",
		Version:        "1.4.5",
	}
	tokenJSON := `{"refresh_token":"rt-123"}`
	env, err := prepareAntigravityProfile(profileDir, rt, tokenJSON)
	require.NoError(t, err)

	envMap := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}

	assert.Equal(t, profileDir, envMap["GEMINI_HOME"])
	assert.Equal(t, "1", envMap["AGY_ACP_FORCE_FILE_STORAGE"])
	assert.Equal(t, "/tmp/localharness_external", envMap["ANTIGRAVITY_HARNESS_PATH"])
	assert.NotEmpty(t, envMap["BROWSER"])
	assert.NotContains(t, envMap, "GEMINI_API_KEY")
	assert.NotContains(t, envMap, "AGY_AUTH_TOKEN")
	assert.NotContains(t, envMap, "GOOGLE_APPLICATION_CREDENTIALS")

	savedToken, err := os.ReadFile(filepath.Join(profileDir, "antigravity-acp", "acp_token.json"))
	require.NoError(t, err)
	assert.Equal(t, tokenJSON, string(savedToken))
}

func TestAntigravityTransport(t *testing.T) {
	fake := &fakeAntigravity{genText: "Streamed hello!"}
	tr := &antigravityTransport{ag: func() Antigravity { return fake }}
	client := &http.Client{Transport: tr}

	// Unauthenticated request returns 401
	unauthResp, err := client.Get("http://127.0.0.1:0/antigravity/models")
	require.NoError(t, err)
	unauthResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, unauthResp.StatusCode)

	// Authenticated /models
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:0/antigravity/models", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", `Bearer {"refresh_token":"rt-test"}`)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Authenticated streaming /chat/completions
	chatBody, _ := json.Marshal(map[string]any{
		"model":  "gemini-3.8-flash-high",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "Hi"},
		},
	})
	chatReq, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:0/antigravity/chat/completions", bytes.NewReader(chatBody))
	require.NoError(t, err)
	chatReq.Header.Set("Authorization", `Bearer {"refresh_token":"rt-test"}`)
	chatReq.Header.Set("Content-Type", "application/json")
	chatResp, err := client.Do(chatReq)
	require.NoError(t, err)
	defer chatResp.Body.Close()
	assert.Equal(t, http.StatusOK, chatResp.StatusCode)
	b, err := io.ReadAll(chatResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(b), "Streamed hello!")
	assert.Contains(t, string(b), "[DONE]")
}

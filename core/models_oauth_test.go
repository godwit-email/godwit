package core

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oauthLLM points provider p at a testoauth server and a testllm Responses server; restores on cleanup.
func oauthLLM(t *testing.T, p string) (*testoauth.Server, *testllm.Server) {
	t.Helper()
	oa, llm := testoauth.Start(t), testllm.Start(t, "openai-responses")
	llm.SetModels("gpt-x")
	saved := modelOAuth[p]
	cfg := oa.Config("openid", "email")
	if p == ProviderGrok {
		cfg = oa.DeviceConfig("openid", "email")
	}
	modelOAuth[p] = modelProvider{BaseURL: llm.URL, OAuth: cfg, Models: saved.Models}
	t.Cleanup(func() { modelOAuth[p] = saved })
	return oa, llm
}

// signIn runs a flow for provider p to its grant and returns the flow ID.
func signIn(t *testing.T, s *Service, oa *testoauth.Server, p, email string) string {
	t.Helper()
	id, authURL, err := s.BeginOAuth(t.Context(), p, "")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := s.AwaitOAuth(t.Context(), id); done <- err }()
	if p == ProviderGrok {
		oa.Approve(email)
	} else {
		oa.Browse(t, authURL, email)
	}
	require.NoError(t, <-done)
	return id
}

func TestAddChatGPTService(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, llm := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	_, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}})
	assert.ErrorIs(t, err, ErrModelLogin)
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid, BaseURL: "https://evil.example"})
	require.NoError(t, err)
	assert.Equal(t, llm.URL, ms.BaseURL, "the address is fixed")
	assert.Equal(t, "alice@example.com", ms.Email)
	assert.True(t, ms.HasKey)
	assert.NotEmpty(t, storedKey(t, s, ms.ID), "the refresh token is in the key slot")
	_, ok := s.takeGrant(fid)
	assert.False(t, ok, "the grant was used up")
	_, err = s.AddModelService(bg, ModelServiceInput{Name: "x", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"m"}, OAuthFlowID: fid})
	assert.ErrorIs(t, err, ErrUnknownFlow)
}

func TestChatGPTServiceRejectsKeyAndJudge(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")

	_, err := s.AddModelService(bg, ModelServiceInput{
		Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses,
		Models: []string{"gpt-x"}, OAuthFlowID: fid, APIKey: "sk-123",
	})
	assert.Error(t, err)

	_, err = s.AddModelService(bg, ModelServiceInput{
		Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses,
		Models: []string{"gpt-x"}, OAuthFlowID: fid, KeyAction: KeyClear,
	})
	assert.Error(t, err)

	_, err = s.AddModelService(bg, ModelServiceInput{
		Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses,
		Models: []string{"gpt-x"}, OAuthFlowID: fid, JudgeFormat: string(ai.FormatDecisions), JudgeModel: "gpt-x",
	})
	assert.Error(t, err)

	_, err = s.AddModelService(bg, ModelServiceInput{
		Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindOpenAI,
		Models: []string{"gpt-x"}, OAuthFlowID: fid,
	})
	assert.Error(t, err)
}

func TestUpdateChatGPTServiceKeepsLogin(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)
	before := storedKey(t, s, ms.ID)

	in := ms.Input()
	in.Name = "ChatGPT Plus"
	in.Models = []string{"gpt-x", "gpt-y"}
	ms2, err := s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Equal(t, "ChatGPT Plus", ms2.Name)
	assert.Equal(t, []string{"gpt-x", "gpt-y"}, ms2.Models)
	assert.Equal(t, "alice@example.com", ms2.Email)
	assert.Equal(t, before, storedKey(t, s, ms.ID))
}

func TestReloginReplacesRefreshToken(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	fid1 := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid1})
	require.NoError(t, err)
	before := storedKey(t, s, ms.ID)

	fid2 := signIn(t, s, oa, ProviderChatGPT, "bob@example.com")
	in := ms.Input()
	in.OAuthFlowID = fid2
	ms2, err := s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Equal(t, "bob@example.com", ms2.Email)
	assert.NotEqual(t, before, storedKey(t, s, ms.ID))
	_, ok := s.takeGrant(fid2)
	assert.False(t, ok, "the relogin grant was used up")
}

func TestChatGPTReloginUsesIssuedClient(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	oa.Dynamic(true)
	mp := modelOAuth[ProviderChatGPT]
	mp.OAuth.ClientID = testoauth.DynamicClientID
	mp.OAuth.DynamicClient = true
	mp.OAuth.AuthParams = map[string]string{"agent_name_hint": "Godwit"}
	modelOAuth[ProviderChatGPT] = mp

	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)
	got, err := s.modelServiceBy(bg, "id = ?", ms.ID)
	require.NoError(t, err)
	assert.Equal(t, testoauth.IssuedClientID, got.clientID)

	id2, authURL, err := s.BeginOAuth(bg, ProviderChatGPT, "alice@example.com")
	require.NoError(t, err)
	t.Cleanup(func() { s.CancelOAuth(id2) })
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, testoauth.IssuedClientID, q.Get("client_id"))
	assert.Empty(t, q.Get("agent_name_hint"))
	assert.Equal(t, s.hostID(), q.Get("ext_agent_host_id"))
}

func TestAddGrokServiceByDeviceCode(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, llm := oauthLLM(t, ProviderGrok)
	fid := signIn(t, s, oa, ProviderGrok, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "Grok", Provider: ProviderGrok, Kind: ai.KindResponses, Models: []string{"grok-4.7"}, OAuthFlowID: fid})
	require.NoError(t, err)
	assert.Equal(t, llm.URL, ms.BaseURL)
	assert.Equal(t, "alice@example.com", ms.Email)
	assert.True(t, ms.HasKey)
	assert.NotEmpty(t, storedKey(t, s, ms.ID))
}

func TestHostID(t *testing.T) {
	s := modelSvc(t)
	assert.Regexp(t, `^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, s.hostID())
}

func TestChatGPTRunUsesAccessToken(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)
	m, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.True(t, oa.ValidAccess(m.APIKey), "APIKey is a live access token, not the refresh token")
	assert.Equal(t, 1, oa.TokenRequests(), "the first token request was the code exchange; the sign-in's access token is reused")
	// 没有缓存时刷新一次，之后命中缓存
	s.forgetModelToken(ms.ID)
	m2, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.Equal(t, 2, oa.TokenRequests())
	_, err = s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.Equal(t, 2, oa.TokenRequests(), "cached")
	assert.NotEqual(t, m.APIKey, m2.APIKey)
}

func TestChatGPTRefreshRotatesAndSaves(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	oa.RotateRefresh(true)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)
	before := storedKey(t, s, ms.ID)

	s.forgetModelToken(ms.ID)
	m, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.True(t, oa.ValidAccess(m.APIKey))
	after := storedKey(t, s, ms.ID)
	assert.NotEqual(t, before, after)

	s.forgetModelToken(ms.ID)
	m2, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.True(t, oa.ValidAccess(m2.APIKey))
}

func TestChatGPTRevokedNeedsReauth(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, _ := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)

	oa.RevokeAll()
	s.forgetModelToken(ms.ID)
	_, err = s.resolveModel(bg, ms.Slug+"/gpt-x")
	assert.ErrorIs(t, err, ErrModelReauth)

	list, err := s.ListModelServices(bg)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "fail", list[0].Status[CapGen].State)
	assert.Equal(t, "auth", list[0].Status[CapGen].Code)
}

func TestChatGPTCheckListsWithToken(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, llm := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)

	chk, err := s.CheckModelService(bg, ms.ID, ms.Input())
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-x"}, chk.Models)
	hs := llm.Headers()
	require.NotEmpty(t, hs)
	auth := hs[len(hs)-1].Get("Authorization")
	assert.True(t, strings.HasPrefix(auth, "Bearer "))
	assert.True(t, oa.ValidAccess(strings.TrimPrefix(auth, "Bearer ")))
}

func TestUnauthorizedRunForgetsToken(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, llm := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)

	m, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.Equal(t, 1, oa.TokenRequests())

	llm.Script(testllm.Turn{Status: 401})
	e, err := s.aiEngine()
	require.NoError(t, err)
	_, err = s.runText(bg, e, ai.Request{Model: m, Prompt: "hi", MaxTurns: 1}, 0)
	require.Error(t, err)

	m2, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.Equal(t, 2, oa.TokenRequests())
	assert.NotEqual(t, m.APIKey, m2.APIKey)
}

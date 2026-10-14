package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/store"
)

// ErrModelLogin is returned when adding a subscription model service without signing in first.
var ErrModelLogin = errors.New("sign in to add this service")

// modelProvider is a subscription model provider's fixed configuration.
// modelOAuth is a package-level var so tests can point it at testoauth/testllm.
type modelProvider struct {
	BaseURL string
	OAuth   auth.OAuthConfig
}

// chatgptOfficial is Sign in with ChatGPT, the flow OpenAI sanctions for
// local apps. Kept, but not the default: accounts outside the subscription
// sharing preview get 403 subscription_sharing_user_not_eligible from it.
var chatgptOfficial = modelProvider{
	BaseURL: "https://api.openai.com/v1",
	OAuth: auth.OAuthConfig{
		ClientID:      "dynamic_agent_client",
		AuthURL:       "https://auth.openai.com/api/accounts/authorize",
		TokenURL:      "https://auth.openai.com/api/accounts/oauth/token",
		Scopes:        []string{"openid", "profile", "email", "offline_access", "resource.invoke", "chatgpt.tokens.use.direct"},
		RedirectHost:  "127.0.0.1",
		RedirectPath:  "/auth/callback",
		DynamicClient: true,
		NoPrompt:      true,
		Nonce:         true,
		RequireScope:  "chatgpt.tokens.use.direct",
		AuthParams:    map[string]string{"resource": "https://api.openai.com/v1", "agent_name_hint": "Godwit"},
		TokenParams:   map[string]string{"resource": "https://api.openai.com/v1"},
	},
}

// chatgptCodex signs in as the Codex CLI does (its public client, which only
// registers localhost:1455) and talks to ChatGPT's Codex backend, as pi's
// openai-codex provider. Its model list comes from the backend (ai.ListModels).
var chatgptCodex = modelProvider{
	BaseURL: "https://chatgpt.com/backend-api/codex",
	OAuth: auth.OAuthConfig{
		ClientID:     "app_EMoamEEZ73f0CkXaXp7hrann",
		AuthURL:      "https://auth.openai.com/oauth/authorize",
		TokenURL:     "https://auth.openai.com/oauth/token",
		Scopes:       []string{"openid", "profile", "email", "offline_access"},
		RedirectHost: "localhost",
		RedirectPath: "/auth/callback",
		RedirectPort: 1455,
		NoPrompt:     true,
		AuthParams:   map[string]string{"id_token_add_organizations": "true", "codex_cli_simplified_flow": "true", "originator": "godwit"},
	},
}

// modelOAuthLogins are every login a provider has had, by the address it
// talks to: a saved service's login is the one its BaseURL names.
var modelOAuthLogins = map[string]modelProvider{
	chatgptOfficial.BaseURL: chatgptOfficial,
	chatgptCodex.BaseURL:    chatgptCodex,
}

// modelLogin is the login behind a service: by its address, else its provider's current one.
func modelLogin(m ModelService) modelProvider {
	if mp, ok := modelOAuthLogins[m.BaseURL]; ok {
		return mp
	}
	return modelOAuth[m.Provider]
}

// modelOAuth is each provider's login for a service added now.
var modelOAuth = map[string]modelProvider{
	ProviderChatGPT: chatgptCodex,
	ProviderGrok: {
		BaseURL: "https://api.x.ai/v1",
		OAuth: auth.OAuthConfig{
			ClientID:   "b1a00492-073a-47ea-816f-4c329264a828",
			DeviceURL:  "https://auth.x.ai/oauth2/device/code",
			TokenURL:   "https://auth.x.ai/oauth2/token",
			Scopes:     []string{"openid", "profile", "email", "offline_access", "grok-cli:access", "api:access"},
			AuthParams: map[string]string{"referrer": "godwit"},
		},
	},
}

// ModelProviderLogin returns "oauth" if provider p signs in with OAuth, else "".
func ModelProviderLogin(p string) string {
	if _, ok := modelOAuth[p]; ok {
		return "oauth"
	}
	return ""
}

// ModelProviderURL returns the fixed BaseURL of a subscription model provider, or "".
func ModelProviderURL(p string) string {
	return modelOAuth[p].BaseURL
}

// hostID formats s.installID (16 random bytes hex) as urn:uuid:xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx.
func (s *Service) hostID() string {
	id := s.installID
	if len(id) == 32 {
		return "urn:uuid:" + id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	}
	return "urn:uuid:" + id
}

func (s *Service) modelOAuthConfig(ctx context.Context, p, loginHint string) (auth.OAuthConfig, error) {
	mp, ok := modelOAuth[p]
	if !ok {
		return auth.OAuthConfig{}, fmt.Errorf("provider %q does not sign in with OAuth", p)
	}
	return s.loginConfig(ctx, mp, p, loginHint)
}

// loginConfig is mp's OAuth settings for provider p, with what a dynamic
// client needs: this install's host ID, and the client already issued to the
// account loginHint names, so a re-login does not register another.
func (s *Service) loginConfig(ctx context.Context, mp modelProvider, p, loginHint string) (auth.OAuthConfig, error) {
	cfg := mp.OAuth
	cfg.Scopes = slices.Clone(cfg.Scopes)
	cfg.AuthParams = maps.Clone(cfg.AuthParams)
	cfg.TokenParams = maps.Clone(cfg.TokenParams)
	if cfg.DynamicClient {
		if cfg.AuthParams == nil {
			cfg.AuthParams = map[string]string{}
		}
		cfg.AuthParams["ext_agent_host_id"] = s.hostID()
		if h := strings.TrimSpace(loginHint); h != "" {
			rows, err := s.db.Query(ctx, `SELECT oauth_client_id FROM model_services
				WHERE provider = ? AND base_url = ? AND lower(oauth_email) = lower(?) AND oauth_client_id != '' LIMIT 1`, p, mp.BaseURL, h)
			if err != nil {
				return auth.OAuthConfig{}, err
			}
			if len(rows.Values) > 0 {
				if cid := store.Str(rows.Values[0][0]); cid != "" {
					cfg.ClientID = cid
					delete(cfg.AuthParams, "agent_name_hint")
					cfg.DynamicClient = false
				}
			}
		}
	}
	return cfg, nil
}

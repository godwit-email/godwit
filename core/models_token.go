package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
)

// ErrModelReauth is returned when a subscription model service's refresh token
// has been revoked, expired or lost from the keychain.
var ErrModelReauth = errors.New("the model service sign-in is no longer valid; sign in again")

type modelToken struct {
	access     string
	expiry     time.Time
	keyVersion int64
}

func (t modelToken) fresh(kv int64) bool {
	return t.access != "" && t.keyVersion == kv && time.Until(t.expiry) > tokenMargin
}

func (s *Service) modelRefreshLock(id int64) chan struct{} {
	s.modelTokMu.Lock()
	defer s.modelTokMu.Unlock()
	if s.modelRefreshing == nil {
		s.modelRefreshing = map[int64]chan struct{}{}
	}
	if s.modelRefreshing[id] == nil {
		s.modelRefreshing[id] = make(chan struct{}, 1)
	}
	return s.modelRefreshing[id]
}

func (s *Service) cacheModelToken(id, keyVersion int64, g auth.Grant) {
	if g.AccessToken == "" {
		return
	}
	keychainValues.Add(g.AccessToken)
	s.modelTokMu.Lock()
	defer s.modelTokMu.Unlock()
	if s.modelTokens == nil {
		s.modelTokens = map[int64]modelToken{}
	}
	s.modelTokens[id] = modelToken{access: g.AccessToken, expiry: g.Expiry, keyVersion: keyVersion}
}

func (s *Service) forgetModelToken(id int64) {
	s.modelTokMu.Lock()
	delete(s.modelTokens, id)
	s.modelTokMu.Unlock()
}

// dropRejectedToken forgets service id's cached access token if it is still
// the one a request was refused with, so a late refusal does not evict a
// token another run has refreshed since.
func (s *Service) dropRejectedToken(id int64, access string) {
	s.modelTokMu.Lock()
	if access != "" && s.modelTokens[id].access == access {
		delete(s.modelTokens, id)
	}
	s.modelTokMu.Unlock()
}

// runRejected drops the token a run was refused with. Not for an ineligible
// ChatGPT account: refreshing cannot change that, it only rotates the token.
func (s *Service) runRejected(m ai.ResolvedModel, e *ai.Error) {
	if e != nil && e.Code == ai.CodeUnauthorized && e.Detail != ai.DetailChatGPTEligibility {
		s.dropRejectedToken(m.ServiceID, m.APIKey)
	}
}

// errModelChanged: the service was removed or signed in again while its token refreshed.
var errModelChanged = errors.New("the model service changed while its sign-in refreshed; try again")

// modelCredential is the credential requests to m should carry: its API key,
// or for a subscription service a cached or refreshed access token.
// The caller must not hold modelMu: refreshing touches the network and keychain.
func (s *Service) modelCredential(ctx context.Context, m ModelService) (string, error) {
	if ModelProviderLogin(m.Provider) != "oauth" {
		return s.modelKey(m)
	}
	s.modelTokMu.Lock()
	seen := s.modelTokens[m.ID]
	s.modelTokMu.Unlock()
	if seen.fresh(m.keyVersion) {
		return seen.access, nil
	}
	sem := s.modelRefreshLock(m.ID)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	s.modelTokMu.Lock()
	cur := s.modelTokens[m.ID]
	if cur.fresh(m.keyVersion) {
		s.modelTokMu.Unlock()
		return cur.access, nil
	}
	delete(s.modelTokens, m.ID)
	s.modelTokMu.Unlock()

	rt, err := s.modelKey(m)
	if err != nil {
		return "", err
	}
	if rt == "" {
		_ = s.putCapStatus(ctx, m.ID, m.Version, CapGen, CapStatus{State: "fail", Code: "auth", HTTPStatus: 401, At: time.Now()})
		return "", ErrModelReauth
	}
	cfg, err := s.loginConfig(ctx, modelLogin(m), m.Provider, m.Email)
	if err != nil {
		return "", err
	}
	if m.clientID != "" {
		cfg.ClientID = m.clientID
	}
	rctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	g, err := auth.RefreshForm(rctx, cfg, http.DefaultClient, rt)
	cancel()
	if errors.Is(err, auth.ErrInvalidGrant) {
		_ = s.putCapStatus(ctx, m.ID, m.Version, CapGen, CapStatus{State: "fail", Code: "auth", HTTPStatus: 401, At: time.Now()})
		return "", ErrModelReauth
	}
	if err != nil {
		return "", err
	}
	// The old refresh token is spent: a cancelled caller must not lose the new one.
	ctx = context.WithoutCancel(ctx)
	// Under modelMu, as a removal or re-login is: neither may have happened since m
	// was read, or the write below would recreate a removed service's credential.
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	if cur, err := s.modelServiceBy(ctx, "id = ?", m.ID); err != nil || cur.keyVersion != m.keyVersion {
		if err != nil && !errors.Is(err, ErrModelServiceNotFound) {
			return "", err
		}
		return "", errModelChanged
	}
	if g.RefreshToken != "" {
		if err := keychainSet(s.modelKeyUser(m.ID, m.keyVersion), g.RefreshToken); err != nil {
			return "", fmt.Errorf("save rotated refresh token: %w", err)
		}
	}
	s.cacheModelToken(m.ID, m.keyVersion, g)
	return g.AccessToken, nil
}

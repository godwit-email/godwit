package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	cfg, err := s.modelOAuthConfig(ctx, m.Provider, m.Email)
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
	if g.RefreshToken != "" {
		if err := keychainSet(s.modelKeyUser(m.ID, m.keyVersion), g.RefreshToken); err != nil {
			return "", fmt.Errorf("save rotated refresh token: %w", err)
		}
	}
	s.cacheModelToken(m.ID, m.keyVersion, g)
	return g.AccessToken, nil
}

package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/godwit-email/godwit/core/auth"
)

// grantTTL is how long a finished flow's Grant waits in memory for the call
// that adds or reauthorizes the account.
var grantTTL = 10 * time.Minute

// oauthConfig returns the provider's OAuth settings; tests point it at testoauth.
var oauthConfig = func(providerID string) (auth.OAuthConfig, error) {
	p, ok := auth.ByID(providerID)
	if !ok || p.OAuth == nil {
		return auth.OAuthConfig{}, fmt.Errorf("provider %q does not sign in with OAuth", providerID)
	}
	return *p.OAuth, nil
}

// BeginOAuth starts an authorization flow for the provider and returns its ID
// and the URL to open in the browser. The flow ends after auth.FlowTimeout.
func (s *Service) BeginOAuth(ctx context.Context, providerID, loginHint string) (flowID, authURL string, err error) {
	_, end, err := s.begin(ctx, 0)
	if err != nil {
		return "", "", err
	}
	defer end() // Close waits for this, then closes the flow added below
	cfg, err := oauthConfig(providerID)
	if err != nil {
		return "", "", err
	}
	f, err := auth.StartFlow(cfg, loginHint, http.DefaultClient)
	if err != nil {
		return "", "", err
	}
	flowID = rand.Text()
	s.oauthMu.Lock()
	if s.flows == nil {
		s.flows = map[string]*auth.Flow{}
	}
	s.flows[flowID] = f // ponytail: a flow never awaited or cancelled stays here (closed) until Close; prune if UIs abandon flows
	s.oauthMu.Unlock()
	return flowID, f.AuthURL(), nil
}

// AwaitOAuth waits for the browser to come back and returns the signed-in email.
// The flow is over either way; on success its Grant stays in memory for grantTTL.
// Cancelling ctx cancels the flow.
func (s *Service) AwaitOAuth(ctx context.Context, flowID string) (email string, err error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return "", err
	}
	defer end()
	s.oauthMu.Lock()
	f := s.flows[flowID]
	s.oauthMu.Unlock()
	if f == nil {
		return "", fmt.Errorf("no sign-in flow %q", flowID)
	}
	g, err := f.Wait(ctx)
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.flows[flowID] == f {
		delete(s.flows, flowID)
	}
	if err != nil {
		return "", err
	}
	if s.grants == nil {
		s.grants = map[string]auth.Grant{}
	}
	s.grants[flowID] = g
	time.AfterFunc(grantTTL, func() {
		s.oauthMu.Lock()
		delete(s.grants, flowID)
		s.oauthMu.Unlock()
	})
	return g.Email, nil
}

// CancelOAuth ends the flow (a pending AwaitOAuth returns an error) and drops
// its grant. Unknown IDs are ignored.
func (s *Service) CancelOAuth(flowID string) {
	s.oauthMu.Lock()
	f := s.flows[flowID]
	delete(s.flows, flowID)
	delete(s.grants, flowID)
	s.oauthMu.Unlock()
	if f != nil {
		f.Close()
	}
}

// takeGrant removes and returns a finished flow's grant.
func (s *Service) takeGrant(flowID string) (auth.Grant, bool) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	g, ok := s.grants[flowID]
	delete(s.grants, flowID)
	return g, ok
}

// closeOAuth ends every flow and drops every grant; Close calls it after the
// calls in flight have ended.
func (s *Service) closeOAuth() {
	s.oauthMu.Lock()
	fs := slices.Collect(maps.Values(s.flows))
	s.flows, s.grants = nil, nil
	s.oauthMu.Unlock()
	for _, f := range fs {
		f.Close()
	}
}

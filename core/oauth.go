package core

import (
	"context"
	"crypto/rand"
	"errors"
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
		s.flows = map[string]oauthFlow{}
	}
	s.flows[flowID] = oauthFlow{f, providerID}
	s.oauthMu.Unlock()
	// The flow closes itself at auth.FlowTimeout; forget it then too, so a result
	// nobody awaits does not stay in memory. A pending AwaitOAuth gets ErrFlowExpired.
	time.AfterFunc(auth.FlowTimeout, func() {
		s.oauthMu.Lock()
		if s.flows[flowID].Flow == f {
			delete(s.flows, flowID)
		}
		s.oauthMu.Unlock()
	})
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
	of := s.flows[flowID]
	s.oauthMu.Unlock()
	f := of.Flow
	if f == nil {
		return "", ErrUnknownFlow
	}
	g, err := f.Wait(ctx)
	if afterOAuthWait != nil {
		afterOAuthWait(flowID)
	}
	// Commit only while the flow is still ours: CancelOAuth, the expiry or another
	// AwaitOAuth may have taken it while Wait returned.
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.flows[flowID].Flow != f {
		if err == nil {
			err = ErrUnknownFlow
		}
		return "", err
	}
	delete(s.flows, flowID)
	if err != nil {
		return "", err
	}
	if s.grants == nil {
		s.grants = map[string]oauthGrant{}
	}
	s.grants[flowID] = oauthGrant{g, of.provider}
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
	f := s.flows[flowID].Flow
	delete(s.flows, flowID)
	delete(s.grants, flowID)
	s.oauthMu.Unlock()
	if f != nil {
		f.Close()
	}
}

// oauthFlow is a flow begun for a provider.
type oauthFlow struct {
	*auth.Flow
	provider string
}

// oauthGrant is a finished flow's grant and the provider it was signed in with.
type oauthGrant struct {
	auth.Grant
	provider string
}

// takeGrant removes and returns a finished flow's grant.
func (s *Service) takeGrant(flowID string) (oauthGrant, bool) {
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

// ErrUnknownFlow: no such flow ID, or it was already awaited, cancelled or expired.
var ErrUnknownFlow = errors.New("no such sign-in flow")

// afterOAuthWait is a test hook between a flow's Wait returning and AwaitOAuth committing its result.
var afterOAuthWait func(flowID string)

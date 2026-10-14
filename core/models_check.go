package core

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/core/ai"
)

// ServiceCheck is what probing a service before saving found. Endpoint and
// Models are generate's ("" and nil when it is off); JudgeEndpoint and
// JudgeModels judge's.
type ServiceCheck struct {
	BaseURL, Endpoint string
	Models            []string
	ListSupported     bool // false: the service has no model list; models are typed by hand
	NoListStatus      int  // the HTTP status that said so, when known
	JudgeEndpoint     string
	JudgeModels       []string // nil until there is a key, except for ProviderOther
}

// CheckModelService probes in as service id (0: a new one) would be saved,
// without saving: the key action decides the key, and a kept key never goes
// to a changed address or protocol (ErrKeyNeedsConfirm, no request).
func (s *Service) CheckModelService(ctx context.Context, id int64, in ModelServiceInput) (ServiceCheck, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ServiceCheck{}, err
	}
	defer end()
	s.modelMu.RLock()
	action, key, old, err := s.prepare(ctx, id, &in)
	s.modelMu.RUnlock()
	if err == nil && ModelProviderLogin(in.Provider) == "oauth" {
		if in.oauthGrant.AccessToken != "" {
			key = in.oauthGrant.AccessToken
		} else if action == KeyKeep {
			key, err = s.modelCredential(ctx, old)
		}
	}
	if err != nil {
		return ServiceCheck{}, err
	}
	c := ServiceCheck{BaseURL: in.BaseURL}
	if in.Kind != "" {
		c.Endpoint, c.ListSupported = ai.Endpoint(in.Kind, in.BaseURL), true
		c.Models, err = ai.ListModels(ctx, s.modelHTTP, in.Kind, in.BaseURL, key)
		var nl *ai.NoListError
		if errors.As(err, &nl) {
			c.ListSupported, c.NoListStatus, c.Models, err = false, nl.Status, nil, nil
		}
		if err != nil {
			return ServiceCheck{}, err
		}
	}
	if in.JudgeFormat != "" {
		f := ai.JudgeFormat(in.JudgeFormat)
		c.JudgeEndpoint = in.BaseURL + f.Path()
		// A gateway's /models lists every chat model: other gets the format's own list.
		switch {
		case in.Provider == ProviderOther:
			c.JudgeModels = f.Models()
		case key != "":
			c.JudgeModels, err = ai.ListJudgeModels(ctx, s.modelHTTP, f, in.BaseURL, key)
			if errors.Is(err, ai.ErrModelAuth) {
				return ServiceCheck{}, err
			}
			if err != nil || len(c.JudgeModels) == 0 {
				c.JudgeModels = f.Models()
			}
		}
	}
	return c, nil
}

// NormalizeModelBaseURL is the address a service is saved with, whatever its
// formats: ErrBaseURLInvalid or ErrBaseURLScheme says why raw is refused.
func NormalizeModelBaseURL(raw string) (string, error) { return ai.NormalizeBaseURL(raw) }

// ModelEndpoint is the URL requests to a service at base will hit.
func ModelEndpoint(kind, base string) string { return ai.Endpoint(kind, base) }

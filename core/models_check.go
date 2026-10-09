package core

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/core/ai"
)

// ServiceCheck is what probing a service before saving found.
type ServiceCheck struct {
	BaseURL, Endpoint string
	Models            []string
	ListSupported     bool // false: the service has no model list; models are typed by hand
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
	_, key, err := s.prepare(ctx, id, &in)
	s.modelMu.RUnlock()
	if err != nil {
		return ServiceCheck{}, err
	}
	c := ServiceCheck{BaseURL: in.BaseURL, Endpoint: ai.Endpoint(in.Kind, in.BaseURL), ListSupported: true}
	c.Models, err = ai.ListModels(ctx, s.modelHTTP, in.Kind, in.BaseURL, key)
	if errors.Is(err, ai.ErrNoModelList) {
		return ServiceCheck{BaseURL: c.BaseURL, Endpoint: c.Endpoint}, nil
	}
	if err != nil {
		return ServiceCheck{}, err
	}
	return c, nil
}

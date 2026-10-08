// Package ai talks to model services. It must not import core.
package ai

import (
	"fmt"
	"strings"
)

// ModelRef names a model as "service/model"; the first "/" ends the service name.
type ModelRef struct{ Service, Model string }

func ParseModelRef(s string) (ModelRef, error) {
	svc, model, ok := strings.Cut(s, "/")
	if !ok || svc == "" || model == "" {
		return ModelRef{}, fmt.Errorf("invalid model reference %q: want service/model", s)
	}
	return ModelRef{svc, model}, nil
}

// ResolvedModel is a model reference resolved against a stored service.
type ResolvedModel struct {
	ServiceID, Version           int64
	Kind, BaseURL, Model, APIKey string
}

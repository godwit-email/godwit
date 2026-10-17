package ai

import (
	"context"
	"errors"
	"net/http"
)

// ModelSession is independent of mail, conversation and machine identities.
type ModelSession struct {
	ID        string
	CreatedAt int64 // Unix milliseconds
}
type modelSessionKey struct{}

func WithModelSession(ctx context.Context, s ModelSession) context.Context {
	return context.WithValue(ctx, modelSessionKey{}, s)
}
func ModelSessionFromContext(ctx context.Context) ModelSession {
	s, _ := ctx.Value(modelSessionKey{}).(ModelSession)
	return s
}

type RequestPreparer func(context.Context, *http.Request) (*http.Request, error)
type preparingTransport struct {
	base    http.RoundTripper
	prepare RequestPreparer
}

func (t preparingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, errors.New("model request body cannot be replayed")
		}
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		copy.Body = body
		defer body.Close()
	}
	prepared, err := t.prepare(req.Context(), copy)
	if err != nil {
		return nil, err
	}
	return t.base.RoundTrip(prepared)
}

// PreparingClient adapts each attempt without mutating the SDK's retry template.
func PreparingClient(base *http.Client, prepare RequestPreparer) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	tr := base.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	copy.Transport = preparingTransport{base: tr, prepare: prepare}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

// ModelRequestError marks host-validated provider errors safe to return without URL text.
type ModelRequestError interface {
	error
	ModelRequestError()
}

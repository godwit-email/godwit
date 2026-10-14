package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Model is a listed model: its ID and, when the service gives one, a display name.
type Model struct{ ID, Name string }

// ListModels asks a service for its model IDs, sorted. Errors never include the
// key or the response body.
func ListModels(ctx context.Context, c *http.Client, kind, baseURL, apiKey string) ([]string, error) {
	ms, err := ListModelsNamed(ctx, c, kind, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids, nil
}

// ListModelsNamed is ListModels with display names. It reads both the API's
// data[].id shape and the Sign in with ChatGPT models[].slug/display_name shape.
func ListModelsNamed(ctx context.Context, c *http.Client, kind, baseURL, apiKey string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := strings.TrimRight(baseURL, "/")
	url, hdr := base+"/models", http.Header{}
	if strings.HasSuffix(base, "/backend-api/codex") {
		// ChatGPT's Codex backend 400s without a client version, and lists only the
		// models that version may use (minimal_client_version). The Codex CLI's.
		url += "?client_version=" + codexClientVersion
	}
	switch kind {
	case KindOpenAI, KindResponses:
		if apiKey != "" {
			hdr.Set("Authorization", "Bearer "+apiKey)
		}
	case KindAnthropic:
		url = AnthropicBase(base) + "/v1/models"
		hdr.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			hdr.Set("x-api-key", apiKey)
		}
	default:
		return nil, fmt.Errorf("unknown service kind %q", kind)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("list models: bad base URL")
	}
	req.Header = hdr
	if c == nil {
		c = NoRedirectClient
	}
	resp, err := c.Do(req)
	if err != nil {
		// Never the *url.Error text: it can carry a server-supplied Location.
		for _, ce := range []error{context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, ce) {
				return nil, fmt.Errorf("list models: %w", ce)
			}
		}
		return nil, errors.New("list models: could not reach the model service")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrModelAuth
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return nil, &NoListError{resp.StatusCode}
	default:
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID         string
			Visibility string
		}
		Models []struct {
			Slug        string
			DisplayName string `json:"display_name"`
			Visibility  string
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("list models: unexpected response")
	}
	ms := make([]Model, 0, len(out.Data)+len(out.Models))
	for _, m := range out.Data {
		if m.ID != "" && (m.Visibility == "" || m.Visibility == "list") {
			ms = append(ms, Model{ID: m.ID})
		}
	}
	for _, m := range out.Models {
		if m.Slug != "" && (m.Visibility == "" || m.Visibility == "list") {
			ms = append(ms, Model{m.Slug, m.DisplayName})
		}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	return ms, nil
}

// codexClientVersion is the Codex CLI release the model list is asked for; a
// newer model may need a newer one.
const codexClientVersion = "0.159.0"

var (
	ErrModelAuth   = errors.New("the model service rejected the key")
	ErrNoModelList = errors.New("the model service has no model list")
)

// NoRedirectClient never follows redirects, so a key is only ever sent to the
// configured URL; a 3xx comes back as an HTTP error.
var NoRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// NoListError is ErrNoModelList with the status that said so.
type NoListError struct{ Status int }

func (e *NoListError) Error() string        { return ErrNoModelList.Error() }
func (e *NoListError) Is(target error) bool { return target == ErrNoModelList }

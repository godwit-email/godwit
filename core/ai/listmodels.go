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

// ListModels asks a service for its model IDs, sorted. Errors never include the
// key or the response body.
func ListModels(ctx context.Context, c *http.Client, kind, baseURL, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := strings.TrimRight(baseURL, "/")
	url, hdr := base+"/models", http.Header{}
	switch kind {
	case "openai":
		if apiKey != "" {
			hdr.Set("Authorization", "Bearer "+apiKey)
		}
	case "anthropic":
		url = base + "/v1/models"
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct{ ID string }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("list models: unexpected response")
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// NoRedirectClient never follows redirects, so a key is only ever sent to the
// configured URL; a 3xx comes back as an HTTP error.
var NoRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

package graph

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// linkClient is a client on BaseURL whose requests land in got instead of
// going out.
func linkClient(got *[]string) *client {
	c := newClient(Config{Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "a@outlook.com", Token: "tok"}, nil
	}})
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*got = append(*got, r.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	return c
}

// Graph names a page link with the default port spelled out
// (https://graph.microsoft.com:443/v1.0/...) or in another case: that is
// still the account's host, so the link is followed (#888).
func TestPageLinkOnSameHostWithPort(t *testing.T) {
	for _, link := range []string{
		"https://graph.microsoft.com:443/v1.0/me/mailFolders/A/messages/delta?$deltatoken=x",
		"https://Graph.Microsoft.com/v1.0/me/mailFolders/A/messages/delta?$skiptoken=y",
	} {
		var got []string
		_, err := linkClient(&got).do(t.Context(), http.MethodGet, link, "", nil)
		require.NoError(t, err, link)
		assert.Equal(t, []string{link}, got)
	}
}

// A link to another host, or another port, is still refused, and the error
// names the host so it can be reported.
func TestPageLinkToOtherHostRefused(t *testing.T) {
	for link, host := range map[string]string{
		"https://evil.example/v1.0/me/messages":             "evil.example",
		"https://graph.microsoft.com:8443/v1.0/me/messages": "graph.microsoft.com:8443",
	} {
		var got []string
		_, err := linkClient(&got).do(t.Context(), http.MethodGet, link, "", nil)
		require.ErrorIs(t, err, conn.ErrOtherHost, link)
		assert.Contains(t, err.Error(), host)
		assert.Empty(t, got)
	}
}

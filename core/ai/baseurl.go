package ai

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const (
	KindOpenAI    = "openai"
	KindResponses = "openai-responses"
	KindAnthropic = "anthropic"
)

// Why NormalizeBaseURL refused an address.
var (
	ErrBaseURLInvalid = errors.New("enter a valid service address without credentials")
	ErrBaseURLScheme  = errors.New("the address must use https (http only for localhost)")
)

func ValidKind(k string) bool { return k == KindOpenAI || k == KindResponses || k == KindAnthropic }

// NormalizeBaseURL trims a pasted URL down to the service's base, the same
// for every format: a pasted endpoint loses its path, a "/v1" stays. Each
// format derives its endpoint from it (Endpoint, JudgeFormat.Path).
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return "", ErrBaseURLInvalid
	}
	host := u.Hostname()
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", ErrBaseURLScheme
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	for _, s := range []string{"/chat/completions", "/completions", "/responses", "/models", "/embeddings", "/messages", "/decisions", "/systemone"} {
		if strings.HasSuffix(p, s) {
			p = strings.TrimSuffix(p, s)
			break
		}
	}
	return u.Scheme + "://" + u.Host + p, nil
}

// Endpoint is the URL a request will actually hit, for the settings preview.
func Endpoint(kind, base string) string {
	switch kind {
	case KindResponses:
		return base + "/responses"
	case KindAnthropic:
		return AnthropicBase(base) + "/v1/messages"
	}
	return base + "/chat/completions"
}

// AnthropicBase is the root the Anthropic SDK and its /v1 paths expect: the
// base without a trailing "/v1" (older Godwits stored it so).
func AnthropicBase(base string) string { return strings.TrimSuffix(base, "/v1") }

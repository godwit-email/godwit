package ai

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	ok := []struct{ in, want string }{
		{" https://api.openai.com/v1/ ", "https://api.openai.com/v1"},
		{"https://x/v1/chat/completions?x=1#y", "https://x/v1"},
		{"https://x/v1/responses", "https://x/v1"},
		{"https://x/v1/models/", "https://x/v1"},
		{"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1"},
		{"https://api.anthropic.com/v1", "https://api.anthropic.com/v1"},
		{"https://api.anthropic.com", "https://api.anthropic.com"},
		{"https://api.openai.com/v1/decisions", "https://api.openai.com/v1"},
		{"https://api.typesafe.ai/v1/systemone/", "https://api.typesafe.ai/v1"},
		{"http://localhost:11434/v1", "http://localhost:11434/v1"},
		{"http://127.0.0.2:1/v1", "http://127.0.0.2:1/v1"},
		{"https://example.com/proxy%23tenant/v1", "https://example.com/proxy%23tenant/v1"},
		{"https://example.com/a%3Fb%2Fc/v1/models", "https://example.com/a%3Fb%2Fc/v1"},
		{"http://[::1]:8080/v1", "http://[::1]:8080/v1"},
	}
	for _, c := range ok {
		got, err := NormalizeBaseURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"http://example.com/v1", "https://u:p@x/v1", "ftp://x", "", "  "} {
		if got, err := NormalizeBaseURL(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestEndpoint(t *testing.T) {
	for _, c := range []struct{ kind, want string }{
		{KindOpenAI, "https://x/v1/chat/completions"},
		{KindResponses, "https://x/v1/responses"},
		{KindAnthropic, "https://x/v1/messages"},
	} {
		if got := Endpoint(c.kind, base(c.kind)); got != c.want {
			t.Errorf("%s: %q", c.kind, got)
		}
	}
	// One base serves every format: OpenRouter with messages and System One,
	// and an Anthropic base stored without "/v1" by older Godwits.
	or, _ := NormalizeBaseURL("https://openrouter.ai/api/v1/messages")
	if got := Endpoint(KindAnthropic, or); got != "https://openrouter.ai/api/v1/messages" {
		t.Error(got)
	}
	if got := or + FormatSystemOne.Path(); got != "https://openrouter.ai/api/v1/systemone" {
		t.Error(got)
	}
	if got := Endpoint(KindAnthropic, "https://api.anthropic.com/v1"); got != "https://api.anthropic.com/v1/messages" {
		t.Error(got)
	}
}

func base(kind string) string {
	if kind == KindAnthropic {
		return "https://x"
	}
	return "https://x/v1"
}

func TestEndpointKeepsEscapes(t *testing.T) {
	b, _ := NormalizeBaseURL("https://example.com/a%3Fb%2Fc/v1")
	if got := Endpoint(KindOpenAI, b); got != "https://example.com/a%3Fb%2Fc/v1/chat/completions" {
		t.Error(got)
	}
}

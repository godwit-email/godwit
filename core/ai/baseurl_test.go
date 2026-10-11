package ai

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	ok := []struct{ kind, in, want string }{
		{KindOpenAI, " https://api.openai.com/v1/ ", "https://api.openai.com/v1"},
		{KindOpenAI, "https://x/v1/chat/completions?x=1#y", "https://x/v1"},
		{KindResponses, "https://x/v1/responses", "https://x/v1"},
		{KindOpenAI, "https://x/v1/models/", "https://x/v1"},
		{KindAnthropic, "https://api.anthropic.com/v1/messages", "https://api.anthropic.com"},
		{KindAnthropic, "https://api.anthropic.com/v1", "https://api.anthropic.com"},
		{KindOpenAI, "http://localhost:11434/v1", "http://localhost:11434/v1"},
		{KindOpenAI, "http://127.0.0.2:1/v1", "http://127.0.0.2:1/v1"},
		{KindOpenAI, "https://example.com/proxy%23tenant/v1", "https://example.com/proxy%23tenant/v1"},
		{KindOpenAI, "https://example.com/a%3Fb%2Fc/v1/models", "https://example.com/a%3Fb%2Fc/v1"},
		{KindOpenAI, "http://[::1]:8080/v1", "http://[::1]:8080/v1"},
	}
	for _, c := range ok {
		got, err := NormalizeBaseURL(c.kind, c.in)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"http://example.com/v1", "https://u:p@x/v1", "ftp://x", "", "  "} {
		if got, err := NormalizeBaseURL(KindOpenAI, in); err == nil {
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
}

func TestPresets(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if seen[p.ID] || !ValidKind(p.Kind) || len(p.HelpURL) < 8 || p.HelpURL[:8] != "https://" {
			t.Errorf("bad preset %+v", p)
		}
		seen[p.ID] = true
		if got, err := NormalizeBaseURL(p.Kind, p.BaseURL); err != nil || got != p.BaseURL {
			t.Errorf("%s: %q, %v", p.ID, got, err)
		}
		if p.KeyRequired == (p.ID == "ollama" || p.ID == "lmstudio") {
			t.Errorf("%s: KeyRequired wrong", p.ID)
		}
	}
	if len(seen) != 8 {
		t.Errorf("got %d presets", len(seen))
	}
}

func base(kind string) string {
	if kind == KindAnthropic {
		return "https://x"
	}
	return "https://x/v1"
}

func TestEndpointKeepsEscapes(t *testing.T) {
	b, _ := NormalizeBaseURL(KindOpenAI, "https://example.com/a%3Fb%2Fc/v1")
	if got := Endpoint(KindOpenAI, b); got != "https://example.com/a%3Fb%2Fc/v1/chat/completions" {
		t.Error(got)
	}
}

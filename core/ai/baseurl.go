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

func ValidKind(k string) bool { return k == KindOpenAI || k == KindResponses || k == KindAnthropic }

// NormalizeBaseURL trims a pasted URL down to the service root the SDKs expect.
func NormalizeBaseURL(kind, raw string) (string, error) {
	if !ValidKind(kind) {
		return "", errors.New("unknown service kind")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("enter a valid service address without credentials")
	}
	host := u.Hostname()
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("the address must use https (http only for localhost)")
	}
	suffixes := []string{"/chat/completions", "/completions", "/responses", "/models", "/embeddings"}
	if kind == KindAnthropic {
		suffixes = []string{"/v1/messages", "/v1/models", "/messages", "/v1"}
	}
	p := strings.TrimRight(u.Path, "/")
	for _, s := range suffixes {
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
		return base + "/v1/messages"
	}
	return base + "/chat/completions"
}

type Preset struct {
	ID, Name, Kind, BaseURL, HelpURL string
	KeyRequired                      bool
}

func Presets() []Preset {
	return []Preset{
		{"openai", "OpenAI", KindResponses, "https://api.openai.com/v1", "https://platform.openai.com/api-keys", true},
		{"anthropic", "Anthropic", KindAnthropic, "https://api.anthropic.com", "https://console.anthropic.com/settings/keys", true},
		{"openrouter", "OpenRouter", KindOpenAI, "https://openrouter.ai/api/v1", "https://openrouter.ai/settings/keys", true},
		{"gemini", "Google Gemini", KindOpenAI, "https://generativelanguage.googleapis.com/v1beta/openai", "https://aistudio.google.com/apikey", true},
		{"deepseek", "DeepSeek", KindOpenAI, "https://api.deepseek.com", "https://platform.deepseek.com/api_keys", true},
		{"groq", "Groq", KindOpenAI, "https://api.groq.com/openai/v1", "https://console.groq.com/keys", true},
		{"ollama", "Ollama", KindOpenAI, "http://localhost:11434/v1", "https://docs.ollama.com/api/openai-compatibility", false},
		{"lmstudio", "LM Studio", KindOpenAI, "http://localhost:1234/v1", "https://lmstudio.ai/docs/developer/openai-compat", false},
	}
}

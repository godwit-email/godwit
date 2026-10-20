package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
)

// AppleIntelligence is the system's on-device AI provider on macOS,
// offering text generation and classification.
type AppleIntelligence interface {
	// Generate generates text for the given prompt with system instructions.
	Generate(ctx context.Context, system, prompt string) (string, error)
	// Judge evaluates typed questions against input.
	Judge(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error)
	// Version changes when system model or OS update may change outputs.
	Version() string
}

func appleIntelligenceModels() []plugins.ProviderModel {
	return []plugins.ProviderModel{
		{
			ID:        "on-device",
			Name:      "Apple Intelligence",
			Transport: "http",
			Protocol:  "openai-completions",
			BaseURL:   "http://127.0.0.1:0",
			Operation: "small",
			Description: plugins.Text{
				Default: "On-device summaries & quick replies",
				ByLang: map[string]string{
					"de": "Lokale Zusammenfassungen & Schnellantworten",
				},
			},
		},
		{
			ID:        "classifier",
			Name:      "Apple Intelligence",
			Transport: "plugin",
			Operation: "judge",
			Description: plugins.Text{
				Default: "On-device classification & labeling",
				ByLang: map[string]string{
					"de": "Lokale Klassifizierung & Labels",
				},
			},
		},
	}
}

// appleIntelligenceJudge is an ai.Judge backed by AppleIntelligence.
type appleIntelligenceJudge struct {
	ai AppleIntelligence
}

func (j appleIntelligenceJudge) Ask(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	if err := ai.ValidateQuestions(qs); err != nil {
		return nil, err
	}
	return j.ai.Judge(ctx, input, qs)
}

// appleIntelligenceClient returns an http.Client whose Transport connects
// Genkit's compat_oai plugin to Apple Intelligence. It caches the client so
// concurrent task resolutions share one pointer, preventing Genkit instance churn (errStale).
func (s *Service) appleIntelligenceClient() *http.Client {
	if s == nil {
		return &http.Client{
			Transport:     &appleIntelligenceTransport{},
			CheckRedirect: ai.NoRedirectClient.CheckRedirect,
		}
	}
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.appleClient == nil {
		s.appleClient = &http.Client{
			Transport: &appleIntelligenceTransport{
				s:  s,
				ai: s.opts.AppleIntelligence,
			},
			CheckRedirect: ai.NoRedirectClient.CheckRedirect,
		}
	}
	return s.appleClient
}

type appleIntelligenceTransport struct {
	s  *Service
	ai AppleIntelligence
}

func (t *appleIntelligenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if req.Body != nil {
		defer req.Body.Close()
	}

	// Models listing endpoint (e.g. GET /models or /v1/models)
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/models") {
		data := map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{
					"id":       "on-device",
					"object":   "model",
					"created":  time.Now().Unix(),
					"owned_by": "apple",
				},
			},
		}
		b, _ := json.Marshal(data)
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(bytes.NewReader(b)),
			ContentLength: int64(len(b)),
			Request:       req,
		}, nil
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	var chatReq struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &chatReq); err != nil {
		return nil, fmt.Errorf("apple intelligence: invalid chat request: %w", err)
	}

	var systemParts []string
	var dialogParts []string
	userTurns := 0
	lastUserText := ""

	for _, m := range chatReq.Messages {
		text := extractContent(m.Content)
		switch m.Role {
		case "system":
			systemParts = append(systemParts, text)
		case "user":
			userTurns++
			lastUserText = text
			dialogParts = append(dialogParts, "User: "+text)
		case "assistant":
			dialogParts = append(dialogParts, "Assistant: "+text)
		}
	}

	system := strings.Join(systemParts, "\n\n")
	prompt := lastUserText
	if userTurns > 1 || len(dialogParts) > 1 {
		prompt = strings.Join(dialogParts, "\n") + "\nAssistant:"
	}

	temp := 0.2
	if strings.Contains(system, "Summarize the email text") {
		system, temp = adaptAppleIntelligenceSummarize(system)
	} else if strings.Contains(system, "Suggest up to three short, distinct replies") {
		system, temp = adaptAppleIntelligenceQuickReplies(system)
	}

	var outText string
	if genWithTemp, ok := t.ai.(interface {
		GenerateWithTemperature(context.Context, string, string, float64) (string, error)
	}); ok {
		outText, err = genWithTemp.GenerateWithTemperature(ctx, system, prompt, temp)
	} else {
		outText, err = t.ai.Generate(ctx, system, prompt)
	}
	if err != nil {
		return nil, fmt.Errorf("apple intelligence: %w", err)
	}

	outText = strings.TrimPrefix(outText, "Assistant:")
	outText = strings.TrimSpace(outText)

	return t.respondText(ctx, req, chatReq.Model, chatReq.Stream, outText)
}

func (t *appleIntelligenceTransport) respondText(ctx context.Context, req *http.Request, model string, stream bool, text string) (*http.Response, error) {
	if stream {
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			chunks := splitTextIntoChunks(text)
			for _, chunk := range chunks {
				select {
				case <-ctx.Done():
					return
				default:
				}
				chunkObj := map[string]any{
					"id":      "chatcmpl-apple",
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   model,
					"choices": []any{
						map[string]any{
							"index": 0,
							"delta": map[string]any{
								"content": chunk,
							},
							"finish_reason": nil,
						},
					},
				}
				b, _ := json.Marshal(chunkObj)
				fmt.Fprintf(pw, "data: %s\n\n", b)
			}
			stopObj := map[string]any{
				"id":      "chatcmpl-apple",
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []any{
					map[string]any{
						"index":         0,
						"delta":         map[string]any{},
						"finish_reason": "stop",
					},
				},
			}
			b, _ := json.Marshal(stopObj)
			fmt.Fprintf(pw, "data: %s\n\n", b)
			fmt.Fprintf(pw, "data: [DONE]\n\n")
		}()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":  []string{"text/event-stream"},
				"Cache-Control": []string{"no-cache"},
				"Connection":    []string{"keep-alive"},
			},
			Body:    pr,
			Request: req,
		}, nil
	}

	respObj := map[string]any{
		"id":      "chatcmpl-apple",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": "stop",
			},
		},
	}
	respBytes, _ := json.Marshal(respObj)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(respBytes)),
		ContentLength: int64(len(respBytes)),
		Request:       req,
	}, nil
}

func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

func splitTextIntoChunks(text string) []string {
	if text == "" {
		return nil
	}
	var chunks []string
	var cur strings.Builder
	for _, r := range text {
		cur.WriteRune(r)
		if r == ' ' || r == '\n' || r == '.' || r == ',' || r == '!' || r == '?' {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func adaptAppleIntelligenceSummarize(system string) (string, float64) {
	langMatch := regexp.MustCompile(`(?i)Answer in\s+([a-zA-Z_-]+)`).FindStringSubmatch(system)
	lang := "en"
	if len(langMatch) > 1 {
		lang = strings.ToLower(langMatch[1])
	}

	if strings.HasPrefix(lang, "de") {
		return "Fasse die E-Mail zwingend auf Deutsch in 3 bis 5 prägnanten Stichpunkten zusammen. " +
			"Wichtig: Die Zusammenfassung muss ausnahmslos auf Deutsch formuliert werden, selbst wenn die ursprüngliche E-Mail in einer anderen Sprache (wie Englisch) verfasst ist.\n" +
			"- 3 bis 5 Stichpunkte mit den wichtigsten Kernpunkten, Entscheidungen und Fristen.\n" +
			"- Keine Einleitung und keine Schlussbemerkung.", 0.2
	}

	langNames := map[string]string{
		"en": "English", "fr": "French", "es": "Spanish", "it": "Italian", "nl": "Dutch",
		"pt": "Portuguese", "ru": "Russian", "ja": "Japanese", "zh": "Chinese",
	}
	targetName := "the requested language"
	prefix := lang
	if idx := strings.IndexAny(prefix, "-_"); idx > 0 {
		prefix = prefix[:idx]
	}
	if name, ok := langNames[prefix]; ok {
		targetName = name
	}

	return fmt.Sprintf("Summarize the email strictly in %s in 3 to 5 concise bullet points.\n"+
		"Important: The summary must be written in %s, even if the source email is in another language.\n"+
		"- 3 to 5 bullet points with key points, decisions and deadlines.\n"+
		"- No preamble and no closing remarks.", targetName, targetName), 0.2
}

func adaptAppleIntelligenceQuickReplies(system string) (string, float64) {
	langInst := "Write the replies in the language of the email."
	if idx := strings.Index(system, "Write the replies in "); idx != -1 {
		langInst = strings.TrimSpace(system[idx:])
	}
	adapted := "Suggest up to three short, distinct replies the user could send to the email you are given, written as the user.\n" +
		"Answer with valid JSON only, in this exact format:\n" +
		"{\"replies\": [{\"html\": \"<p>first reply</p>\"}, {\"html\": \"<p>second reply</p>\"}, {\"html\": \"<p>third reply</p>\"}]}\n" +
		"Use simple HTML (paragraphs, line breaks). No greeting placeholders, no signature, no quoted text. If no reply is needed, return {\"replies\": []}.\n\n" +
		ai.UntrustedSystemNote + "\n\n" +
		langInst
	return adapted, 0.1
}

// FindAppleIntelligenceHelper finds the bundled or local apple intelligence helper binary.
func FindAppleIntelligenceHelper() string {
	var candidates []string

	// 1. App bundle Resources directory (e.g. Godwit.app/Contents/Resources/apple-intelligence-helper)
	if exe, err := os.Executable(); err == nil {
		resDir := filepath.Join(filepath.Dir(exe), "..", "Resources", "apple-intelligence-helper")
		candidates = append(candidates, resDir)
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "apple-intelligence-helper"))
	}

	// 2. Relative resources from current working dir or project root
	candidates = append(candidates,
		"resources/darwin-arm64/apple-intelligence-helper",
		"../resources/darwin-arm64/apple-intelligence-helper",
	)

	// 3. User's local installation
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "apple-intelligence-cli"),
			filepath.Join(home, "Library", "Application Support", "Godwit", "bin", "apple-intelligence-helper"),
		)
	}

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (fi.Mode()&0111 != 0) {
			return p
		}
	}
	return ""
}

// DefaultAppleIntelligence is the built-in Apple Intelligence implementation.
type DefaultAppleIntelligence struct {
	ver        string
	helperPath string
}

// NewDefaultAppleIntelligence creates a default Apple Intelligence engine.
func NewDefaultAppleIntelligence() *DefaultAppleIntelligence {
	return &DefaultAppleIntelligence{
		ver:        "apple-intelligence-1.0",
		helperPath: FindAppleIntelligenceHelper(),
	}
}

// HelperPath returns the active path to the apple-intelligence-helper binary.
func (d *DefaultAppleIntelligence) HelperPath() string {
	if d.helperPath == "" {
		d.helperPath = FindAppleIntelligenceHelper()
	}
	return d.helperPath
}

// SetHelperPath overrides the helper path (for tests).
func (d *DefaultAppleIntelligence) SetHelperPath(p string) {
	d.helperPath = p
}

func (d *DefaultAppleIntelligence) Version() string {
	return d.ver
}

// Available reports whether Apple Intelligence is available on this Mac.
func (d *DefaultAppleIntelligence) Available() bool {
	bin := d.HelperPath()
	if bin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--check")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var res struct {
		Available bool `json:"available"`
	}
	return json.Unmarshal(out, &res) == nil && res.Available
}

// Generate produces text for the prompt using Apple's FoundationModels.
func (d *DefaultAppleIntelligence) Generate(ctx context.Context, system, prompt string) (string, error) {
	return d.GenerateWithTemperature(ctx, system, prompt, 0.1)
}

// GenerateWithTemperature produces text for the prompt using Apple's FoundationModels with a custom temperature.
func (d *DefaultAppleIntelligence) GenerateWithTemperature(ctx context.Context, system, prompt string, temp float64) (string, error) {
	bin := d.HelperPath()
	if bin == "" {
		return "", errors.New("apple intelligence helper binary not found")
	}

	payload, err := json.Marshal(map[string]any{
		"system":         system,
		"text":           prompt,
		"detectLanguage": false,
		"temperature":    temp,
	})
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, "--json", string(payload))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("apple intelligence helper (%s): %w (stderr: %s)", bin, err, stderr.String())
	}

	var res struct {
		Success bool   `json:"success"`
		Text    string `json:"text"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return "", fmt.Errorf("apple intelligence: invalid response from helper: %w (output: %s)", err, stdout.String())
	}

	if !res.Success {
		return "", fmt.Errorf("apple intelligence error: %s", res.Error)
	}

	return strings.TrimSpace(res.Text), nil
}

// Judge evaluates questions against input using Apple Intelligence.
func (d *DefaultAppleIntelligence) Judge(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	gen := func(ctx context.Context, system, prompt string) (string, error) {
		out, err := d.Generate(ctx, system, prompt)
		if err != nil {
			return "", err
		}
		return normalizeJudgeJSON(out), nil
	}
	return ai.NewGenJudge(gen).Ask(ctx, input, qs)
}

func normalizeJudgeJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			s = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	if strings.Contains(s, `"answers"`) {
		return s
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return `{"answers":` + s + `}`
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		if strings.Contains(s, `"name"`) {
			return `{"answers":[` + s + `]}`
		}
	}
	return s
}

// AppleIntelligence returns the system's Apple Intelligence provider, if any.
func (s *Service) AppleIntelligence() AppleIntelligence {
	return s.opts.AppleIntelligence
}

// SetAppleIntelligence replaces the system's Apple Intelligence provider.
func (s *Service) SetAppleIntelligence(ai AppleIntelligence) {
	s.opts.AppleIntelligence = ai
}

func (s *Service) appleIntelligenceAvailable() bool {
	return s.opts.AppleIntelligence != nil
}

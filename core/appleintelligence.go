package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os/exec"
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
	isSummarize := false
	isQuickReplies := false
	if strings.Contains(system, "Summarize the email text") {
		isSummarize = true
		system, temp = adaptAppleIntelligenceSummarize(system)
	} else if strings.Contains(system, "Suggest up to three short, distinct replies") {
		isQuickReplies = true
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

	if isSummarize {
		outText = cleanAppleIntelligenceSummary(outText)
	} else if isQuickReplies {
		outText = formatQuickRepliesJSON(outText)
	}

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

func languageName(code string) string {
	prefix := strings.ToLower(strings.TrimSpace(code))
	if idx := strings.IndexAny(prefix, "-_"); idx > 0 {
		if prefix[:idx] == "zh" {
			switch prefix[idx+1:] {
			case "hant", "tw", "hk", "mo":
				return "Traditional Chinese"
			}
			return "Simplified Chinese"
		}
		prefix = prefix[:idx]
	}
	names := map[string]string{
		"de": "German",
		"en": "English",
		"es": "Spanish",
		"fr": "French",
		"it": "Italian",
		"ja": "Japanese",
		"ko": "Korean",
		"nl": "Dutch",
		"pt": "Portuguese",
		"ru": "Russian",
		"tr": "Turkish",
		"vi": "Vietnamese",
		"zh": "Chinese",
	}
	if name, ok := names[prefix]; ok {
		return name
	}
	return code
}

func adaptAppleIntelligenceSummarize(system string) (string, float64) {
	langMatch := regexp.MustCompile(`(?i)Answer in\s+([a-zA-Z_-]+)`).FindStringSubmatch(system)
	lang := "en"
	if len(langMatch) > 1 {
		lang = strings.ToLower(langMatch[1])
	}

	if strings.HasPrefix(lang, "de") {
		return "Fasse den Inhalt der E-Mail prägnant in 2 bis 4 Stichpunkten auf Deutsch zusammen.\n" +
			"- Nenne nur Tatsachen, die tatsächlich im Text vorkommen.\n" +
			"- Keine negativen Feststellungen (wie 'keine Fristen' oder 'nicht erwähnt').\n" +
			"- Keine Einleitung, keine Schlussbemerkung und keine Systemhinweise.\n\n" + ai.UntrustedSystemNote, 0.2
	}

	targetName := languageName(lang)
	return fmt.Sprintf("Summarize the email concisely in 2 to 4 factual bullet points in %s.\n"+
		"- Mention only facts that actually appear in the text.\n"+
		"- Do not mention what is missing (no statements like 'no deadlines' or 'not mentioned').\n"+
		"- No preamble, no closing remarks, and no system notes.\n\n%s", targetName, ai.UntrustedSystemNote), 0.2
}

func adaptAppleIntelligenceQuickReplies(system string) (string, float64) {
	langInst := "Write the replies in the language of the email."
	if idx := strings.Index(system, "Write the replies in "); idx != -1 {
		raw := strings.TrimSpace(system[idx:])
		if strings.Contains(raw, "language of the email") {
			langInst = "Write the replies in the language of the email."
		} else {
			target := strings.TrimPrefix(raw, "Write the replies in ")
			target = strings.TrimSuffix(target, ".")
			target = strings.TrimSpace(target)
			name := languageName(target)
			langInst = fmt.Sprintf("Write the replies strictly in %s.", name)
		}
	}
	adapted := "Suggest up to three short, distinct replies the user could send to the email you are given, written as the user.\n" +
		"- Format your output as a simple list with one reply per line starting with a dash (-).\n" +
		"- Do not output JSON, HTML tags, numbering, greetings, or signatures.\n" +
		"- Write 3 short, natural replies from the user's perspective.\n\n" +
		ai.UntrustedSystemNote + "\n\n" +
		langInst
	return adapted, 0.3
}

func cleanAppleIntelligenceSummary(s string) string {
	lines := strings.Split(s, "\n")
	var cleaned []string
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		lower := strings.ToLower(trim)
		if strings.Contains(lower, "keine explizite nennung") ||
			strings.Contains(lower, "keine nennung von apple") ||
			strings.Contains(lower, "keine fristen oder entscheidungen") ||
			strings.Contains(lower, "zugrundeliegenden technologie") ||
			strings.Contains(lower, "no explicit mention of apple") ||
			strings.Contains(lower, "no deadlines or decisions") {
			continue
		}
		cleaned = append(cleaned, l)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

type quickReplyItem struct {
	HTML string `json:"html"`
}

type quickRepliesResp struct {
	Replies []quickReplyItem `json:"replies"`
}

// quickReplyMarker is a list marker: a bullet, or "1." / "1)".
var quickReplyMarker = regexp.MustCompile(`^(?:[-*•]|\d{1,2}[.)])\s*`)

func extractQuickReplies(raw string) []string {
	lines := strings.Split(raw, "\n")
	var bulletLines []string
	var plainLines []string

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(trimmed, ":") {
			continue
		}
		marker := quickReplyMarker.FindString(trimmed)
		isBullet := marker != ""
		// Only the list marker goes: "- 10:00 works" keeps its leading digits.
		content := strings.TrimLeft(trimmed[len(marker):], "\"'“„”")
		content = strings.TrimRight(content, "\"'“„”")
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}

		if isBullet {
			bulletLines = append(bulletLines, content)
		} else {
			lower := strings.ToLower(content)
			if !strings.HasPrefix(lower, "here are") &&
				!strings.HasPrefix(lower, "hier sind") &&
				!strings.HasPrefix(lower, "sure") &&
				!strings.HasPrefix(lower, "gerne") {
				plainLines = append(plainLines, content)
			}
		}
	}

	if len(bulletLines) > 0 {
		if len(bulletLines) > 3 {
			return bulletLines[:3]
		}
		return bulletLines
	}
	if len(plainLines) > 3 {
		return plainLines[:3]
	}
	return plainLines
}

func formatQuickRepliesJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") && strings.Contains(raw, `"replies"`) {
		var check struct {
			Replies []struct {
				HTML string `json:"html"`
			} `json:"replies"`
		}
		if err := json.Unmarshal([]byte(raw), &check); err == nil && len(check.Replies) > 0 {
			return raw
		}
	}

	replies := extractQuickReplies(raw)
	items := make([]quickReplyItem, 0, len(replies))
	for _, r := range replies {
		items = append(items, quickReplyItem{
			HTML: "<p>" + html.EscapeString(r) + "</p>",
		})
	}

	resp := quickRepliesResp{
		Replies: items,
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// DefaultAppleIntelligence is the built-in Apple Intelligence implementation:
// FoundationModels through the bundled helper binary.
type DefaultAppleIntelligence struct {
	ver        string
	helperPath string
}

// NewDefaultAppleIntelligence runs the helper at helperPath.
func NewDefaultAppleIntelligence(helperPath string) *DefaultAppleIntelligence {
	return &DefaultAppleIntelligence{ver: "apple-intelligence-1.0", helperPath: helperPath}
}

func (d *DefaultAppleIntelligence) Version() string {
	return d.ver
}

// Available reports whether Apple Intelligence is available on this Mac.
func (d *DefaultAppleIntelligence) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.helperPath, "--check")
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
	payload, err := json.Marshal(map[string]any{
		"system":         system,
		"text":           prompt,
		"detectLanguage": false,
		"temperature":    temp,
	})
	if err != nil {
		return "", err
	}

	// stdin, not argv: argv is visible to other processes and capped by ARG_MAX.
	cmd := exec.CommandContext(ctx, d.helperPath)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("apple intelligence helper: %w (%s%s)", err, stdout.String(), stderr.String())
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

func (s *Service) appleIntelligenceAvailable() bool {
	return s.opts.AppleIntelligence != nil
}

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
	s  *Service
	ai AppleIntelligence
}

func (j appleIntelligenceJudge) Ask(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	if err := ai.ValidateQuestions(qs); err != nil {
		return nil, err
	}
	// A labeling batch keeps its judge: stop once the user turns the plugin off.
	if on, err := j.s.builtinOn(ctx, AppleIntelligencePlugin); err != nil || !on {
		return nil, fmt.Errorf("%w: plugin/%s", ErrModelUnavailable, AppleIntelligencePlugin)
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

	switch {
	case len(prompt) <= appleIntelligencePromptBytes:
	case isSummarize || isQuickReplies:
		// Mail blocks run oldest to newest: keep the newest.
		prompt = truncateHead(prompt, appleIntelligencePromptBytes)
	default:
		// Structured requests (translation batches, ...) must not be cut.
		return nil, fmt.Errorf("apple intelligence: %w: the input is too long for the on-device model", ErrModelUnavailable)
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
			// Any subtag may carry it: zh-Hant, zh-TW, zh-Hant-HK.
			for _, sub := range strings.FieldsFunc(prefix[idx+1:], func(r rune) bool { return r == '-' || r == '_' }) {
				switch sub {
				case "hant", "tw", "hk", "mo":
					return "Traditional Chinese"
				}
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

// NewDefaultAppleIntelligence runs the helper at helperPath; version names
// what may change its outputs (the OS build, the app that ships the helper).
func NewDefaultAppleIntelligence(helperPath, version string) *DefaultAppleIntelligence {
	return &DefaultAppleIntelligence{ver: version, helperPath: helperPath}
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

// appleIntelligencePromptBytes keeps a prompt inside the on-device model's
// 4,096-token context with room for instructions and the answer: about
// 2,700 tokens of CJK (3 bytes a token) and fewer of Latin text.
// shortcut: byte-based estimate, not the model's tokenizer; upgrade to
// SystemLanguageModel.tokenCount once Godwit requires a macOS that has it.
const appleIntelligencePromptBytes = 8000

// truncateForContext cuts s to at most n bytes on a rune boundary.
func truncateForContext(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := max(n, 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// judgeFramingBytes is room for what ai.NewGenJudge wraps around the input
// and questions; judgeMinInputBytes the least input a batch is asked about.
const (
	judgeFramingBytes  = 1500
	judgeMinInputBytes = 2000
)

// judgeBatches splits qs so each batch's questions leave the input at least
// judgeMinInputBytes, and at most 128 a batch (ai's maxQuestions).
func judgeBatches(qs []ai.Question) [][]ai.Question {
	var out [][]ai.Question
	var cur []ai.Question
	size := 0
	for _, q := range qs {
		b, _ := json.Marshal(q)
		if len(cur) > 0 && (len(cur) == 128 || size+len(b) > appleIntelligencePromptBytes-judgeFramingBytes-judgeMinInputBytes) {
			out, cur, size = append(out, cur), nil, 0
		}
		cur, size = append(cur, q), size+len(b)
	}
	return append(out, cur)
}

// truncateHead drops the start of s so at most n bytes remain, on a rune boundary.
func truncateHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - max(n, 0)
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
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
	judge := ai.NewGenJudge(gen)
	var out []ai.Answer
	for _, batch := range judgeBatches(qs) {
		// Cut the input, not the whole prompt: the batch's questions come after it.
		q, _ := json.Marshal(batch)
		budget := appleIntelligencePromptBytes - len(q) - judgeFramingBytes
		if budget < judgeMinInputBytes { // one question too long to leave room for the mail
			return nil, fmt.Errorf("apple intelligence: question %q is too long for the on-device model", batch[0].Name)
		}
		as, err := judge.Ask(ctx, truncateForContext(input, budget), batch)
		if err != nil {
			return nil, err
		}
		out = append(out, as...)
	}
	return out, nil
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

// appleIntelligenceVersion is the model version cache keys use: it follows
// a.Version() and the prompt adaptations in this file.
func appleIntelligenceVersion(a AppleIntelligence) int64 {
	h := fnv.New64a()
	fmt.Fprint(h, a.Version(), "\x00", appleIntelligenceAdaptations)
	return int64(h.Sum64() >> 1)
}

// appleIntelligenceAdaptations: bump when the prompt adaptations or output
// cleanup here change, so cached outputs are regenerated.
const appleIntelligenceAdaptations = 1

func (s *Service) appleIntelligenceAvailable() bool {
	return s.opts.AppleIntelligence != nil
}

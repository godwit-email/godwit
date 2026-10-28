package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAppleIntelligence struct {
	ver      string
	genText  string
	genErr   error
	judgeAns []ai.Answer
	judgeErr error
	calls    int
	asked    string // the last judge input
}

func (f *fakeAppleIntelligence) Version() string { return f.ver }

func (f *fakeAppleIntelligence) Generate(_ context.Context, system, prompt string) (string, error) {
	f.calls++
	if f.genErr != nil {
		return "", f.genErr
	}
	if f.genText != "" {
		return f.genText, nil
	}
	return "generated response for " + prompt, nil
}

func (f *fakeAppleIntelligence) Judge(_ context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	f.calls++
	f.asked = input
	if f.judgeErr != nil {
		return nil, f.judgeErr
	}
	if f.judgeAns != nil {
		return f.judgeAns, nil
	}
	answers := make([]ai.Answer, len(qs))
	for i, q := range qs {
		answers[i] = ai.Answer{Name: q.Name, Type: q.Type}
		if q.Type == ai.Predicate {
			p := 0.95
			answers[i].Probability = &p
		}
	}
	return answers, nil
}

func TestAppleIntelligenceBuiltinListing(t *testing.T) {
	e := newSkillEnv(t)

	// When AppleIntelligence is nil, not offered
	ps, err := e.s.ListPlugins(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(ps, func(p PluginInfo) bool { return p.Name == AppleIntelligencePlugin }))

	ts, err := e.s.ListTranslators(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(ts, func(x TranslatorInfo) bool { return x.Name == AppleIntelligencePlugin }))

	pms, err := e.s.ListPluginModels(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(pms, func(pm PluginModels) bool { return pm.Plugin == AppleIntelligencePlugin }))

	// When AppleIntelligence is configured
	fake := &fakeAppleIntelligence{ver: "15.1"}
	e.s.opts.AppleIntelligence = fake

	ps, err = e.s.ListPlugins(t.Context())
	require.NoError(t, err)
	aiIdx := slices.IndexFunc(ps, func(p PluginInfo) bool { return p.Name == AppleIntelligencePlugin })
	require.True(t, aiIdx >= 0)
	assert.True(t, ps[aiIdx].Builtin)
	assert.True(t, ps[aiIdx].Enabled)
	assert.Equal(t, "Apple Intelligence", ps[aiIdx].Title.Default)
	assert.Equal(t, "Apple Intelligence", ps[aiIdx].Title.ByLang["de"])

	ts, err = e.s.ListTranslators(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(ts, func(x TranslatorInfo) bool {
		return x.ID == "plugin/"+AppleIntelligencePlugin
	}))

	pms, err = e.s.ListPluginModels(t.Context())
	require.NoError(t, err)
	pmIdx := slices.IndexFunc(pms, func(pm PluginModels) bool { return pm.Plugin == AppleIntelligencePlugin })
	require.True(t, pmIdx >= 0)
	require.Len(t, pms[pmIdx].Models, 2)
	assert.Equal(t, "on-device", pms[pmIdx].Models[0].ID)
	assert.False(t, pms[pmIdx].Models[0].Judges())
	assert.Equal(t, "classifier", pms[pmIdx].Models[1].ID)
	assert.True(t, pms[pmIdx].Models[1].Judges())

	// Disable plugin
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), AppleIntelligencePlugin, false))

	ps, err = e.s.ListPlugins(t.Context())
	require.NoError(t, err)
	aiIdx = slices.IndexFunc(ps, func(p PluginInfo) bool { return p.Name == AppleIntelligencePlugin })
	require.True(t, aiIdx >= 0)
	assert.False(t, ps[aiIdx].Enabled)

	pms, err = e.s.ListPluginModels(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(pms, func(pm PluginModels) bool { return pm.Plugin == AppleIntelligencePlugin }))

	ts, err = e.s.ListTranslators(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(ts, func(x TranslatorInfo) bool { return x.Name == AppleIntelligencePlugin && x.Running }))
}

func TestAppleIntelligenceResolutionAndExecution(t *testing.T) {
	e := newSkillEnv(t)
	fake := &fakeAppleIntelligence{
		ver:     "15.1",
		genText: "Sehr geehrte Damen und Herren,\n\nvielen Dank.",
	}
	e.s.opts.AppleIntelligence = fake

	// 1. Generation model resolution
	m, ref, err := e.s.resolveRef(t.Context(), "plugin/"+AppleIntelligencePlugin+"/on-device")
	require.NoError(t, err)
	assert.Equal(t, "plugin/apple-intelligence/on-device", ref)
	assert.Equal(t, "on-device", m.Model)
	assert.Equal(t, ai.KindOpenAI, m.Kind)
	assert.NotNil(t, m.Client)

	// Second resolution must share the exact same Client pointer to prevent errStale
	m2, _, err := e.s.resolveRef(t.Context(), "plugin/"+AppleIntelligencePlugin+"/on-device")
	require.NoError(t, err)
	assert.Same(t, m.Client, m2.Client, "client must be cached to prevent errStale on concurrent runs")

	// Execute text generation through aiEngine
	engine, err := e.s.aiEngine()
	require.NoError(t, err)
	out, err := e.s.runText(t.Context(), engine, ai.Request{Model: m, Prompt: "Schreib eine Antwort", MaxTurns: 1}, 1000)
	require.NoError(t, err)
	assert.Equal(t, fake.genText, out)
	assert.Greater(t, fake.calls, 0)

	// 2. Judge model resolution
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.JudgeService = "plugin/" + AppleIntelligencePlugin + "/classifier"
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)

	judge, label, gen, err := e.s.resolveJudge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "plugin/apple-intelligence/classifier", label)
	assert.False(t, gen)
	require.NotNil(t, judge)

	answers, err := judge.Ask(t.Context(), "Important mail", []ai.Question{
		{Name: "important", Type: ai.Predicate, Instructions: "Is this important?"},
	})
	require.NoError(t, err)
	require.Len(t, answers, 1)
	assert.Equal(t, "important", answers[0].Name)
	assert.Equal(t, 0.95, *answers[0].Probability)

}

func TestDefaultAppleIntelligence(t *testing.T) {
	d := NewDefaultAppleIntelligence(filepath.Join("..", "resources", "darwin-"+runtime.GOARCH, "apple-intelligence-helper"), "test")
	assert.NotEmpty(t, d.Version())

	if !d.Available() {
		t.Skip("Apple Intelligence not available in this test environment")
	}

	// Real Generate via FoundationModels
	out, err := d.Generate(t.Context(), "Du bist ein Assistent. Antworte kurz.", "Hallo")
	require.NoError(t, err)
	assert.NotEmpty(t, out)

	// Real Judge via FoundationModels
	qs := []ai.Question{
		{Name: "work", Type: ai.Predicate, Instructions: "Ist diese Nachricht geschäftlich?"},
	}
	ans, err := d.Judge(t.Context(), "Hier ist der Quartalsbericht der Firma.", qs)
	require.NoError(t, err)
	require.Len(t, ans, 1)
}

func TestAppleIntelligenceSummarizeAdaptation(t *testing.T) {
	// German reading language
	germanSys := "Summarize the email text you are given.\n\n- Write 3 to 5 bullet points with the key points and any decisions or deadlines.\n- Do not suggest actions, buttons or next steps.\n- No preamble and no closing remarks.\n\nAnswer in de."
	adapted, temp := adaptAppleIntelligenceSummarize(germanSys)
	assert.Contains(t, adapted, "auf Deutsch zusammen")
	assert.Contains(t, adapted, "Keine negativen Feststellungen")
	assert.InDelta(t, 0.2, temp, 0.001)

	// English reading language
	engSys := "Summarize the email text you are given.\n\n- Write 3 to 5 bullet points...\n\nAnswer in en."
	adaptedEng, tempEng := adaptAppleIntelligenceSummarize(engSys)
	assert.Contains(t, adaptedEng, "factual bullet points in English")
	assert.Contains(t, adaptedEng, "Do not mention what is missing")
	assert.InDelta(t, 0.2, tempEng, 0.001)

	// Verify transport round-trip with temperature and summary cleanup
	mock := &mockAIWithTemp{out: "- Key point 1\n- Key point 2\nKeine explizite Nennung von Apple oder der Technologie."}
	transport := &appleIntelligenceTransport{
		s:  nil,
		ai: mock,
	}

	reqBody := map[string]any{
		"model":  "on-device",
		"stream": false,
		"messages": []any{
			map[string]string{"role": "system", "content": summarizeSystem("de")},
			map[string]string{"role": "user", "content": "English email content here..."},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	resp, err := transport.RoundTrip(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))
	assert.Equal(t, "- Key point 1\n- Key point 2", chatResp.Choices[0].Message.Content)
	assert.InDelta(t, 0.2, mock.recordedTemp, 0.001)

	// Verify quick replies adaptation and format packaging
	qrSys := `Suggest up to three short, distinct replies the user could send to the email you are given, written as the user. Answer with JSON only, exactly in this shape: {"replies":[{"html":"<p>...</p>"}]}. Use simple HTML (paragraphs, line breaks). No greeting placeholders, no signature, no quoted text.

` + ai.UntrustedSystemNote + `

Write the replies in de.`
	adaptedQR, tempQR := adaptAppleIntelligenceQuickReplies(qrSys)
	assert.Contains(t, adaptedQR, "Format your output as a simple list with one reply per line starting with a dash (-)")
	assert.Contains(t, adaptedQR, "Write the replies strictly in German.")
	assert.InDelta(t, 0.3, tempQR, 0.001)

	// Verify French ISO mapping
	adaptedFR, _ := adaptAppleIntelligenceQuickReplies("Suggest up to three short, distinct replies... Write the replies in fr.")
	assert.Contains(t, adaptedFR, "Write the replies strictly in French.")

	// Verify language of email preserves match
	adaptedMatch, _ := adaptAppleIntelligenceQuickReplies("Suggest up to three short, distinct replies... Write the replies in the language of the email.")
	assert.Contains(t, adaptedMatch, "Write the replies in the language of the email.")

	// Verify transport round-trip for quick replies
	mockQR := &mockAIWithTemp{out: "- Danke für den Guide!\n- Ich sehe mir das an.\n- Tolle Arbeit!"}
	transportQR := &appleIntelligenceTransport{
		s:  nil,
		ai: mockQR,
	}
	reqBodyQR := map[string]any{
		"model":  "on-device",
		"stream": false,
		"messages": []any{
			map[string]string{"role": "system", "content": qrSys},
			map[string]string{"role": "user", "content": "Email text"},
		},
	}
	bodyBytesQR, _ := json.Marshal(reqBodyQR)
	httpReqQR, err := http.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1/chat/completions", bytes.NewReader(bodyBytesQR))
	require.NoError(t, err)

	respQR, err := transportQR.RoundTrip(httpReqQR)
	require.NoError(t, err)
	defer respQR.Body.Close()

	var chatRespQR struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.NewDecoder(respQR.Body).Decode(&chatRespQR))

	// The assistant message must now be valid JSON matching parseQuickReplies!
	rs, err := parseQuickReplies(chatRespQR.Choices[0].Message.Content, func(h string) string { return h })
	require.NoError(t, err)
	require.Len(t, rs, 3)
	assert.Equal(t, "Danke für den Guide!", rs[0].Preview)
	assert.Equal(t, "<p>Danke für den Guide!</p>", rs[0].HTML)
	assert.Equal(t, "Ich sehe mir das an.", rs[1].Preview)
	assert.Equal(t, "Tolle Arbeit!", rs[2].Preview)
}

type mockAI struct {
	out string
}

func (m *mockAI) Generate(ctx context.Context, system, prompt string) (string, error) {
	return m.out, nil
}
func (m *mockAI) Judge(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	return nil, nil
}
func (m *mockAI) Version() string { return "mock-1.0" }

type mockAIWithTemp struct {
	out            string
	recordedTemp   float64
	recordedPrompt string
	recordedSystem string
}

func (m *mockAIWithTemp) Generate(ctx context.Context, system, prompt string) (string, error) {
	return m.out, nil
}
func (m *mockAIWithTemp) GenerateWithTemperature(ctx context.Context, system, prompt string, temp float64) (string, error) {
	m.recordedTemp, m.recordedPrompt, m.recordedSystem = temp, prompt, system
	return m.out, nil
}
func (m *mockAIWithTemp) Judge(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	return nil, nil
}
func (m *mockAIWithTemp) Version() string { return "mock-temp-1.0" }

func TestAppleIntelligenceReviewFixes(t *testing.T) {
	assert.Equal(t, []string{"10:00 works for me", "3 Uhr passt", "Sure, see you"},
		extractQuickReplies("- 10:00 works for me\n2. 3 Uhr passt\n* Sure, see you"))

	long := strings.Repeat("邮", 5000)
	cut := truncateForContext(long, appleIntelligencePromptBytes)
	assert.LessOrEqual(t, len(cut), appleIntelligencePromptBytes)
	assert.True(t, utf8.ValidString(cut))
	assert.Equal(t, "short", truncateForContext("short", appleIntelligencePromptBytes))

	assert.Equal(t, "- Erklärt die Arbeit der zugrundeliegenden Technologie",
		cleanAppleIntelligenceSummary("- Erklärt die Arbeit der zugrundeliegenden Technologie"))

	assert.Equal(t, "Traditional Chinese", languageName("zh-Hant"))
	assert.Equal(t, "Traditional Chinese", languageName("zh-Hant-TW"))
	assert.Equal(t, "Traditional Chinese", languageName("zh_HK"))
	assert.Equal(t, "Simplified Chinese", languageName("zh-Hans-HK"))
	assert.Equal(t, "Simplified Chinese", languageName("zh-Hans-TW"))
	assert.Equal(t, "Simplified Chinese", languageName("zh-CN"))
	assert.Equal(t, "German", languageName("de"))

	for _, lang := range []string{"de", "en"} {
		sys, _ := adaptAppleIntelligenceSummarize("Summarize the email text you are given.\n\nAnswer in " + lang + ".")
		assert.Contains(t, sys, ai.UntrustedSystemNote, lang)
	}

	e := newSkillEnv(t)
	e.s.opts.AppleIntelligence = &fakeAppleIntelligence{ver: "1"}
	_, _, err := e.s.resolveRef(t.Context(), "plugin/"+AppleIntelligencePlugin+"/typo")
	assert.ErrorIs(t, err, ErrModelNotFound)
	_, err = e.s.pluginJudge(t.Context(), "plugin/"+AppleIntelligencePlugin+"/on-device")
	assert.ErrorIs(t, err, ErrModelNotFound)

	// An OS or helper update changes the version, so cached outputs aren't reused.
	m1, _, err := e.s.resolveRef(t.Context(), "plugin/"+AppleIntelligencePlugin+"/on-device")
	require.NoError(t, err)
	e.s.opts.AppleIntelligence = &fakeAppleIntelligence{ver: "2"}
	m2, _, err := e.s.resolveRef(t.Context(), "plugin/"+AppleIntelligencePlugin+"/on-device")
	require.NoError(t, err)
	assert.NotEqual(t, m1.Version, m2.Version)
}

// A long classifier input keeps its questions: the helper sees every label name.
func TestAppleIntelligenceJudgeKeepsQuestions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script; the real one is macOS only")
	}
	dir := t.TempDir()
	got := filepath.Join(dir, "stdin")
	helper := filepath.Join(dir, "helper")
	require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\ncat > "+got+"\necho '{\"success\":true,\"text\":\"{}\"}'\n"), 0o755))
	d := NewDefaultAppleIntelligence(helper, "t")
	_, _ = d.Judge(t.Context(), strings.Repeat("标签", 4000), []ai.Question{{Name: "invoice_label", Type: ai.Predicate, Instructions: "Is this an invoice?"}})
	sent, err := os.ReadFile(got)
	require.NoError(t, err)
	var req struct{ Text, System string }
	require.NoError(t, json.Unmarshal(sent, &req))
	assert.LessOrEqual(t, len(req.Text), appleIntelligencePromptBytes)
	assert.Contains(t, req.System+req.Text, "invoice_label")
}

func TestAppleIntelligenceLongPrompts(t *testing.T) {
	ask := func(system, user string) (*http.Response, *mockAIWithTemp, error) {
		mock := &mockAIWithTemp{out: "- ok"}
		body, _ := json.Marshal(map[string]any{"model": "on-device", "messages": []any{
			map[string]string{"role": "system", "content": system},
			map[string]string{"role": "user", "content": user},
		}})
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1/chat/completions", bytes.NewReader(body))
		require.NoError(t, err)
		resp, err := (&appleIntelligenceTransport{ai: mock}).RoundTrip(req)
		return resp, mock, err
	}
	long := ai.Untrusted("message:1", "OLDEST "+strings.Repeat("邮", 5000), false) + "\n" +
		ai.Untrusted("message:2", "NEWEST reply", false)

	// A summary keeps the newest mail whole and drops the oldest.
	resp, mock, err := ask(summarizeSystem("en"), long)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Contains(t, mock.recordedPrompt, "NEWEST reply\n</untrusted_email>")
	assert.NotContains(t, mock.recordedPrompt, "OLDEST")
	assert.LessOrEqual(t, len(mock.recordedPrompt), appleIntelligencePromptBytes)

	// A structured request (a translation batch) is refused, not cut.
	_, _, err = ask("Translate the JSON array of texts.", long)
	assert.ErrorIs(t, err, ErrModelUnavailable)
}

func TestAppleIntelligenceJudgeBatches(t *testing.T) {
	desc := strings.Repeat("描述", 250) // a 500-rune label description
	var qs []ai.Question
	for i := range 200 {
		qs = append(qs, ai.Question{Name: "label_" + strconv.Itoa(i), Type: ai.Predicate, Instructions: desc})
	}
	n := 0
	for _, b := range judgeBatches(qs) {
		q, _ := json.Marshal(b)
		assert.GreaterOrEqual(t, appleIntelligencePromptBytes-len(q)-judgeFramingBytes, judgeMinInputBytes)
		assert.LessOrEqual(t, len(b), 128)
		n += len(b)
	}
	assert.Equal(t, len(qs), n)

	// A judge kept by a labeling batch stops once the plugin is off.
	e := newSkillEnv(t)
	e.s.opts.AppleIntelligence = &fakeAppleIntelligence{ver: "1"}
	j, err := e.s.pluginJudge(t.Context(), "plugin/"+AppleIntelligencePlugin+"/classifier")
	require.NoError(t, err)
	require.NoError(t, e.s.SetPluginEnabled(t.Context(), AppleIntelligencePlugin, false))
	_, err = j.Ask(t.Context(), "mail", []ai.Question{{Name: "x", Type: ai.Predicate, Instructions: "x?"}})
	assert.ErrorIs(t, err, ErrModelUnavailable)
}

// Apple Intelligence runs on this device: its judge is asked the values
// as they are, masking on.
func TestAppleIntelligenceJudgeIsNotMasked(t *testing.T) {
	e := newSkillEnv(t)
	fake := &fakeAppleIntelligence{ver: "1"}
	e.s.opts.AppleIntelligence = fake
	j, err := e.s.pluginJudge(t.Context(), "plugin/"+AppleIntelligencePlugin+"/classifier")
	require.NoError(t, err)
	m, err := e.s.masker()
	require.NoError(t, err)
	require.True(t, m.On())
	_, err = j.Ask(t.Context(), "Card 4111 1111 1111 1111", []ai.Question{{Name: "x", Type: ai.Predicate, Instructions: "x?"}})
	require.NoError(t, err)
	assert.Equal(t, "Card 4111 1111 1111 1111", fake.asked)
}

func TestAppleIntelligenceTruncateMailKeepsWrapper(t *testing.T) {
	// The newest block, too long alone, keeps its tag, header and body start.
	body := "Subject: Invoice\n\nPlease pay. " + strings.Repeat("邮", 4000) + " old quoted footer"
	prompt := ai.Untrusted("message:1", body, true)
	got := truncateMail(prompt, appleIntelligencePromptBytes)
	assert.LessOrEqual(t, len(got), appleIntelligencePromptBytes)
	assert.True(t, strings.HasPrefix(got, `<untrusted_email source="message:1" suspect="true">`+"\nSubject: Invoice\n\nPlease pay."))
	assert.Contains(t, got, "\n</untrusted_email>")
	assert.NotContains(t, got, "old quoted footer")
	assert.True(t, utf8.ValidString(got))

	// Any length, however the budget falls across the open tag, stays wrapped.
	for extra := range 40 {
		p := ai.Untrusted("message:1", strings.Repeat("a", appleIntelligencePromptBytes-50+extra), false)
		got := truncateMail(p, appleIntelligencePromptBytes)
		assert.True(t, strings.HasPrefix(got, `<untrusted_email source="message:1">`), extra)
		assert.Len(t, untrustedBlock.FindAllString(got, -1), 1, extra)
		assert.LessOrEqual(t, len(got), appleIntelligencePromptBytes, extra)
	}

	// Older blocks go whole; the newest stays whole when it fits.
	newest := ai.Untrusted("message:2", "newest", false)
	two := ai.Untrusted("message:1", strings.Repeat("a", 9000), false) + "\n" + newest
	assert.Equal(t, newest, truncateMail(two, len(newest)+10))
}

func TestAppleIntelligenceJudgeBatchOverhead(t *testing.T) {
	// Questions whose own encodings fill the limit only with the array's commas counted.
	q := ai.Question{Name: "l", Type: ai.Predicate}
	b, _ := json.Marshal(q)
	limit := appleIntelligencePromptBytes - judgeFramingBytes - judgeMinInputBytes
	q.Instructions = strings.Repeat("x", limit/9-len(b))
	qs := slices.Repeat([]ai.Question{q}, 9)
	for _, batch := range judgeBatches(qs) {
		enc, _ := json.Marshal(batch)
		assert.LessOrEqual(t, len(enc), limit)
	}
}

func TestAppleIntelligenceSystemBudget(t *testing.T) {
	ask := func(system string, n int) error {
		body, _ := json.Marshal(map[string]any{"model": "on-device", "messages": []any{
			map[string]string{"role": "system", "content": system},
			map[string]string{"role": "user", "content": strings.Repeat("x", n)},
		}})
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1/chat/completions", bytes.NewReader(body))
		require.NoError(t, err)
		resp, err := (&appleIntelligenceTransport{ai: &mockAIWithTemp{out: "ok"}}).RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	skill := "A user skill.\n" + strings.Repeat("rule ", 800) // a 4 KB SKILL.md body
	require.NoError(t, ask(skill, 5000))
	assert.ErrorIs(t, ask(skill, 6000), ErrModelUnavailable, "the skill body shrinks the prompt's share")
	assert.ErrorIs(t, ask(strings.Repeat("rule ", 1700), 10), ErrModelUnavailable, "instructions alone too long")
}

// Turning Apple Intelligence on while Godwit runs shows the plugin without a restart.
func TestAppleIntelligenceAvailabilityRechecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script; the real one is macOS only")
	}
	old := appleIntelligenceRecheck
	appleIntelligenceRecheck = 0
	t.Cleanup(func() { appleIntelligenceRecheck = old })

	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	helper := filepath.Join(dir, "helper")
	require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nif [ -e "+ready+" ]; then echo '{\"available\":true}'; else echo '{\"available\":false}'; fi\n"), 0o755))

	e := newSkillEnv(t)
	e.s.opts.AppleIntelligence = NewDefaultAppleIntelligence(helper, "t")
	listed := func() bool {
		pms, err := e.s.ListPluginModels(t.Context())
		require.NoError(t, err)
		return slices.ContainsFunc(pms, func(pm PluginModels) bool { return pm.Plugin == AppleIntelligencePlugin })
	}
	assert.False(t, listed())
	require.NoError(t, os.WriteFile(ready, nil, 0o644))
	assert.True(t, listed())
}

// summarizeSystem is the system prompt runSkill builds for the built-in summarize skill.
func summarizeSystem(lang string) string {
	return builtinSkillBody("summarize") + "\n\n" + ai.UntrustedSystemNote + "\n\nAnswer in " + lang + "."
}

// A user's own summarize skill keeps its instructions: only Godwit's prompt is rewritten.
func TestAppleIntelligenceUserSkillNotRewritten(t *testing.T) {
	ask := func(system string) *mockAIWithTemp {
		mock := &mockAIWithTemp{out: "- ok"}
		body, _ := json.Marshal(map[string]any{"model": "on-device", "messages": []any{
			map[string]string{"role": "system", "content": system},
			map[string]string{"role": "user", "content": "mail"},
		}})
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1/chat/completions", bytes.NewReader(body))
		require.NoError(t, err)
		resp, err := (&appleIntelligenceTransport{ai: mock}).RoundTrip(req)
		require.NoError(t, err)
		resp.Body.Close()
		return mock
	}
	custom := "Summarize the email text you are given.\n\nAlways end with a line TL;DR.\n\n" + ai.UntrustedSystemNote + "\n\nAnswer in en."
	assert.Equal(t, custom, ask(custom).recordedSystem)
	assert.Contains(t, ask(summarizeSystem("en")).recordedSystem, "factual bullet points in English")

	// A skill that copies the quick-reply prompt and adds rules is not Quick Replies.
	qr := quickRepliesSystem + "\n\n" + ai.UntrustedSystemNote + "\n\n"
	customQR := quickRepliesSystem + " Also add a P.S.\n\n" + ai.UntrustedSystemNote + "\n\nAnswer in en."
	assert.Equal(t, customQR, ask(customQR).recordedSystem)
	assert.False(t, isBuiltinQuickReplies(qr+"Write the replies in de. And sign as Bob."))
	assert.True(t, isBuiltinQuickReplies(qr+"Write the replies in zh-Hant-TW."))
	assert.True(t, isBuiltinQuickReplies(qr+"Write the replies in the language of the email."))
	assert.True(t, isBuiltinSummarize(summarizeSystem("pt-BR")))
	assert.False(t, isBuiltinSummarize(summarizeSystem("en")+" Then add a haiku."))
}

// Batches are sized by the wire format NewGenJudge sends, score levels' index included.
func TestAppleIntelligenceJudgeBatchWireFormat(t *testing.T) {
	levels := make([]ai.Level, 10)
	for i := range levels {
		levels[i] = ai.Level{Label: "level " + strconv.Itoa(i), Description: "how urgent, step " + strconv.Itoa(i)}
	}
	var qs []ai.Question
	for i := range 21 {
		qs = append(qs, ai.Question{Name: "score_" + strconv.Itoa(i), Type: ai.Score, Instructions: "How urgent is it?", Levels: levels})
	}
	limit := appleIntelligencePromptBytes - judgeFramingBytes - judgeMinInputBytes
	for _, b := range judgeBatches(qs) {
		enc, err := ai.GenJudgeQuestions(b)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(enc), limit)
	}
}

// A fenced JSON answer, which parseQuickReplies accepts, is kept as the replies.
func TestAppleIntelligenceFencedQuickReplies(t *testing.T) {
	fenced := "```json\n{\"replies\":[{\"html\":\"<p>Sounds good</p>\"}]}\n```"
	rs, err := parseQuickReplies(formatQuickRepliesJSON(fenced), func(h string) string { return h })
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, "<p>Sounds good</p>", rs[0].HTML)

	// A fenced empty array means no suggestions, not fence lines as replies.
	rs, err = parseQuickReplies(formatQuickRepliesJSON("```json\n{\"replies\":[]}\n```"), func(h string) string { return h })
	require.NoError(t, err)
	assert.Empty(t, rs)
}

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

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
	d := NewDefaultAppleIntelligence(filepath.Join("..", "resources", "darwin-"+runtime.GOARCH, "apple-intelligence-helper"))
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
			map[string]string{"role": "system", "content": germanSys},
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
	rs, err := parseQuickReplies(chatRespQR.Choices[0].Message.Content)
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
	out          string
	recordedTemp float64
}

func (m *mockAIWithTemp) Generate(ctx context.Context, system, prompt string) (string, error) {
	return m.out, nil
}
func (m *mockAIWithTemp) GenerateWithTemperature(ctx context.Context, system, prompt string, temp float64) (string, error) {
	m.recordedTemp = temp
	return m.out, nil
}
func (m *mockAIWithTemp) Judge(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	return nil, nil
}
func (m *mockAIWithTemp) Version() string { return "mock-temp-1.0" }

func TestAppleIntelligenceReviewFixes(t *testing.T) {
	assert.Equal(t, []string{"10:00 works for me", "3 Uhr passt", "Sure, see you"},
		extractQuickReplies("- 10:00 works for me\n2. 3 Uhr passt\n* Sure, see you"))

	assert.Equal(t, "Traditional Chinese", languageName("zh-Hant"))
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
}

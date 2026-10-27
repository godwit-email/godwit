package core

import (
	"context"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBodyTranslationUsesTextNodes(t *testing.T) {
	e := newSkillEnv(t)
	_, err := e.s.db.Execute(t.Context(), `UPDATE messages SET body_html = ? WHERE id = ?`,
		`<table><tr><td><img src="https://images.test/logo.png"><a href="https://example.com">Hello <strong>world</strong></a></td></tr></table><code>go test ./...</code><pre>export TOKEN=example</pre>`, e.msg)
	require.NoError(t, err)
	e.srv.Script(testllm.Turn{Text: []string{`{"texts":["你好","世界"],"source":"en"}`}})
	tg := Target{Kind: "message", ID: e.msg, PreserveHTML: true}
	evs := e.run(t, "translate", tg)
	require.Equal(t, "done", evs[len(evs)-1].Kind)
	require.Len(t, evs, 3)
	assert.Equal(t, map[string]string{"Hello ": "你好 ", "world": "世界"}, evs[1].Translations)
	prompt := promptOf(t, e.srv.Requests()[0])
	assert.Contains(t, prompt, `"Hello"`)
	assert.NotContains(t, prompt, "https://images.test")
	assert.NotContains(t, prompt, "Subject:")
	assert.NotContains(t, prompt, "go test")
	assert.NotContains(t, prompt, "TOKEN")
	assert.Equal(t, evs, e.run(t, "translate", tg), "layout translations have their own cache")
	assert.Len(t, e.srv.Requests(), 1)
}

func TestBodyTranslationBatchesWithoutTruncation(t *testing.T) {
	original := "  " + strings.Repeat("Hello 世界\n", 6000) + "  "
	calls := 0
	result, err := translateBodyTexts(t.Context(), []string{original}, "zh-CN", nil,
		func(_ context.Context, req translateRequest) (translateResult, error) {
			calls++
			return translateResult{Texts: req.Texts, Source: "en"}, nil
		})
	require.NoError(t, err)
	assert.Greater(t, calls, 1)
	assert.Equal(t, strings.TrimSpace(original), result.Text)
	assert.Empty(t, result.Translations, "unchanged text keeps the original whitespace")
}

func TestBodyTranslationCanceledAfterReply(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := translateBodyTexts(ctx, []string{"Hello"}, "zh-CN", nil,
		func(_ context.Context, req translateRequest) (translateResult, error) {
			cancel()
			return translateResult{Texts: []string{"你好"}}, nil
		})
	assert.ErrorIs(t, err, context.Canceled)
}

// A translator's detected language needs no translation only in the target's
// script: Traditional Chinese mail is translated for a Simplified reader.
func TestBodyTranslationKeepsOnlySameScript(t *testing.T) {
	for _, tc := range []struct {
		source, target string
		keep           []string
		translated     bool
	}{
		{"zh-Hant", "zh-Hans", nil, true},
		{"zh-TW", "zh-Hans", nil, true},
		{"zh", "zh-Hans", nil, false},
		{"zh-Hans", "zh-Hant", nil, true},
		{"zh-Hant", "en", []string{"zh-Hans"}, true},
		{"zh-Hant", "en", []string{"zh-Hant"}, false},
		{"en", "en-US", nil, false},
		{"EN", "fr", []string{"en"}, false},
		{"ja", "zh-Hans", nil, true},
	} {
		result, err := translateBodyTexts(t.Context(), []string{"原文"}, tc.target, tc.keep,
			func(_ context.Context, req translateRequest) (translateResult, error) {
				return translateResult{Texts: []string{"译文"}, Source: tc.source}, nil
			})
		require.NoError(t, err)
		assert.Equal(t, tc.translated, result.Text == "译文", "%s → %s, keep %v", tc.source, tc.target, tc.keep)
	}
}

func TestBodyTranslationRejectsIncompleteAnswer(t *testing.T) {
	for _, answer := range []string{`{"texts":[],"source":"en"}`, `{"texts":[""],"source":"en"}`, `<untrusted_email>hello</untrusted_email>`} {
		t.Run(answer, func(t *testing.T) {
			e := newSkillEnv(t)
			e.srv.Script(testllm.Turn{Text: []string{answer}})
			evs := e.run(t, "translate", Target{Kind: "message", ID: e.msg, PreserveHTML: true})
			assert.Equal(t, "error", evs[len(evs)-1].Kind)
			assert.Empty(t, texts(evs), "a partial result must not replace the body")
			assert.Zero(t, e.cacheRows(t))
		})
	}
}

// A value with a line break or a quote restores after the model's JSON
// is parsed: restored before, it would break the JSON.
func TestBodyTranslationRestoresAfterParsing(t *testing.T) {
	const pem = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
	e := newSkillEnv(t)
	setBody(t, e, "Key "+pem, "")
	mk, err := e.s.masker()
	require.NoError(t, err)
	p, err := mk.Text(pem)
	require.NoError(t, err)
	e.srv.Script(testllm.Turn{Text: []string{`{"texts":["Clé ` + p + `"],"source":"en"}`}})
	evs := e.run(t, "translate", Target{Kind: "message", ID: e.msg, PreserveHTML: true})
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
	assert.Equal(t, map[string]string{"Key " + pem: "Clé " + pem}, evs[1].Translations)
}

// A value a long text is cut inside goes to the model whole, masked: the
// texts are masked before they are cut.
func TestBodyTranslationMasksAcrossACut(t *testing.T) {
	const card = "4111 1111 1111 1111"
	e := newSkillEnv(t)
	text := strings.Repeat("a", 8<<10-10) + " " + card + " b" // splitText cuts at the last space before the limit: inside the card
	setBody(t, e, text, "")
	e.srv.Script(testllm.Turn{Text: []string{`{"texts":["x","y"],"source":"en"}`}})
	evs := e.run(t, "translate", Target{Kind: "message", ID: e.msg, PreserveHTML: true})
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
	body := string(e.srv.Requests()[0].Body)
	assert.True(t, strings.Contains(body, "{{BANK_CARD_"), "the card is masked")
	assert.False(t, strings.Contains(body, "1111"), "no piece of the card is sent")
}

// A secret found by its label stays masked when the text is cut between
// the label and the value.
func TestBodyTranslationMasksASecretCutFromItsLabel(t *testing.T) {
	for label, value := range map[string]string{"password": "abcd1234efgh5678", "passport": "E12345678"} {
		t.Run(label, func(t *testing.T) {
			e := newSkillEnv(t)
			text := strings.Repeat("a", 8179) + " " + label + ": " + value
			setBody(t, e, text, "")
			e.srv.Script(testllm.Turn{Text: []string{`{"texts":["x","y"],"source":"en"}`}})
			evs := e.run(t, "translate", Target{Kind: "message", ID: e.msg, PreserveHTML: true})
			require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
			for _, r := range e.srv.Requests() {
				assert.NotContains(t, string(r.Body), value)
			}
		})
	}
}

// A text needing no translation comes back as it was, its values too, not
// as it was masked.
func TestBodyTranslationKeepsTheValuesOfAnUntranslatedText(t *testing.T) {
	const text = "卡号 4111 1111 1111 1111"
	res, err := translateMasked(t.Context(), []string{text}, 1, "zh-CN", nil, mask.New([]byte("k")),
		func(_ context.Context, req translateRequest) (translateResult, error) {
			return translateResult{Texts: []string{"x"}, Source: "zh-CN"}, nil
		})
	require.NoError(t, err)
	assert.Equal(t, text, res.Text)
}

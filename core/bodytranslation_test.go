package core

import (
	"context"
	"strings"
	"testing"

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

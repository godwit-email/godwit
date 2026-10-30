package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// complete masks what it sends, records it for its feature, and cuts the
// text; a raw reply keeps its placeholders for the caller to restore, a
// plain one comes restored.
func TestCompleteMasksTagsAndRestores(t *testing.T) {
	const card = "4111 1111 1111 1111"
	e := newSkillEnv(t)
	m, err := e.s.masker()
	require.NoError(t, err)
	p, err := m.Text(card)
	require.NoError(t, err)
	model, err := e.s.resolveModel(t.Context(), "")
	require.NoError(t, err)

	e.srv.Script(testllm.Turn{Text: []string{`{"v":"`, p, `"}`}})
	r, err := e.s.complete(t.Context(), featureReplies, ai.Request{Model: model, Prompt: "Is " + card + " mine?", Raw: true}, 0)
	require.NoError(t, err)
	assert.Equal(t, `{"v":"`+p+`"}`, r.Text, "raw: as the model wrote it")
	assert.Equal(t, card, r.Restore(p))
	assert.Equal(t, "4111 &amp; 1111", r.RestoreHTML("4111 &amp; 1111"))
	body := string(e.srv.Requests()[0].Body)
	assert.NotContains(t, body, card)
	assert.Contains(t, body, p)
	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, log)
	assert.Equal(t, featureReplies, log[0].Feature)

	e.srv.Script(testllm.Turn{Text: []string{"It is ", p, "."}})
	r, err = e.s.complete(t.Context(), featureSkill, ai.Request{Model: model, Prompt: "Is " + card + " mine?"}, 0)
	require.NoError(t, err)
	assert.Equal(t, "It is "+card+".", r.Text, "plain: restored")

	e.srv.Script(testllm.Turn{Text: []string{"abc", "def"}})
	r, err = e.s.complete(t.Context(), featureSkill, ai.Request{Model: model, Prompt: "hi"}, 4)
	require.NoError(t, err)
	assert.Equal(t, "abcd", r.Text, "cut at max bytes")
}

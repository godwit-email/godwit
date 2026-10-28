package core

import (
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A message's gist is one clean line, cached by the message and the model:
// read again, it asks the model nothing.
func TestMessageGist(t *testing.T) {
	e := newSkillEnv(t)
	e.srv.Script(testllm.Turn{Text: []string{"\n“提议周五在港口见面。”\n\nand more"}})
	g, err := e.s.MessageGist(t.Context(), e.msg)
	require.NoError(t, err)
	assert.Equal(t, "提议周五在港口见面", g)
	require.Len(t, e.srv.Requests(), 1)

	again, err := e.s.MessageGist(t.Context(), e.msg)
	require.NoError(t, err)
	assert.Equal(t, g, again)
	assert.Len(t, e.srv.Requests(), 1)

	assert.Equal(t, strings.Repeat("é", gistRunes-1)+"…", gistLine(strings.Repeat("é", 100)))
	assert.Equal(t, "Asks to move the call", gistLine("  Asks  to move\tthe call.  "))
}

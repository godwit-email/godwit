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

// A gist that repeats a link of its message shows the link, not the
// reference the model saw it by.
func TestMessageGistExpandsLinks(t *testing.T) {
	e := newSkillEnv(t)
	url := "https://example.com/very/long/path/to/the/meeting/notes?ref=newsletter&id=1234567890"
	id := addMessage(t, e.s, "Notes", "Bob <bob@x>", "Notes at "+url, "", 1_700_000_200)
	ref := linkRefs(url, id)
	require.NotEqual(t, url, ref, "the message's link reaches the model as a reference")
	e.srv.Script(testllm.Turn{Text: []string{"笔记在 " + ref}})
	g, err := e.s.MessageGist(t.Context(), id)
	require.NoError(t, err)
	assert.NotContains(t, g, linkRefPrefix)
	assert.True(t, strings.HasPrefix(g, "笔记在 https://example.com/very/"), g) // cut to the gist's length after
}

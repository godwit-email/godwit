package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noteState is a history whose one user turn carries context before its question.
func noteState(t *testing.T, context string) []byte {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{"role": "user", "content": []map[string]any{
		{"text": context, "metadata": map[string]any{"godwit_context": true}}, {"text": "q"}}}})
	require.NoError(t, err)
	return b
}

func TestTimeNote(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 21, 22, 20, 0, 0, zone)
	note := func(at time.Time) string { s, err := timeNote(nil, at); require.NoError(t, err); return s }

	first := note(now)
	assert.True(t, strings.HasPrefix(first, timeNotePrefix), first)
	assert.Contains(t, first, "Wednesday")

	cases := []struct {
		name  string
		state []byte
		at    time.Time
		fresh bool
	}{
		{"recent note holds", noteState(t, note(now)), now.Add(10 * time.Minute), false},
		{"old note", noteState(t, note(now)), now.Add(timeNoteGap), true},
		{"next day", noteState(t, note(now.Add(90*time.Minute))), now.Add(105 * time.Minute), true},
		{"after quoted mail", noteState(t, note(now)+"\n\nThe user quoted these emails"), now.Add(time.Minute), false},
		{"offset changed", noteState(t, note(now)), now.Add(10 * time.Minute).In(time.FixedZone("JST", 9*3600)), true},
		{"clock set back", noteState(t, note(now)), now.Add(-time.Hour), true},
		{"quote only", noteState(t, "The user quoted these emails"), now, true},
	}
	for _, c := range cases {
		got, err := timeNote(c.state, c.at)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.fresh, got != "", c.name)
	}
}

// TestAskTimeNote: the first question carries the time, hidden from the
// conversation; a question right after it does not repeat it.
func TestAskTimeNote(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Text: []string{"Hi."}}, testllm.Turn{Text: []string{"Again."}})
	e.ask(t, conv.ID, "first")
	e.ask(t, conv.ID, "second")

	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[0].System), "Mail dates are in UTC")
	assert.Equal(t, 1, strings.Count(promptOf(t, reqs[0]), timeNotePrefix))
	assert.Equal(t, 1, strings.Count(promptOf(t, reqs[1]), timeNotePrefix), "the first note still holds")

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "first", c.Messages[0].Text, "the note never shows")
}

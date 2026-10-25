package core

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactPromptFitsTheBudget(t *testing.T) {
	var rows []ai.StoredMessage
	for i, text := range []string{"q1", "A1", "q2", "A2", "q3", "A3", "q4", "A4"} {
		role := "user"
		if i%2 == 1 {
			role, text = "model", text+strings.Repeat("a", 1000)
		}
		b, err := json.Marshal(map[string]any{"role": role, "content": []map[string]string{{"text": text}}})
		require.NoError(t, err)
		rows = append(rows, ai.StoredMessage{Seq: int64(i + 1), Data: b})
	}
	genkit, err := ai.LoadHistory(rows, 9)
	require.NoError(t, err)
	pi, _, err := genkit.PiHistory()
	require.NoError(t, err)
	for _, h := range []*ai.History{genkit, pi} {
		testCompactPrompt(t, h)
	}
}

func testCompactPrompt(t *testing.T, h *ai.History) {
	upto, p, err := compactPrompt(h, ai.Summary{}, 1500, 1<<30)
	require.NoError(t, err)
	assert.EqualValues(t, 2, upto, "only q1's turn fits")
	assert.Contains(t, p, "A1")
	assert.NotContains(t, p, "A2")

	upto, p, err = compactPrompt(h, ai.Summary{Text: "s", Upto: 2}, 1500, 1<<30)
	require.NoError(t, err)
	assert.EqualValues(t, 4, upto, "the next stretch, through CompactPoint")
	assert.Contains(t, p, "A2")

	upto, _, err = compactPrompt(h, ai.Summary{Text: "s", Upto: 4}, 1500, 1<<30)
	require.NoError(t, err)
	assert.Zero(t, upto, "nothing left before the last two turns")

	upto, p, err = compactPrompt(h, ai.Summary{}, 100, 1<<30)
	require.NoError(t, err)
	assert.EqualValues(t, 2, upto, "one turn past the budget still moves on")
	assert.LessOrEqual(t, utf8.RuneCountInString(p), 106, "cut to fit")
}

// Seqs are not reused: a turn asked again right after the summary leaves a
// gap with no message in it, and no whole turns before the split one.
func TestTurnsBetween(t *testing.T) {
	var rows []ai.StoredMessage
	for _, m := range []struct {
		seq        int64
		role, text string
	}{{1, "user", "q1"}, {2, "model", "A1"}, {3, "user", "q2"}, {4, "model", "A2"}, {11, "user", "q3 again"}, {12, "model", "A3"}} {
		b, err := json.Marshal(map[string]any{"role": m.role, "content": []map[string]string{{"text": m.text}}})
		require.NoError(t, err)
		rows = append(rows, ai.StoredMessage{Seq: m.seq, Data: b})
	}
	h, err := ai.LoadHistory(rows, 13)
	require.NoError(t, err)
	assert.False(t, turnsBetween(h, 4, 11), "the gap 5 to 10 holds nothing")
	assert.True(t, turnsBetween(h, 2, 11), "q2's turn")
	assert.False(t, turnsBetween(h, 1, 2))
}

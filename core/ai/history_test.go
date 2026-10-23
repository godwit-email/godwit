package ai

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	gai "github.com/firebase/genkit/go/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// turn is a user question answered after one tool call, with an answer of n runes.
func turn(q, id string, n int) []*gai.Message {
	req := gai.NewToolRequestPart(&gai.ToolRequest{Name: "search", Ref: id, Input: map[string]any{"q": q}})
	return []*gai.Message{
		gai.NewUserTextMessage(q),
		gai.NewModelMessage(req),
		{Role: gai.RoleTool, Content: []*gai.Part{gai.NewToolResponsePart(&gai.ToolResponse{Name: "search", Ref: id, Output: "found"})}},
		gai.NewModelTextMessage(q + "-answer:" + strings.Repeat("a", n)),
	}
}

func stateOf(t *testing.T, turns ...[]*gai.Message) []byte {
	t.Helper()
	var all []*gai.Message
	for _, tr := range turns {
		all = append(all, tr...)
	}
	b, err := json.Marshal(all)
	require.NoError(t, err)
	return b
}

func TestTrimState(t *testing.T) {
	full := stateOf(t, turn("q1", "c1", 1000), turn("q2", "c2", 1000), turn("q3", "c3", 1000), turn("q4", "c4", 1000))

	same, dropped, err := TrimState(full, 100000, Summary{})
	require.NoError(t, err)
	assert.Zero(t, dropped, "under budget: nothing dropped")
	assert.Equal(t, full, same)

	trimmed, dropped, err := TrimState(full, 3000, Summary{})
	require.NoError(t, err)
	assert.Equal(t, 7, dropped, "q1's answer and all of q2's turn")
	msgs, err := Messages(trimmed)
	require.NoError(t, err)
	require.Len(t, msgs, 10)
	assert.Equal(t, "q1", msgs[0].Text, "the first user message is kept")
	assert.Equal(t, "model", msgs[1].Role)
	assert.Equal(t, HistoryNote, msgs[1].Text)
	assert.Equal(t, "q3", msgs[2].Text)
	assert.Equal(t, "q4", msgs[6].Text)
	calls, results := 0, 0
	for _, m := range msgs {
		calls += len(m.Calls)
		results += len(m.Results)
	}
	assert.Equal(t, 2, calls)
	assert.Equal(t, calls, results, "tool requests and results are dropped in pairs")

	tiny, dropped, err := TrimState(full, 10, Summary{})
	require.NoError(t, err)
	assert.Equal(t, 7, dropped, "the last two turns stay even over budget")
	assert.Equal(t, trimmed, tiny)

	// The run's result is the trimmed state plus what it added; restoring
	// puts the dropped messages back in front of it.
	var res []*gai.Message
	require.NoError(t, json.Unmarshal(trimmed, &res))
	res = append(res, gai.NewUserTextMessage("q5"), gai.NewModelTextMessage("a5"))
	out, err := json.Marshal(res)
	require.NoError(t, err)
	restored, err := RestoreState(full, out, dropped)
	require.NoError(t, err)
	want, err := json.Marshal(append(mustDecode(t, full), gai.NewUserTextMessage("q5"), gai.NewModelTextMessage("a5")))
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(restored))

	two := stateOf(t, turn("q1", "c1", 5000), turn("q2", "c2", 5000))
	kept, dropped, err := TrimState(two, 10, Summary{})
	require.NoError(t, err)
	assert.Zero(t, dropped, "two turns: nothing can go")
	assert.Equal(t, two, kept)
}

func mustDecode(t *testing.T, state []byte) []*gai.Message {
	t.Helper()
	m, err := decodeState(state)
	require.NoError(t, err)
	return m
}

func TestTrimStateSummary(t *testing.T) {
	full := stateOf(t, turn("q1", "c1", 1000), turn("q2", "c2", 1000), turn("q3", "c3", 1000), turn("q4", "c4", 1000))
	upto, err := CompactPoint(full)
	require.NoError(t, err)
	assert.Equal(t, 7, upto, "everything before the last two turns but the first message")
	sum := Summary{Text: "they asked q1 and q2", Upto: upto}

	sent, dropped, err := TrimState(full, 100000, sum)
	require.NoError(t, err)
	assert.Equal(t, 7, dropped, "a summary stands in even under budget")
	msgs, err := Messages(sent)
	require.NoError(t, err)
	assert.Equal(t, "q1", msgs[0].Text)
	assert.Equal(t, SummaryNote+"\n\nthey asked q1 and q2", msgs[1].Text)
	assert.Equal(t, "q3", msgs[2].Text)
	size, err := CompactedSize(full, sum)
	require.NoError(t, err)
	assert.Equal(t, utf8.RuneCount(sent), size)

	more := stateOf(t, turn("q1", "c1", 1000), turn("q2", "c2", 1000), turn("q3", "c3", 1000), turn("q4", "c4", 1000), turn("q5", "c5", 1000))
	sent, dropped, err = TrimState(more, 3000, sum)
	require.NoError(t, err)
	assert.Equal(t, 11, dropped, "over budget, q3's turn goes after the summary")
	msgs, err = Messages(sent)
	require.NoError(t, err)
	assert.Equal(t, SummaryNote+"\n\nthey asked q1 and q2\n\n"+SummaryGapNote, msgs[1].Text)
	assert.Equal(t, "q4", msgs[2].Text)

	stale := Summary{Text: "x", Upto: 5} // a rewind left it inside a turn
	same, dropped, err := TrimState(full, 100000, stale)
	require.NoError(t, err)
	assert.Zero(t, dropped, "a summary not ending before a user message stands for nothing")
	assert.Equal(t, full, same)

	tr, err := Transcript(full, 1, 3)
	require.NoError(t, err)
	assert.Contains(t, tr, `Assistant called search with {"q":"q1"}`)
	assert.Contains(t, tr, "search returned: \"found\"")
	assert.Contains(t, tr, "Assistant: q1-answer:")
	assert.NotContains(t, tr, "User: q1", "from the second message")
}

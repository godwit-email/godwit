package ai

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// genkitHistory is a conversation saved before #234: a question with
// context, a reply with Anthropic reasoning, and a command after it.
func genkitHistory(t *testing.T) *History {
	t.Helper()
	h, _, _, err := LegacyHistory([]byte(`[{"role":"user","content":[{"text":"quoted","metadata":{"godwit_context":true}},{"text":"first"}]},
		{"role":"model","content":[{"reasoning":"hmm","metadata":{"signature":"c2lnLTE="}},{"text":"hi"}]}]`),
		[]PlacedCommand{{At: 2, Command: Command{Skill: "translate-draft", Lang: "fr"}}})
	require.NoError(t, err)
	require.False(t, h.Pi())
	return h
}

// An old conversation with tool calls (two results in one Genkit message), an
// interrupt, context, reasoning and a command converts to pi messages, one
// row each, and goes on: the resume runs no call again, the next question
// goes on pi with the whole history.
func TestPiHistoryConvertsAGenkitConversation(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "r1", Name: "read"}, {ID: "r2", Name: "read"}}},
		testllm.Turn{Calls: []testllm.Call{{ID: "r3", Name: "read"}, {ID: "a1", Name: "write", Args: `{"n":1}`}}},
		testllm.Turn{Text: []string{"done"}}, testllm.Turn{Text: []string{"more"}})
	var readRuns atomic.Int64
	sent := asking(genkitHistory(t), "go")
	evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: mixedTools(&readRuns)})
	require.Nil(t, res.Err)
	require.Equal(t, "interrupted", last(t, evs).Kind)
	h := after(t, sent, res)
	require.False(t, h.Pi())
	require.Equal(t, int64(8), h.Next(), "question, reply, command, question, calls, results, calls")

	p, seqOf, err := h.PiHistory()
	require.NoError(t, err)
	assert.True(t, p.Pi())
	assert.Equal(t, int64(9), p.Next(), "the two results take a row each")
	for seq, want := range map[int64]int64{1: 1, 3: 3, 6: 6, 7: 8, 8: 9} {
		assert.Equal(t, want, seqOf(seq), seq)
	}
	rows := must(p.Rows())
	for _, r := range rows {
		assert.True(t, IsPiMessage(r.Data), string(r.Data))
	}
	assert.Contains(t, string(rows[1].Data), `"thinkingSignature":"sig-1"`)
	assert.Contains(t, string(rows[2].Data), `"godwitCommand":{"skill":"translate-draft","lang":"fr"}`)
	stored := must(LoadHistory(rows, p.Next()))
	assert.Equal(t, display(h.msgs)[:5], display(stored.msgs)[:5])
	assert.Equal(t, []string{"quoted"}, must(stored.Send(1<<20, Summary{}, false)).Contexts())
	require.Len(t, stored.Turns(), 3)
	assert.Equal(t, []ToolResult{{CallID: "a1", Name: "write"}}, stored.OpenCalls())

	sent = resuming(stored)
	evs, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: mixedTools(&readRuns),
		Responses: []ToolResult{{CallID: "a1", Name: "write", Output: json.RawMessage(`{"draft":7}`)}}})
	require.Nil(t, res.Err)
	assert.Equal(t, "done", last(t, evs).Kind)
	assert.EqualValues(t, 3, readRuns.Load(), "no read runs again")

	h = after(t, sent, res)
	sent = asking(must(LoadHistory(must(h.Rows()), h.Next())), "and?")
	_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	reqs := srv.Requests()
	msgs := string(reqs[len(reqs)-1].Messages)
	for _, s := range []string{"quoted", "first", "hi", "r1", "r2", "r3", "read-result", `\"draft\":7`, "done"} {
		assert.Contains(t, msgs, s)
	}
	assert.NotContains(t, msgs, "translate-draft", "a command is never sent")
}

// A Responses reasoning item Genkit kept goes back, encrypted content and
// ID, once the conversation is converted and goes on on pi.
func TestPiHistoryKeepsResponsesReasoning(t *testing.T) {
	srv := testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Reasoning: "enc-1", Think: []string{"thinking"}, Text: []string{"one"}}, testllm.Turn{Text: []string{"two"}})
	old, _, _, err := LegacyHistory([]byte(`[{"role":"user","content":[{"text":"zero"}]},{"role":"model","content":[{"text":"0"}]}]`), nil)
	require.NoError(t, err)
	sent := asking(old, "first")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Effort: "low"})
	require.Nil(t, res.Err)
	h := after(t, sent, res)
	require.False(t, h.Pi())

	p, _, err := h.PiHistory()
	require.NoError(t, err)
	sent = asking(must(LoadHistory(must(p.Rows()), p.Next())), "second")
	_, res = run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Effort: "low", Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	reqs := srv.Requests()
	assert.Empty(t, reqs[1].Invalid)
	assert.Contains(t, string(reqs[1].Messages), "enc-1")
	assert.Contains(t, string(reqs[1].Messages), "rs_")
}

// An Anthropic signature Genkit kept goes back once converted; a signature
// of another kind does not (pi-ai drops that reasoning, as before).
func TestPiHistoryKeepsAnthropicSignatures(t *testing.T) {
	srv := testllm.Start(t, KindAnthropic)
	srv.Script(testllm.Turn{Text: []string{"two"}}, testllm.Turn{Text: []string{"three"}})
	p, _, err := genkitHistory(t).PiHistory()
	require.NoError(t, err)
	sent := asking(p, "second")
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindAnthropic, "k"), History: sent, Pi: true})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	assert.Contains(t, string(srv.Requests()[0].Messages), `"signature":"sig-1"`)

	srv = testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Text: []string{"two"}})
	_, res = run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: asking(p, "second"), Pi: true})
	require.Nil(t, res.Err)
	assert.Empty(t, srv.Requests()[0].Invalid)
	assert.NotContains(t, string(srv.Requests()[0].Messages), "sig-1")
}

// A message whose pi form would lose something fails the conversion.
func TestPiHistoryRefusesWhatItWouldLose(t *testing.T) {
	h, _, _, err := LegacyHistory([]byte(`[{"role":"user","content":[{"text":"q"},{"media":{"url":"data:image/png;base64,AA=="}}]}]`), nil)
	require.NoError(t, err)
	_, _, err = h.PiHistory()
	assert.Error(t, err)
}

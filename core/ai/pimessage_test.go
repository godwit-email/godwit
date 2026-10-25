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
	require.False(t, h.pi)
	return h
}

// genkitInterrupted is a conversation a run on Genkit left interrupted, as it
// saved it (recorded before Genkit was removed, #236): a question with
// context, a reply with Anthropic reasoning, a command, then a question whose
// run read twice (two results in one Genkit message) and was interrupted by
// a write, the read beside it already run.
func genkitInterrupted(t *testing.T) *History {
	t.Helper()
	rows := []string{
		`{"content":[{"metadata":{"godwit_context":true},"text":"quoted"},{"text":"first"}],"role":"user"}`,
		`{"content":[{"metadata":{"signature":"c2lnLTE="},"reasoning":"hmm"},{"text":"hi"}],"role":"model"}`,
		`{"content":[{"metadata":{"godwit_command":{"lang":"fr","skill":"translate-draft"}},"text":"/translate-draft"}],"role":"user"}`,
		`{"content":[{"text":"go"}],"role":"user"}`,
		`{"content":[{"toolRequest":{"input":{},"name":"read","ref":"r1"}},{"toolRequest":{"input":{},"name":"read","ref":"r2"}}],"role":"model"}`,
		`{"content":[{"toolResponse":{"name":"read","output":"read-result","ref":"r1"}},{"toolResponse":{"name":"read","output":"read-result","ref":"r2"}}],"role":"tool"}`,
		`{"content":[{"metadata":{"pendingOutput":"read-result"},"toolRequest":{"input":{},"name":"read","ref":"r3"}},` +
			`{"metadata":{"interrupt":true,"releasedBy":["godwit_call_ids"]},"toolRequest":{"input":{"n":1},"name":"write","ref":"a1"}}],"role":"model"}`,
	}
	saved := make([]StoredMessage, len(rows))
	for i, r := range rows {
		saved[i] = StoredMessage{Seq: int64(i + 1), Data: []byte(r)}
	}
	h, err := LoadHistory(saved, 8)
	require.NoError(t, err)
	require.False(t, h.pi)
	return h
}

// A run Genkit left interrupted resumes on pi, as saved or once converted
// (#234): no call runs again, the held write takes its answer and the next
// question goes on with the whole history.
func TestGenkitInterruptResumesOnPi(t *testing.T) {
	for _, convert := range []bool{false, true} {
		h := genkitInterrupted(t)
		if convert {
			p, _, err := h.PiHistory()
			require.NoError(t, err)
			h = p
		}
		srv := testllm.Start(t, "openai")
		srv.Script(testllm.Turn{Text: []string{"done"}}, testllm.Turn{Text: []string{"more"}})
		var readRuns atomic.Int64
		sent := resuming(h)
		evs, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: mixedTools(&readRuns),
			Responses: []ToolResult{{CallID: "a1", Name: "write", Output: json.RawMessage(`{"draft":7}`)}}})
		require.Nil(t, res.Err, convert)
		assert.Equal(t, "done", last(t, evs).Kind)
		assert.Zero(t, readRuns.Load(), "no read runs again")
		reqs := srv.Requests()
		calls, results := toolIDs(t, "openai", reqs[0].Messages)
		assert.Equal(t, []string{"r1", "r2", "r3", "a1"}, calls)
		assert.Equal(t, []string{"r1", "r2", "r3", "a1"}, results)

		h = after(t, sent, res)
		assert.Equal(t, convert, h.pi)
		sent = asking(must(LoadHistory(must(h.Rows()), h.Next())), "and?")
		_, res = run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: sent})
		require.Nil(t, res.Err)
		reqs = srv.Requests()
		msgs := string(reqs[len(reqs)-1].Messages)
		for _, s := range []string{"quoted", "first", "hi", "r1", "r2", "r3", "read-result", `\"draft\":7`, "done"} {
			assert.Contains(t, msgs, s)
		}
		assert.NotContains(t, msgs, "translate-draft", "a command is never sent")
	}
}

// A run Genkit left interrupted can be closed instead (its run cancelled) and
// the conversation goes on on pi, the read that ran keeping its result.
func TestGenkitInterruptClosesOnPi(t *testing.T) {
	closed, _, err := genkitInterrupted(t).Close([]ToolResult{{CallID: "a1", Name: "write", Error: "cancelled"}})
	require.NoError(t, err)
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "openai", "k"), History: asking(closed, "next")})
	require.Nil(t, res.Err)
	calls, results := toolIDs(t, "openai", srv.Requests()[0].Messages)
	assert.Equal(t, []string{"r1", "r2", "r3", "a1"}, calls)
	assert.Equal(t, calls, results)
	assert.Contains(t, string(srv.Requests()[0].Messages), "cancelled")
}

// An old conversation with tool calls (two results in one Genkit message), an
// interrupt, context, reasoning and a command converts to pi messages, one
// row each.
func TestPiHistoryConvertsAGenkitConversation(t *testing.T) {
	h := genkitInterrupted(t)
	p, seqOf, err := h.PiHistory()
	require.NoError(t, err)
	assert.True(t, p.pi)
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
	require.False(t, h.pi)

	p, _, err := h.PiHistory()
	require.NoError(t, err)
	sent = asking(must(LoadHistory(must(p.Rows()), p.Next())), "second")
	_, res = run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: sent, Effort: "low"})
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
	_, res := run(newEngine(t), Request{Model: modelFor(srv, KindAnthropic, "k"), History: sent})
	require.Nil(t, res.Err)
	assert.True(t, ranOnPi(t, srv))
	assert.Contains(t, string(srv.Requests()[0].Messages), `"signature":"sig-1"`)

	// So does one the startup conversion left as it was.
	_, res = run(newEngine(t), Request{Model: modelFor(srv, KindAnthropic, "k"), History: asking(genkitHistory(t), "second")})
	require.Nil(t, res.Err)
	assert.Contains(t, string(srv.Requests()[1].Messages), `"signature":"sig-1"`)

	srv = testllm.Start(t, "openai-responses")
	srv.Script(testllm.Turn{Text: []string{"two"}})
	_, res = run(newEngine(t), Request{Model: modelFor(srv, KindResponses, "k"), History: asking(p, "second")})
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

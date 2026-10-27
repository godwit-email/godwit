package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testllm"
)

const (
	testCard = "4111 1111 1111 1111"
	testID   = "11010519491231002X"
	testKey  = "sk-proj-abcdefghijklmnopqrstuvwx1234"
)

// maskedEngine is an engine that masks what it sends, with its masker.
func maskedEngine(t *testing.T) (*Engine, *mask.Masker) {
	e := newEngine(t)
	e.Masker = mask.New([]byte("0123456789abcdef0123456789abcdef"))
	return e, e.Masker
}

func streamed(evs []Event) string {
	var b strings.Builder
	for _, ev := range kindsOf(evs, "text") {
		b.WriteString(ev.Text)
	}
	return b.String()
}

// The mail and the system prompt reach every protocol as placeholders.
func TestARunSendsPlaceholders(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		e, _ := maskedEngine(t)
		srv := testllm.Start(t, kind)
		srv.Script(testllm.Turn{Text: []string{"ok"}})
		_, res := run(e, Request{Model: modelFor(srv, kind, "k"), System: "Mail: card " + testCard + ", key " + testKey, Prompt: "ID " + testID + "?"})
		require.Nil(t, res.Err)
		body := string(srv.Requests()[0].Body)
		for _, v := range []string{testCard, testKey, testID} {
			assert.NotContains(t, body, v)
		}
		for _, k := range []string{"BANK_CARD", "API_KEY", "ID_CARD"} {
			assert.Regexp(t, `\{\{`+k+`_[a-z2-7]{12}\}\}`, body)
		}
	})
}

// What the service signed or encrypted goes back as it wrote it, though it
// holds a value masked everywhere else.
func TestSealedReasoningGoesBackAsWritten(t *testing.T) {
	for kind, turn := range map[string]testllm.Turn{
		"anthropic":   {Think: []string{"card " + testCard}},
		KindResponses: {Think: []string{"card " + testCard}, Reasoning: "enc-" + testCard},
	} {
		t.Run(kind, func(t *testing.T) {
			e, _ := maskedEngine(t)
			srv := testllm.Start(t, kind)
			turn.Calls = []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"x"}`}}
			srv.Script(turn, testllm.Turn{Text: []string{"ok"}})
			search := ToolSpec{Name: "search", Schema: piSearch, Run: func(context.Context, json.RawMessage) (any, error) { return "card " + testCard, nil }}
			_, res := run(e, Request{Model: modelFor(srv, kind, "k"), History: asking(nil, "go"), Tools: []ToolSpec{search}, Effort: "low"})
			require.Nil(t, res.Err)
			reqs := srv.Requests()
			require.Len(t, reqs, 2)
			require.Empty(t, reqs[1].Invalid)
			body := string(reqs[1].Body)
			sealed := map[string]string{
				"anthropic":   `"thinking":"card ` + testCard + `","signature":"sig"`,
				KindResponses: `"encrypted_content":"enc-` + testCard + `","id":"rs_0","summary":[{"text":"card ` + testCard + `","type":"summary_text"}]`,
			}[kind]
			require.Contains(t, body, sealed)
			assert.NotContains(t, strings.Replace(body, sealed, "", 1), testCard, "the tool's result is masked")
		})
	}
}

// The model's placeholders reach the user and the tools as their values,
// one cut between two chunks too; the history keeps what the model wrote.
func TestARunRestoresWhatTheModelWrites(t *testing.T) {
	eachKind(t, func(t *testing.T, kind string) {
		e, m := maskedEngine(t)
		p, _ := m.Text(testCard)
		srv := testllm.Start(t, kind)
		srv.Script(
			testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"` + p + `"}`}}},
			testllm.Turn{Text: []string{"Card " + p[:9], p[9:] + "."}},
		)
		var ran string
		search := ToolSpec{Name: "search", Schema: piSearch, Run: func(_ context.Context, a json.RawMessage) (any, error) { ran = string(a); return "found", nil }}
		sent := asking(nil, "Is "+testCard+" mine?")
		evs, res := run(e, Request{Model: modelFor(srv, kind, "k"), History: sent, Tools: []ToolSpec{search}})
		require.Nil(t, res.Err)
		assert.JSONEq(t, `{"q":"`+testCard+`"}`, ran)
		assert.JSONEq(t, `{"q":"`+testCard+`"}`, string(kindsOf(evs, "tool_call")[0].Call.Args))
		assert.Equal(t, "Card "+testCard+".", streamed(evs))
		shown := res.Messages[len(res.Messages)-1]
		assert.Equal(t, "Card "+testCard+".", shown.Text)
		assert.JSONEq(t, `{"q":"`+testCard+`"}`, string(res.Messages[1].Calls[0].Args))

		rows := must(after(t, sent, res).Rows())
		assert.Contains(t, string(rows[1].Data), p, "the call as the model wrote it")
		assert.NotContains(t, string(rows[3].Data), testCard, "the answer as the model wrote it")
	})
}

// A placeholder in a call's arguments that names no value refuses the call:
// the model is told why, and the tool never runs.
func TestAnUnknownPlaceholderRefusesTheCall(t *testing.T) {
	e, _ := maskedEngine(t)
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"{{BANK_CARD_aaaaaaaaaaaa}}"}`}}}, testllm.Turn{Text: []string{"ok"}})
	search := ToolSpec{Name: "search", Schema: piSearch, Run: func(context.Context, json.RawMessage) (any, error) {
		t.Error("ran")
		return nil, nil
	}}
	evs, res := run(e, Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "go"), Tools: []ToolSpec{search}})
	require.Nil(t, res.Err)
	require.Len(t, kindsOf(evs, "tool_result"), 1)
	assert.Contains(t, kindsOf(evs, "tool_result")[0].Result.Error, "placeholder")
	assert.Contains(t, string(srv.Requests()[1].Messages), "placeholder")
}

// A held call's arguments, the interrupt shows, are its values.
func TestAnInterruptShowsTheValues(t *testing.T) {
	e, m := maskedEngine(t)
	p, _ := m.Text(testCard)
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "a1", Name: "write", Args: `{"to":"` + p + `"}`}}})
	write := ToolSpec{Name: "write", Schema: map[string]any{"type": "object"}, NeedsApproval: true}
	evs, res := run(e, Request{Model: modelFor(srv, "openai", "k"), History: asking(nil, "go"), Tools: []ToolSpec{write}})
	require.Nil(t, res.Err)
	require.Equal(t, "interrupted", last(t, evs).Kind)
	assert.JSONEq(t, `{"to":"`+testCard+`"}`, string(last(t, evs).Interrupts[0].Args))
}

// Apple Intelligence runs on this device, and masking can be turned off:
// the values go as they are, and what comes back is restored all the same.
func TestOnDeviceOrOffSendsTheValues(t *testing.T) {
	for name, off := range map[string]func(*ResolvedModel, *mask.Masker){
		"on device": func(r *ResolvedModel, _ *mask.Masker) { r.OnDevice = true },
		"off":       func(_ *ResolvedModel, m *mask.Masker) { m.SetOn(false) },
	} {
		t.Run(name, func(t *testing.T) {
			e, m := maskedEngine(t)
			p, _ := m.Text(testCard)
			srv := testllm.Start(t, "openai")
			srv.Script(testllm.Turn{Text: []string{"It is " + p}})
			model := modelFor(srv, "openai", "k")
			off(&model, m)
			evs, res := run(e, Request{Model: model, Prompt: "Card " + testCard + "?"})
			require.Nil(t, res.Err)
			assert.Contains(t, string(srv.Requests()[0].Body), testCard)
			assert.Equal(t, "It is "+testCard, streamed(evs))
			assert.Equal(t, "It is "+testCard, res.Messages[1].Text)
		})
	}
}

// A judge's request is masked too.
func TestAJudgeIsAskedWithPlaceholders(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		io.WriteString(w, `{"answers":[{"type":"predicate","name":"urgent","probability":0.9}]}`)
	}))
	defer srv.Close()
	_, m := maskedEngine(t)
	j, err := NewJudge(nil, m, FormatDecisions, srv.URL, "k", "m")
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "Card "+testCard, []Question{{Name: "urgent", Type: Predicate, Instructions: "Urgent?"}})
	require.NoError(t, err)
	assert.NotContains(t, got, testCard)
	assert.Contains(t, got, "{{BANK_CARD_")
}

// Arguments the model wrote that do not parse come back in the call's result
// as written (pi-ai's argumentsError): the user sees, and the history keeps,
// their values.
func TestAnArgumentsErrorShowsTheValues(t *testing.T) {
	e, m := maskedEngine(t)
	p, _ := m.Text(testCard)
	srv := testllm.Start(t, "openai")
	srv.Script(testllm.Turn{Calls: []testllm.Call{{ID: "c1", Name: "search", Args: `{"q":"` + p + `"`}}}, testllm.Turn{Text: []string{"ok"}})
	search := ToolSpec{Name: "search", Schema: piSearch, Run: func(context.Context, json.RawMessage) (any, error) {
		t.Error("ran")
		return nil, nil
	}}
	sent := asking(nil, "go")
	evs, res := run(e, Request{Model: modelFor(srv, "openai", "k"), History: sent, Tools: []ToolSpec{search}})
	require.Nil(t, res.Err)
	require.Len(t, kindsOf(evs, "tool_result"), 1)
	live := kindsOf(evs, "tool_result")[0].Result.Error
	assert.Contains(t, live, testCard)
	assert.NotContains(t, live, p)
	require.Len(t, res.Messages[2].Results, 1)
	shown := string(res.Messages[2].Results[0].Output) + res.Messages[2].Results[0].Error
	assert.Contains(t, shown, testCard)
	assert.NotContains(t, shown, p)

	rows := must(after(t, sent, res).Rows())
	assert.Contains(t, string(rows[2].Data), testCard, "the result as the user sees it")
	assert.NotContains(t, string(rows[2].Data), p)
	assert.NotContains(t, string(srv.Requests()[1].Body), testCard, "and the model sees the placeholder again")
}

// A result stored with placeholders, before they were restored, shows its values.
func TestAStoredResultShowsTheValues(t *testing.T) {
	_, m := maskedEngine(t)
	p, _ := m.Text(testCard)
	out, _ := json.Marshal("Received arguments: " + p)
	msg := RestoreMessage(m, Message{Role: "tool", Results: []ToolResult{{CallID: "c1", Output: out, Error: "card " + p}}})
	assert.JSONEq(t, `"Received arguments: `+testCard+`"`, string(msg.Results[0].Output))
	assert.Equal(t, "card "+testCard, msg.Results[0].Error)
}

package testllm

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sdk(s *Server) *openai.Client {
	c := openai.NewClient(option.WithBaseURL(s.URL), option.WithAPIKey("k"))
	return &c
}

// stream runs one Responses request with the real SDK and returns its events.
func stream(t *testing.T, s *Server, p responses.ResponseNewParams) ([]responses.ResponseStreamEventUnion, error) {
	t.Helper()
	st := sdk(s).Responses.NewStreaming(t.Context(), p)
	defer st.Close()
	var evs []responses.ResponseStreamEventUnion
	for st.Next() {
		evs = append(evs, st.Current())
	}
	return evs, st.Err()
}

func types(evs []responses.ResponseStreamEventUnion) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

var withReasoning = []responses.ResponseIncludable{"reasoning.encrypted_content"}

func TestResponsesFakeStream(t *testing.T) {
	s := Start(t, "openai-responses")
	s.Script(Turn{Text: []string{"Hel", "lo"}, Calls: []Call{{Name: "look", Args: `{"q":"x"}`}}, Reasoning: "enc-1"},
		Turn{Text: []string{"done"}})
	tool := responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{Name: "look",
		Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(false)}}
	p := responses.ResponseNewParams{Model: "m1", Store: openai.Bool(false), Include: withReasoning,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hi")}, Tools: []responses.ToolUnionParam{tool}}
	evs, err := stream(t, s, p)
	require.NoError(t, err)
	assert.Equal(t, []string{"response.created",
		"response.output_item.added", "response.output_item.done", // reasoning
		"response.output_item.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.completed"}, types(evs))

	var text, args string
	var rs responses.ResponseReasoningItem
	var fc responses.ResponseFunctionToolCall
	for i, e := range evs {
		assert.EqualValues(t, i, e.SequenceNumber)
		switch v := e.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			text += v.Delta
			assert.EqualValues(t, 1, v.OutputIndex)
			assert.True(t, strings.HasPrefix(v.ItemID, "msg_"))
		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			args += v.Delta
			assert.EqualValues(t, 2, v.OutputIndex)
			assert.True(t, strings.HasPrefix(v.ItemID, "fc_"))
		case responses.ResponseFunctionCallArgumentsDoneEvent:
			assert.Equal(t, `{"q":"x"}`, v.Arguments)
		case responses.ResponseOutputItemDoneEvent:
			switch it := v.Item.AsAny().(type) {
			case responses.ResponseReasoningItem:
				rs = it
			case responses.ResponseFunctionToolCall:
				fc = it
			}
		case responses.ResponseCompletedEvent:
			assert.Equal(t, responses.ResponseStatusCompleted, v.Response.Status)
			assert.Len(t, v.Response.Output, 3)
			assert.EqualValues(t, 2, v.Response.Usage.TotalTokens)
			assert.Equal(t, "Hello", v.Response.OutputText())
		}
	}
	assert.Equal(t, "Hello", text)
	assert.Equal(t, `{"q":"x"}`, args)
	assert.Equal(t, "enc-1", rs.EncryptedContent)
	assert.True(t, strings.HasPrefix(rs.ID, "rs_"))
	assert.True(t, strings.HasPrefix(fc.ID, "fc_"))
	assert.True(t, strings.HasPrefix(fc.CallID, "call_"))
	assert.Equal(t, "look", fc.Name)

	// The continuation, built the way a client would, passes validation.
	rp, fp := rs.ToParam(), fc.ToParam()
	p.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage("hi", responses.EasyInputMessageRoleUser),
		{OfReasoning: &rp}, {OfFunctionCall: &fp},
		responses.ResponseInputItemParamOfFunctionCallOutput(fc.CallID, `{"r":1}`),
	}}
	evs, err = stream(t, s, p)
	require.NoError(t, err)
	reqs := s.Requests()
	require.Len(t, reqs, 2)
	assert.Empty(t, reqs[1].Invalid)
	assert.Equal(t, "response.completed", evs[len(evs)-1].Type)
	assert.Equal(t, "done", evs[len(evs)-1].Response.OutputText())
}

func TestResponsesFakeEndings(t *testing.T) {
	s := Start(t, "openai-responses")
	p := responses.ResponseNewParams{Model: "m1", Store: openai.Bool(false),
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hi")}}

	s.Script(Turn{Text: []string{"a"}, Fail: "boom"})
	evs, err := stream(t, s, p)
	require.NoError(t, err)
	end := evs[len(evs)-1]
	assert.Equal(t, "response.failed", end.Type)
	assert.Equal(t, "boom", end.Response.Error.Message)

	s.Script(Turn{Text: []string{"a"}, Incomplete: true})
	evs, err = stream(t, s, p)
	require.NoError(t, err)
	end = evs[len(evs)-1]
	assert.Equal(t, "response.incomplete", end.Type)
	assert.Equal(t, "max_output_tokens", end.Response.IncompleteDetails.Reason)

	s.Script(Turn{Text: []string{"a"}, Calls: []Call{{Name: "f"}}, Truncate: true})
	evs, err = stream(t, s, p)
	require.NoError(t, err)
	for _, e := range evs {
		assert.NotContains(t, []string{"response.completed", "response.failed", "response.incomplete"}, e.Type)
	}
	assert.Equal(t, "response.output_item.done", evs[len(evs)-1].Type)

	// A status keeps answering the SDK's retries until the script moves on.
	s.Script(Turn{Status: 500})
	n := len(s.Requests())
	_, err = stream(t, s, p)
	require.Error(t, err)
	assert.Len(t, s.Requests(), n+3, "the first try and two retries all fail")
	s.Script(Turn{Text: []string{"back"}})
	evs, err = stream(t, s, p)
	require.NoError(t, err)
	assert.Equal(t, "back", evs[len(evs)-1].Response.OutputText())
}

func post(t *testing.T, s *Server, path, body string) int {
	t.Helper()
	res, err := http.Post(s.URL+path, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode
}

func TestResponsesFakeValidates(t *testing.T) {
	s := Start(t, "openai-responses")
	s.Script(Turn{Calls: []Call{{ID: "call_r", Name: "f"}}, Reasoning: "enc-9"}, Turn{Text: []string{"fine"}})
	bad := map[string]string{
		"no store":         `{"model":"m","input":"hi"}`,
		"store true":       `{"model":"m","store":true,"input":"hi"}`,
		"no include":       `{"model":"m","store":false,"input":"hi"}`, // the next turn has reasoning
		"tool sans strict": `{"model":"m","store":false,"include":["reasoning.encrypted_content"],"input":"hi","tools":[{"type":"function","name":"f","parameters":{}}]}`,
		"unknown call_id":  `{"model":"m","store":false,"include":["reasoning.encrypted_content"],"input":[{"type":"function_call_output","call_id":"call_x","output":"r"}]}`,
		"object output":    `{"model":"m","store":false,"include":["reasoning.encrypted_content"],"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":{"a":1}}]}`,
	}
	for name, body := range bad {
		assert.Equal(t, 400, post(t, s, "/responses", body), name)
	}
	reqs := s.Requests()
	require.Len(t, reqs, len(bad))
	for _, r := range reqs {
		assert.NotEmpty(t, r.Invalid, string(r.Body))
	}

	// The scripted turn was not used up by the rejected requests.
	ok := `{"model":"m","store":false,"include":["reasoning.encrypted_content"],"input":"hi","tools":[{"type":"function","name":"f","parameters":{},"strict":true}]}`
	require.Equal(t, 200, post(t, s, "/responses", ok))
	call := `{"type":"function_call","call_id":"call_r","name":"f","arguments":"{}"}`
	out := `{"type":"function_call_output","call_id":"call_r","output":"r"}`
	reasoning := `{"type":"reasoning","id":"rs_` + itoa(len(bad)) + `","summary":[],"encrypted_content":"enc-9"}`
	wrap := func(items ...string) string {
		return `{"model":"m","store":false,"input":[` + strings.Join(items, ",") + `]}`
	}
	assert.Equal(t, 400, post(t, s, "/responses", wrap(call, out)), "reasoning not carried back")
	assert.Equal(t, 400, post(t, s, "/responses", wrap(call, reasoning, out)), "reasoning after its call")
	assert.Equal(t, 400, post(t, s, "/responses", wrap(strings.Replace(reasoning, "enc-9", "enc-0", 1), call, out)), "reasoning altered")
	assert.Equal(t, 200, post(t, s, "/responses", wrap(reasoning, call, out)))
	last := s.Requests()[len(s.Requests())-1]
	assert.Empty(t, last.Invalid)

	c := Start(t, "openai")
	assert.Equal(t, 400, post(t, c, "/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_x","content":"r"}]}`))
	assert.NotEmpty(t, c.Requests()[0].Invalid)
	assert.Equal(t, 200, post(t, c, "/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"call_x","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_x","content":"r"}]}`))
	assert.Empty(t, c.Requests()[1].Invalid)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRequestRecordsModel(t *testing.T) {
	for kind, path := range map[string]string{"openai": "/chat/completions", "openai-responses": "/responses", "anthropic": "/v1/messages"} {
		s := Start(t, kind)
		body := `{"model":"vendor/m2","store":false,"max_tokens":5,"input":"hi","messages":[{"role":"user","content":"hi"}]}`
		require.Equal(t, 200, post(t, s, path, body), kind)
		r := s.Requests()[0]
		assert.Equal(t, "vendor/m2", r.Model, kind)
		assert.JSONEq(t, body, string(r.Body), kind)
		assert.Equal(t, path, r.Path, kind)
	}
}

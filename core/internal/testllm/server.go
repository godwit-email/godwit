// Package testllm is a fake model service for tests: the model-list endpoint and
// a scripted, streaming chat endpoint for each protocol (openai, openai-responses,
// anthropic). Requests a real service would reject get a 400 and are marked.
package testllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

type Server struct {
	URL string // base URL, as a user would enter it

	mu         sync.Mutex
	models     []string
	hidden     []string
	names      map[string]string // non-nil: list in the Sign in with ChatGPT shape (models[].slug/display_name)
	key        string
	listStatus int
	headers    []http.Header
	turns      []Turn
	requests   []Request
	release    chan struct{}
	stuck      bool        // the head turn is a Status that keeps answering
	emitted    [][]outItem // the output of every finished turn with reasoning
	Mux        *http.ServeMux
}

// Turn is one scripted model response. Status != 0 answers with that HTTP error
// (its body echoes the key the request carried). Hang streams the text,
// then waits for Release or the client to go away. Finish, if set, is the
// raw finish reason sent (OpenAI finish_reason, Anthropic stop_reason).
//
// openai-responses only: Reasoning adds a reasoning item with that
// encrypted_content; Fail ends with response.failed carrying that message,
// FailCode sets its error.code (default server_error), Incomplete with
// response.incomplete, Truncate with no terminal event. A Status turn keeps
// answering (the SDK's retries) until Advance or the next Script call.
type Turn struct {
	Text       []string
	Calls      []Call
	Hang       bool
	Status     int
	Code       string // openai-responses: the error.code a Status turn's body carries
	Finish     string
	Reasoning  string
	Fail       string
	FailCode   string
	Incomplete bool
	Truncate   bool
	Refusal    string // openai-responses: the reply is this refusal, not text
	// Think is the readable reasoning's deltas, before the text: openai chat's
	// reasoning_content, anthropic's thinking block, openai-responses' summary
	// of the Reasoning item (set Reasoning too).
	Think []string
	Usage [2]int // openai chat and responses: the input and output tokens reported (responses: 1 and 1 when unset)
}

// Call is a tool call; an empty ID gets "call_<n>".
type Call struct {
	ID, Name string
	Args     string // JSON object
}

// Request is the raw JSON of one chat request. For openai-responses System is
// the instructions and Messages the input. Invalid says why the fake rejected
// it with a 400; empty means it was answered from the script.
type Request struct {
	System, Messages, Tools json.RawMessage
	Header                  http.Header
	Model, Path, Invalid    string
	Body                    json.RawMessage
}

// Start serves the endpoints of kind ("openai", "openai-responses" or "anthropic").
func Start(t *testing.T, kind string) *Server {
	t.Helper()
	s := &Server{Mux: http.NewServeMux(), release: make(chan struct{})}
	list, chat := "/models", "/chat/completions"
	switch kind {
	case "anthropic":
		list, chat = "/v1/models", "/v1/messages"
	case "openai-responses":
		chat = "/responses"
	}
	s.Mux.HandleFunc("GET "+list, func(w http.ResponseWriter, r *http.Request) { s.list(kind, w, r, false) })
	if kind == "openai-responses" { // ChatGPT's Codex backend: client_version is required, and the list is models[].slug
		s.Mux.HandleFunc("GET /backend-api/codex/models", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("client_version") == "" {
				http.Error(w, `{"message":"client_version: Field required"}`, http.StatusBadRequest)
				return
			}
			s.list(kind, w, r, true)
		})
	}
	s.Mux.HandleFunc("POST "+chat, func(w http.ResponseWriter, r *http.Request) { s.chat(kind, w, r) })
	if kind == "openai-responses" { // ChatGPT's Codex backend, at <URL>/backend-api/codex
		s.Mux.HandleFunc("POST /backend-api/codex/responses", func(w http.ResponseWriter, r *http.Request) { s.chat(kind, w, r) })
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		s.Mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { s.Release(); ts.Close() })
	s.URL = ts.URL
	return s
}

// list answers the model list; codex forces the Codex backend's models[].slug shape.
func (s *Server) list(kind string, w http.ResponseWriter, r *http.Request, codex bool) {
	{
		s.mu.Lock()
		models, hidden, names, key, status := s.models, s.hidden, s.names, s.key, s.listStatus
		s.mu.Unlock()
		if codex && names == nil {
			names = map[string]string{}
		}
		got := sentKey(kind, r)
		if status != 0 {
			// echoes the key, so tests can prove errors repeat neither it nor the body
			http.Error(w, "UPSTREAM-BODY bad key "+got, status)
			return
		}
		if key != "" && got != key {
			// echoes what it got, so tests can prove errors don't repeat it
			http.Error(w, "bad key "+got, http.StatusUnauthorized)
			return
		}
		data, field := []map[string]string{}, "data"
		for _, m := range models {
			item := map[string]string{"id": m}
			if names != nil {
				item, field = map[string]string{"slug": m, "display_name": names[m]}, "models"
			}
			if len(hidden) > 0 || names != nil {
				item["visibility"] = "list"
				if slices.Contains(hidden, m) {
					item["visibility"] = "hidden"
				}
			}
			data = append(data, item)
		}
		json.NewEncoder(w).Encode(map[string]any{field: data})
	}
}

func sentKey(kind string, r *http.Request) string {
	if kind != "anthropic" {
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return r.Header.Get("x-api-key")
}

func (s *Server) SetModels(m ...string) { s.mu.Lock(); s.models = m; s.mu.Unlock() }
func (s *Server) SetHidden(m ...string) { s.mu.Lock(); s.hidden = m; s.mu.Unlock() }

// SetDisplayNames lists models the way a ChatGPT subscription token sees them: models[].slug with display_name.
func (s *Server) SetDisplayNames(n map[string]string) { s.mu.Lock(); s.names = n; s.mu.Unlock() }
func (s *Server) RequireKey(k string)                 { s.mu.Lock(); s.key = k; s.mu.Unlock() }

// SetListStatus makes the model list answer with that HTTP status; 0 restores it.
func (s *Server) SetListStatus(code int) { s.mu.Lock(); s.listStatus = code; s.mu.Unlock() }

// Script appends turns; once they run out every request gets the text "ok".
func (s *Server) Script(turns ...Turn) {
	s.mu.Lock()
	if s.stuck {
		s.turns, s.stuck = s.turns[1:], false
	}
	s.turns = append(s.turns, turns...)
	s.mu.Unlock()
}

// Advance drops the current turn, e.g. a Status turn that keeps answering.
func (s *Server) Advance() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) > 0 {
		s.turns = s.turns[1:]
	}
	s.stuck = false
}

// Release lets hanging turns finish. It is safe to call more than once.
func (s *Server) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

// Headers returns the headers of every request so far.
func (s *Server) Headers() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.headers...)
}

// Requests returns every chat request so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) chat(kind string, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var in struct {
		Model                                        string
		System, Messages, Tools, Instructions, Input json.RawMessage
	}
	json.Unmarshal(body, &in)
	if kind == "openai-responses" {
		in.System, in.Messages = in.Instructions, in.Input
	}
	if kind == "openai" { // the system prompt is the first message
		var msgs []struct {
			Role    string
			Content json.RawMessage
		}
		json.Unmarshal(in.Messages, &msgs)
		if len(msgs) > 0 && msgs[0].Role == "system" {
			in.System = msgs[0].Content
		}
	}
	s.mu.Lock()
	n := len(s.requests)
	turn := Turn{Text: []string{"ok"}}
	if len(s.turns) > 0 {
		turn = s.turns[0]
	}
	req := Request{System: in.System, Messages: in.Messages, Tools: in.Tools, Header: r.Header.Clone(),
		Model: in.Model, Path: r.URL.Path, Body: body, Invalid: s.validate(kind, body)}
	s.requests = append(s.requests, req)
	if req.Invalid == "" && len(s.turns) > 0 {
		if kind == "openai-responses" && turn.Status != 0 {
			s.stuck = true
		} else {
			s.turns = s.turns[1:]
		}
	}
	turn.Calls = append([]Call(nil), turn.Calls...)
	for i := range turn.Calls {
		if turn.Calls[i].ID == "" {
			turn.Calls[i].ID = fmt.Sprintf("call_%d_%d", n, i)
		}
		if turn.Calls[i].Args == "" {
			turn.Calls[i].Args = "{}"
		}
	}
	if kind == "openai-responses" && turn.Reasoning != "" && turn.Status == 0 && req.Invalid == "" &&
		turn.Fail == "" && turn.FailCode == "" && !turn.Incomplete && !turn.Truncate && turn.Refusal == "" {
		out := []outItem{{"reasoning", fmt.Sprintf("rs_%d", n), turn.Reasoning}}
		if len(turn.Text) > 0 {
			out = append(out, outItem{typ: "message"})
		}
		for _, c := range turn.Calls {
			out = append(out, outItem{typ: "function_call", key: c.ID})
		}
		s.emitted = append(s.emitted, out)
	}
	s.mu.Unlock()

	if req.Invalid != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":`+js(req.Invalid)+`}}`)
		return
	}
	if turn.Status != 0 {
		w.Header().Set("retry-after-ms", "1") // SDK retries come back at once
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(turn.Status)
		fmt.Fprintf(w, `{"error":{"type":"error","code":%s,"message":"rejected key %s"}}`, js(turn.Code), sentKey(kind, r))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	send := func(lines ...string) {
		for _, l := range lines {
			fmt.Fprint(w, l+"\n\n")
		}
		f.Flush()
	}
	hang := func() bool { return s.hang(r) }
	switch kind {
	case "openai":
		openAIStream(send, turn, hang)
	case "openai-responses":
		responsesStream(send, turn, n, in.Model, hang)
	default:
		anthropicStream(send, turn, hang)
	}
}

// hang waits for Release or the client; false means the client left.
func (s *Server) hang(r *http.Request) bool {
	select {
	case <-s.release:
		return true
	case <-r.Context().Done():
		return false
	}
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

func openAIStream(send func(...string), t Turn, hang func() bool) {
	chunk := func(delta any, finish any) string {
		return "data: " + js(map[string]any{"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	}
	for _, r := range t.Think {
		send(chunk(map[string]any{"role": "assistant", "reasoning_content": r}, nil))
	}
	for _, txt := range t.Text {
		send(chunk(map[string]any{"role": "assistant", "content": txt}, nil))
	}
	if t.Hang && !hang() {
		return
	}
	for i, c := range t.Calls {
		send(chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": c.Args}}}}, nil))
	}
	finish := "stop"
	if len(t.Calls) > 0 {
		finish = "tool_calls"
	}
	if t.Finish != "" {
		finish = t.Finish
	}
	send(chunk(map[string]any{}, finish))
	if t.Usage != [2]int{} {
		send("data: " + js(map[string]any{"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "m", "choices": []any{},
			"usage": map[string]any{"prompt_tokens": t.Usage[0], "completion_tokens": t.Usage[1], "total_tokens": t.Usage[0] + t.Usage[1]}}))
	}
	send("data: [DONE]")
}

func anthropicStream(send func(...string), t Turn, hang func() bool) {
	ev := func(name string, data map[string]any) string {
		data["type"] = name
		return "event: " + name + "\ndata: " + js(data)
	}
	send(ev("message_start", map[string]any{"message": map[string]any{"id": "m1", "type": "message", "role": "assistant", "model": "claude",
		"content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}}))
	idx := 0
	if len(t.Think) > 0 {
		send(ev("content_block_start", map[string]any{"index": idx, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}}))
		for _, th := range t.Think {
			send(ev("content_block_delta", map[string]any{"index": idx, "delta": map[string]any{"type": "thinking_delta", "thinking": th}}))
		}
		send(ev("content_block_delta", map[string]any{"index": idx, "delta": map[string]any{"type": "signature_delta", "signature": "sig"}}),
			ev("content_block_stop", map[string]any{"index": idx}))
		idx++
	}
	for _, txt := range t.Text {
		send(ev("content_block_start", map[string]any{"index": idx, "content_block": map[string]any{"type": "text", "text": ""}}),
			ev("content_block_delta", map[string]any{"index": idx, "delta": map[string]any{"type": "text_delta", "text": txt}}),
			ev("content_block_stop", map[string]any{"index": idx}))
		idx++
	}
	if t.Hang && !hang() {
		return
	}
	for _, c := range t.Calls {
		send(ev("content_block_start", map[string]any{"index": idx, "content_block": map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": map[string]any{}}}),
			ev("content_block_delta", map[string]any{"index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": c.Args}}),
			ev("content_block_stop", map[string]any{"index": idx}))
		idx++
	}
	stop := "end_turn"
	if len(t.Calls) > 0 {
		stop = "tool_use"
	}
	if t.Finish != "" {
		stop = t.Finish
	}
	send(ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 1}}),
		ev("message_stop", map[string]any{}))
}

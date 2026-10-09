// Package testllm is a fake model service for tests: the model-list endpoint and
// a scripted, streaming chat endpoint for both protocols.
package testllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Server struct {
	URL string // base URL, as a user would enter it

	mu         sync.Mutex
	models     []string
	key        string
	listStatus int
	headers    []http.Header
	turns      []Turn
	requests   []Request
	release    chan struct{}
	Mux        *http.ServeMux
}

// Turn is one scripted model response. Status != 0 answers with that HTTP error
// (its body echoes the key the request carried). Hang streams the text,
// then waits for Release or the client to go away. Finish, if set, is the
// raw finish reason sent (OpenAI finish_reason, Anthropic stop_reason).
type Turn struct {
	Text   []string
	Calls  []Call
	Hang   bool
	Status int
	Finish string
}

// Call is a tool call; an empty ID gets "call_<n>".
type Call struct {
	ID, Name string
	Args     string // JSON object
}

// Request is the raw JSON of one chat request.
type Request struct {
	System, Messages, Tools json.RawMessage
	Header                  http.Header
}

// Start serves the endpoints of kind ("openai" or "anthropic").
func Start(t *testing.T, kind string) *Server {
	t.Helper()
	s := &Server{Mux: http.NewServeMux(), release: make(chan struct{})}
	list, chat := "/models", "/chat/completions"
	if kind == "anthropic" {
		list, chat = "/v1/models", "/v1/messages"
	}
	s.Mux.HandleFunc("GET "+list, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		models, key, status := s.models, s.key, s.listStatus
		s.mu.Unlock()
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
		data := []map[string]string{}
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	s.Mux.HandleFunc("POST "+chat, func(w http.ResponseWriter, r *http.Request) { s.chat(kind, w, r) })
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

func sentKey(kind string, r *http.Request) string {
	if kind == "openai" {
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return r.Header.Get("x-api-key")
}

func (s *Server) SetModels(m ...string) { s.mu.Lock(); s.models = m; s.mu.Unlock() }
func (s *Server) RequireKey(k string)   { s.mu.Lock(); s.key = k; s.mu.Unlock() }

// SetListStatus makes the model list answer with that HTTP status; 0 restores it.
func (s *Server) SetListStatus(code int) { s.mu.Lock(); s.listStatus = code; s.mu.Unlock() }

// Script appends turns; once they run out every request gets the text "ok".
func (s *Server) Script(turns ...Turn) {
	s.mu.Lock()
	s.turns = append(s.turns, turns...)
	s.mu.Unlock()
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
	var in struct{ System, Messages, Tools json.RawMessage }
	json.Unmarshal(body, &in)
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
	s.requests = append(s.requests, Request{in.System, in.Messages, in.Tools, r.Header.Clone()})
	turn := Turn{Text: []string{"ok"}}
	if len(s.turns) > 0 {
		turn, s.turns = s.turns[0], s.turns[1:]
	}
	s.mu.Unlock()

	if turn.Status != 0 {
		w.Header().Set("retry-after-ms", "1") // SDK retries come back at once
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(turn.Status)
		fmt.Fprintf(w, `{"error":{"type":"error","message":"rejected key %s"}}`, sentKey(kind, r))
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
	for i := range turn.Calls {
		if turn.Calls[i].ID == "" {
			turn.Calls[i].ID = fmt.Sprintf("call_%d_%d", n, i)
		}
		if turn.Calls[i].Args == "" {
			turn.Calls[i].Args = "{}"
		}
	}
	if kind == "openai" {
		openAIStream(send, turn, func() bool { return s.hang(r) })
	} else {
		anthropicStream(send, turn, func() bool { return s.hang(r) })
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
	send(chunk(map[string]any{}, finish), "data: [DONE]")
}

func anthropicStream(send func(...string), t Turn, hang func() bool) {
	ev := func(name string, data map[string]any) string {
		data["type"] = name
		return "event: " + name + "\ndata: " + js(data)
	}
	send(ev("message_start", map[string]any{"message": map[string]any{"id": "m1", "type": "message", "role": "assistant", "model": "claude",
		"content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}}))
	idx := 0
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

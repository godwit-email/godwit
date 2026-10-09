package testllm

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// reasoning is an emitted reasoning item; before is what followed it in that
// output: "message", a function call's call_id, or "".
type reasoning struct{ id, enc, before string }

// validate returns why a real service would reject body, or "". Called with
// s.mu held.
func (s *Server) validate(kind string, body []byte) string {
	switch kind {
	case "openai":
		var b struct {
			Messages []struct {
				Role       string
				ToolCallID string                `json:"tool_call_id"`
				ToolCalls  []struct{ ID string } `json:"tool_calls"`
			}
		}
		json.Unmarshal(body, &b)
		calls := map[string]bool{}
		for _, m := range b.Messages {
			for _, c := range m.ToolCalls {
				calls[c.ID] = true
			}
			if m.Role == "tool" && !calls[m.ToolCallID] {
				return fmt.Sprintf("tool_call_id %q answers no earlier tool call", m.ToolCallID)
			}
		}
	case "openai-responses":
		var b struct {
			Store   *bool
			Include []string
			Input   json.RawMessage
			Tools   []map[string]json.RawMessage
		}
		if err := json.Unmarshal(body, &b); err != nil {
			return "body is not a JSON object"
		}
		if b.Store == nil || *b.Store {
			return "store must be explicitly false"
		}
		for _, t := range b.Tools {
			if _, ok := t["strict"]; !ok {
				return "tool without an explicit strict"
			}
		}
		if !slices.Contains(b.Include, "reasoning.encrypted_content") {
			return "include lacks reasoning.encrypted_content"
		}
		type item struct {
			Type, ID, Role   string
			CallID           string `json:"call_id"`
			EncryptedContent string `json:"encrypted_content"`
			Output           json.RawMessage
		}
		var items []item
		json.Unmarshal(b.Input, &items) // a plain string input has no items
		// Every reasoning item sent must come back, in order, before what followed it.
		last := -1
		for _, rs := range s.emitted {
			at := slices.IndexFunc(items, func(it item) bool { return it.Type == "reasoning" && it.ID == rs.id })
			if at < 0 || items[at].EncryptedContent != rs.enc {
				return fmt.Sprintf("reasoning %s is not carried back with its encrypted_content", rs.id)
			}
			if at < last {
				return fmt.Sprintf("reasoning %s is out of order", rs.id)
			}
			last = at
			if rs.before != "" && !slices.ContainsFunc(items[at+1:], func(it item) bool {
				return rs.before == "message" && it.Role == "assistant" || it.Type == "function_call" && it.CallID == rs.before
			}) {
				return fmt.Sprintf("reasoning %s does not come before the item that followed it", rs.id)
			}
		}
		calls := map[string]bool{}
		for _, it := range items {
			switch it.Type {
			case "function_call":
				calls[it.CallID] = true
			case "function_call_output":
				if !calls[it.CallID] {
					return fmt.Sprintf("function_call_output %q answers no earlier function_call", it.CallID)
				}
				if !strings.HasPrefix(string(it.Output), `"`) {
					return "function_call_output output must be a string"
				}
			}
		}
	}
	return ""
}

// responsesStream writes t as the Responses API's SSE events: reasoning (as
// the real service, first), the message, the function calls, then the end.
func responsesStream(send func(...string), t Turn, n int, model string, hang func() bool) {
	seq := 0
	ev := func(typ string, data map[string]any) string {
		data["type"], data["sequence_number"] = typ, seq
		seq++
		return "event: " + typ + "\ndata: " + js(data)
	}
	var output []any
	resp := func(status string) map[string]any {
		out := append([]any{}, output...)
		return map[string]any{"id": fmt.Sprintf("resp_%d", n), "object": "response", "created_at": 1, "model": model,
			"status": status, "output": out, "error": nil, "incomplete_details": nil,
			"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}}
	}
	item := func(idx int, added, done map[string]any, between func()) {
		send(ev("response.output_item.added", map[string]any{"output_index": idx, "item": added}))
		between()
		send(ev("response.output_item.done", map[string]any{"output_index": idx, "item": done}))
		output = append(output, done)
	}
	send(ev("response.created", map[string]any{"response": resp("in_progress")}))
	if t.Reasoning != "" {
		rs := map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_%d", n), "summary": []any{}, "encrypted_content": t.Reasoning}
		item(len(output), rs, rs, func() {})
	}
	if len(t.Text) > 0 {
		idx, id := len(output), fmt.Sprintf("msg_%d", n)
		msg := func(status string, content []any) map[string]any {
			return map[string]any{"type": "message", "id": id, "role": "assistant", "status": status, "content": content}
		}
		send(ev("response.output_item.added", map[string]any{"output_index": idx, "item": msg("in_progress", []any{})}))
		for _, txt := range t.Text {
			send(ev("response.output_text.delta", map[string]any{"item_id": id, "output_index": idx, "content_index": 0, "delta": txt, "logprobs": []any{}}))
		}
		if t.Hang && !hang() {
			return
		}
		full := strings.Join(t.Text, "")
		done := msg("completed", []any{map[string]any{"type": "output_text", "text": full, "annotations": []any{}}})
		send(ev("response.output_text.done", map[string]any{"item_id": id, "output_index": idx, "content_index": 0, "text": full, "logprobs": []any{}}),
			ev("response.output_item.done", map[string]any{"output_index": idx, "item": done}))
		output = append(output, done)
	} else if t.Hang && !hang() {
		return
	}
	for i, c := range t.Calls {
		idx, id := len(output), fmt.Sprintf("fc_%d_%d", n, i)
		fc := func(status, args string) map[string]any {
			return map[string]any{"type": "function_call", "id": id, "call_id": c.ID, "name": c.Name, "arguments": args, "status": status}
		}
		item(idx, fc("in_progress", ""), fc("completed", c.Args), func() {
			r := []rune(c.Args)
			for _, part := range []string{string(r[:len(r)/2]), string(r[len(r)/2:])} {
				if part != "" {
					send(ev("response.function_call_arguments.delta", map[string]any{"item_id": id, "output_index": idx, "delta": part}))
				}
			}
			send(ev("response.function_call_arguments.done", map[string]any{"item_id": id, "output_index": idx, "arguments": c.Args}))
		})
	}
	switch {
	case t.Truncate:
		return // the stream just ends
	case t.Fail != "":
		r := resp("failed")
		r["error"] = map[string]any{"code": "server_error", "message": t.Fail}
		send(ev("response.failed", map[string]any{"response": r}))
	case t.Incomplete:
		r := resp("incomplete")
		r["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		send(ev("response.incomplete", map[string]any{"response": r}))
	default:
		r := resp("completed")
		r["usage"] = map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
			"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}
		send(ev("response.completed", map[string]any{"response": r}))
	}
}

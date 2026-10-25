package ai

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A conversation started on pi saves its messages as pi-ai's Message (pi
// messages); History works on their Genkit form, which piMessage and
// genkitMessage convert between. A pi message keeps what Genkit has no room
// for (signatures, usage), so it is saved as pi wrote it.

// piMessage is the part of a pi message Godwit reads.
type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	Timestamp  *json.Number    `json:"timestamp"`
}

// piBlock is one content block. GodwitContext marks a question's context
// text (contextKey), GodwitCommand a command (commandKey); GodwitMetadata is
// a tool call's Genkit metadata (a Genkit run on a pi conversation: the
// output of a call that ran before an interrupt). pi-ai sends none of them on.
type piBlock struct {
	Type              string          `json:"type"`
	Text              string          `json:"text,omitempty"`
	Thinking          string          `json:"thinking,omitempty"`
	ThinkingSignature string          `json:"thinkingSignature,omitempty"`
	ID                string          `json:"id,omitempty"`
	Name              string          `json:"name,omitempty"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
	GodwitContext     bool            `json:"godwitContext,omitempty"`
	GodwitCommand     *Command        `json:"godwitCommand,omitempty"`
	GodwitMetadata    json.RawMessage `json:"godwitMetadata,omitempty"`
}

var errPiMessage = errors.New("ai: unreadable conversation state")

// IsPiMessage reports whether a saved message is a pi message: they all have
// a timestamp, Genkit's none.
func IsPiMessage(b []byte) bool {
	var m piMessage
	return json.Unmarshal(b, &m) == nil && m.Timestamp != nil
}

// genkitMessage is the Genkit form of pi message b.
func genkitMessage(b []byte) (*message, error) {
	var m piMessage
	if err := json.Unmarshal(b, &m); err != nil || m.Timestamp == nil {
		return nil, errPiMessage
	}
	var blocks []piBlock
	if len(m.Content) > 0 && m.Content[0] == '"' {
		var s string
		if json.Unmarshal(m.Content, &s) != nil {
			return nil, errPiMessage
		}
		blocks = []piBlock{{Type: "text", Text: s}}
	} else if json.Unmarshal(m.Content, &blocks) != nil {
		return nil, errPiMessage
	}
	var parts []*part
	switch m.Role {
	case "user":
		for _, b := range blocks {
			switch {
			case b.GodwitCommand != nil:
				return commandMessage(*b.GodwitCommand), nil
			case b.Type == "text":
				p := textPart(b.Text)
				if b.GodwitContext {
					p.Metadata = map[string]any{contextKey: true}
				}
				parts = append(parts, p)
			}
		}
		return userMessage(parts...), nil
	case "assistant":
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, textPart(b.Text))
			case "thinking":
				parts = append(parts, reasoningPart(b))
			case "toolCall":
				var in any
				if err := exactJSON(cmpRaw(b.Arguments), &in); err != nil || b.ID == "" || b.Name == "" {
					return nil, errPiMessage
				}
				p := toolRequestPart(&toolRequest{Ref: b.ID, Name: b.Name, Input: in})
				if len(b.GodwitMetadata) > 0 && json.Unmarshal(b.GodwitMetadata, &p.Metadata) != nil {
					return nil, errPiMessage
				}
				parts = append(parts, p)
			}
		}
		return modelMessage(parts...), nil
	case "toolResult":
		if m.ToolCallID == "" || m.ToolName == "" {
			return nil, errPiMessage
		}
		var text strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				text.WriteString(b.Text)
			}
		}
		var out any
		if exactJSON([]byte(text.String()), &out) != nil {
			out = text.String() // a refusal pi wrote itself
			if m.IsError {
				out = map[string]any{"error": text.String()}
			}
		}
		return &message{Role: roleTool, Content: []*part{
			toolResponsePart(&toolResponse{Ref: m.ToolCallID, Name: m.ToolName, Output: out})}}, nil
	}
	return nil, errPiMessage
}

// reasoningPart is thinking block b as Genkit's plugins replay it: a
// Responses reasoning item's encrypted content and ID (pi-ai keeps the item
// as the signature), or Anthropic's signature.
func reasoningPart(b piBlock) *part {
	var item struct {
		Type, ID         string
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal([]byte(b.ThinkingSignature), &item) == nil && item.Type == "reasoning" {
		p := newReasoningPart(b.Thinking, []byte(item.EncryptedContent))
		if p.Metadata == nil {
			p.Metadata = map[string]any{}
		}
		p.Metadata["id"] = item.ID
		return p
	}
	var sig []byte
	if b.ThinkingSignature != "" {
		sig = []byte(b.ThinkingSignature)
	}
	return newReasoningPart(b.Thinking, sig)
}

// thinkingSignature is reasoning part p's signature as reasoningPart reads
// it back: a Responses reasoning item (p has its ID) or the signature itself.
func thinkingSignature(p *part) string {
	sig := signature(p)
	if id, _ := p.Metadata["id"].(string); id != "" {
		b, _ := json.Marshal(map[string]any{"type": "reasoning", "id": id, "encrypted_content": sig, "summary": []any{}})
		return string(b)
	}
	return sig
}

// replayed is pi message b as a run on api's model sends it: a reply Genkit
// wrote (piMessages) is another model's to pi-ai, which drops its reasoning,
// so one whose every reasoning signature is api's kind is marked the run's
// model's, and its reasoning goes back as Genkit's plugins replay it.
func replayed(b []byte, api, model string) []byte {
	var m struct {
		API     string
		Content []piBlock
	}
	if json.Unmarshal(b, &m) != nil || m.API != "genkit" {
		return b
	}
	fits := false
	for _, c := range m.Content {
		if c.Type != "thinking" {
			continue
		}
		var item struct{ Type string }
		resp := json.Unmarshal([]byte(c.ThinkingSignature), &item) == nil && item.Type == "reasoning"
		switch api {
		case "openai-responses", "openai-codex-responses":
			fits = resp
		case "anthropic-messages":
			fits = c.ThinkingSignature != "" && !resp
		default:
			fits = false
		}
		if !fits {
			return b
		}
	}
	if !fits {
		return b
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(b, &all) != nil {
		return b
	}
	all["api"], all["model"] = rawJSON(api), rawJSON(model)
	out, err := json.Marshal(all)
	if err != nil {
		return b
	}
	return out
}

// piMessages are the pi messages of Genkit message m: one, or one per tool
// result of a tool message.
func piMessages(m *message) ([][]byte, error) {
	ts := time.Now().UnixMilli()
	var blocks []piBlock
	var out []any
	switch m.Role {
	case roleUser:
		if c := commandOf(m); c != nil {
			blocks = []piBlock{{Type: "text", Text: "/" + c.Skill, GodwitCommand: c}}
		} else {
			for _, p := range m.Content {
				if p.IsText() {
					blocks = append(blocks, piBlock{Type: "text", Text: p.Text, GodwitContext: p.Metadata[contextKey] != nil})
				}
			}
		}
		out = append(out, map[string]any{"role": "user", "content": blocks, "timestamp": ts})
	case roleModel:
		stop := "stop"
		for _, p := range m.Content {
			switch {
			case p.IsReasoning():
				blocks = append(blocks, piBlock{Type: "thinking", Thinking: p.Text, ThinkingSignature: thinkingSignature(p)})
			case p.IsText():
				blocks = append(blocks, piBlock{Type: "text", Text: p.Text})
			case p.IsToolRequest():
				var meta json.RawMessage
				if len(p.Metadata) > 0 {
					meta = rawJSON(p.Metadata)
				}
				blocks = append(blocks, piBlock{Type: "toolCall", ID: p.ToolRequest.Ref, Name: p.ToolRequest.Name, Arguments: rawJSON(p.ToolRequest.Input),
					GodwitMetadata: meta})
				stop = "toolUse"
			}
		}
		// Another engine wrote it: pi-ai replays it as it does another model's.
		out = append(out, map[string]any{"role": "assistant", "content": blocks, "api": "genkit", "provider": "godwit", "model": "",
			"usage":      map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
			"stopReason": stop, "timestamp": ts})
	case roleTool:
		for _, p := range m.Content {
			if !p.IsToolResponse() {
				continue
			}
			o, _ := p.ToolResponse.Output.(map[string]any)
			_, failed := o["error"]
			out = append(out, map[string]any{"role": "toolResult", "toolCallId": p.ToolResponse.Ref, "toolName": p.ToolResponse.Name,
				"content": []piBlock{{Type: "text", Text: string(rawJSON(p.ToolResponse.Output))}}, "isError": failed, "timestamp": ts})
		}
	}
	if len(out) == 0 {
		return nil, errPiMessage
	}
	bs := make([][]byte, len(out))
	for i, o := range out {
		b, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		bs[i] = b
	}
	return bs, nil
}

// exactJSON decodes b keeping large integers exact.
func exactJSON(b []byte, v *any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errPiMessage
	}
	return nil
}

// cmpRaw is b, or an empty object for none.
func cmpRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}

// PiHistory is h, a conversation saved before new ones were saved as pi
// messages (#234), as pi messages, and the seq each seq of h moves to: a tool
// message becomes one per result, so the seqs after it move on; a seq
// between messages moves with the message after it. A message whose pi form
// would show or replay differently fails it. A pi history is itself.
func (h *History) PiHistory() (*History, func(seq int64) int64, error) {
	if h.pi {
		return h, func(seq int64) int64 { return seq }, nil
	}
	n := &History{pi: true}
	var from []int64 // h's seq where each further shift begins
	var by []int64   // the shift from it on
	shift := int64(0)
	for i, m := range h.msgs {
		bs, err := piMessages(m)
		if err != nil {
			return nil, nil, err
		}
		ms := make([]*message, len(bs))
		for k, b := range bs {
			if ms[k], err = genkitMessage(b); err != nil {
				return nil, nil, err
			}
		}
		if !samePi(m, ms) {
			return nil, nil, fmt.Errorf("ai: message %d does not convert to pi messages", h.seqs[i])
		}
		for k := range ms {
			n.msgs, n.raws, n.seqs = append(n.msgs, ms[k]), append(n.raws, bs[k]), append(n.seqs, h.seqs[i]+shift+int64(k))
		}
		if len(ms) > 1 {
			shift += int64(len(ms) - 1)
			from, by = append(from, h.seqs[i]+1), append(by, shift)
		}
	}
	n.next = h.next + shift
	return n, func(seq int64) int64 {
		s := int64(0)
		for k, f := range from {
			if seq >= f {
				s = by[k]
			}
		}
		return seq + s
	}, nil
}

// samePi reports whether ms, the Genkit form of m's pi messages, show and
// replay as m does: its text, context, command, reasoning and signatures,
// calls (with what Genkit keeps on them) and results, and no other part.
func samePi(m *message, ms []*message) bool {
	view := func(msgs ...*message) string {
		var d Message
		var extra []any
		for _, x := range display(msgs) {
			d.Role, d.Command = x.Role, cmp.Or(d.Command, x.Command)
			d.Text, d.Reasoning = d.Text+x.Text, d.Reasoning+x.Reasoning
			d.Calls, d.Results = append(d.Calls, x.Calls...), append(d.Results, x.Results...)
		}
		for _, x := range msgs {
			for _, p := range x.Content {
				switch {
				case p.IsText() && p.Metadata[contextKey] != nil:
					extra = append(extra, p.Text)
				case p.IsReasoning():
					extra = append(extra, thinkingSignature(p))
				case p.IsToolRequest():
					extra = append(extra, p.Metadata)
				case !p.IsText() && !p.IsToolResponse(): // a part pi messages have no room for
					extra = append(extra, p)
				}
			}
		}
		b, _ := json.Marshal([]any{d, extra})
		return string(b)
	}
	return view(m) == view(ms...)
}

// signature is a reasoning part's encrypted content: bytes as Genkit made
// them, or base64 once the state went through JSON.
func signature(p *part) string {
	switch s := p.Metadata["signature"].(type) {
	case []byte:
		return string(s)
	case string:
		b, _ := base64.StdEncoding.DecodeString(s)
		return string(b)
	}
	return ""
}

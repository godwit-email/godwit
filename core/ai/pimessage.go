package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	gai "github.com/firebase/genkit/go/ai"
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

// isPiMessage reports whether a saved message is a pi message: they all have
// a timestamp, Genkit's none.
func isPiMessage(b []byte) bool {
	var m piMessage
	return json.Unmarshal(b, &m) == nil && m.Timestamp != nil
}

// genkitMessage is the Genkit form of pi message b.
func genkitMessage(b []byte) (*gai.Message, error) {
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
	var parts []*gai.Part
	switch m.Role {
	case "user":
		for _, b := range blocks {
			switch {
			case b.GodwitCommand != nil:
				return commandMessage(*b.GodwitCommand), nil
			case b.Type == "text":
				p := gai.NewTextPart(b.Text)
				if b.GodwitContext {
					p.Metadata = map[string]any{contextKey: true}
				}
				parts = append(parts, p)
			}
		}
		return gai.NewUserMessage(parts...), nil
	case "assistant":
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, gai.NewTextPart(b.Text))
			case "thinking":
				parts = append(parts, reasoningPart(b))
			case "toolCall":
				var in any
				if err := exactJSON(cmpRaw(b.Arguments), &in); err != nil || b.ID == "" || b.Name == "" {
					return nil, errPiMessage
				}
				p := gai.NewToolRequestPart(&gai.ToolRequest{Ref: b.ID, Name: b.Name, Input: in})
				if len(b.GodwitMetadata) > 0 { // through Genkit's own decoding, which lifts an interrupt out of it
					w, _ := json.Marshal(map[string]any{"toolRequest": p.ToolRequest, "metadata": b.GodwitMetadata})
					p = &gai.Part{}
					if json.Unmarshal(w, p) != nil {
						return nil, errPiMessage
					}
					p.ToolRequest.Input = in
				}
				parts = append(parts, p)
			}
		}
		return gai.NewModelMessage(parts...), nil
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
		return &gai.Message{Role: gai.RoleTool, Content: []*gai.Part{
			gai.NewToolResponsePart(&gai.ToolResponse{Ref: m.ToolCallID, Name: m.ToolName, Output: out})}}, nil
	}
	return nil, errPiMessage
}

// reasoningPart is thinking block b as Genkit's plugins replay it: a
// Responses reasoning item's encrypted content and ID (pi-ai keeps the item
// as the signature), or Anthropic's signature.
func reasoningPart(b piBlock) *gai.Part {
	var item struct {
		Type, ID         string
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal([]byte(b.ThinkingSignature), &item) == nil && item.Type == "reasoning" {
		p := gai.NewReasoningPart(b.Thinking, []byte(item.EncryptedContent))
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
	return gai.NewReasoningPart(b.Thinking, sig)
}

// piMessages are the pi messages of Genkit message m: one, or one per tool
// result of a tool message.
func piMessages(m *gai.Message) ([][]byte, error) {
	ts := time.Now().UnixMilli()
	var blocks []piBlock
	var out []any
	switch m.Role {
	case gai.RoleUser:
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
	case gai.RoleModel:
		stop := "stop"
		for _, p := range m.Content {
			switch {
			case p.IsReasoning():
				blocks = append(blocks, piBlock{Type: "thinking", Thinking: p.Text})
			case p.IsText():
				blocks = append(blocks, piBlock{Type: "text", Text: p.Text})
			case p.IsToolRequest():
				var wire struct{ Metadata json.RawMessage } // Genkit folds an interrupt into it
				if b, err := json.Marshal(p); err == nil {
					json.Unmarshal(b, &wire)
				}
				blocks = append(blocks, piBlock{Type: "toolCall", ID: p.ToolRequest.Ref, Name: p.ToolRequest.Name, Arguments: rawJSON(p.ToolRequest.Input),
					GodwitMetadata: wire.Metadata})
				stop = "toolUse"
			}
		}
		// Another engine wrote it: pi-ai replays it as it does another model's.
		out = append(out, map[string]any{"role": "assistant", "content": blocks, "api": "genkit", "provider": "godwit", "model": "",
			"usage":      map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
			"stopReason": stop, "timestamp": ts})
	case gai.RoleTool:
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

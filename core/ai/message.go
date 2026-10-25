package ai

import (
	"bytes"
	"encoding/json"
)

// message is a conversation message in the form History works on, encoded as
// Genkit encoded its messages: conversations saved before #234 are still
// read in it, and pi messages convert to and from it (pimessage.go).
type message struct {
	Content  []*part        `json:"content,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Role     role           `json:"role,omitempty"`
}

type role string

const (
	roleUser  role = "user"
	roleModel role = "model"
	roleTool  role = "tool"
)

// part is one part of a message: text, reasoning, a tool request or a tool
// response. A part of another kind (media, data) is kept as it was saved.
type part struct {
	Text         string // a text or reasoning part's
	reasoning    bool
	ToolRequest  *toolRequest
	ToolResponse *toolResponse
	Metadata     map[string]any
	other        json.RawMessage
}

type toolRequest struct {
	Input   any    `json:"input,omitempty"`
	Name    string `json:"name,omitempty"`
	Partial bool   `json:"partial,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

type toolResponse struct {
	Content json.RawMessage `json:"content,omitempty"`
	Name    string          `json:"name,omitempty"`
	Output  any             `json:"output,omitempty"`
	Ref     string          `json:"ref,omitempty"`
}

func textPart(s string) *part { return &part{Text: s} }

// reasoningPart is reasoning text with its signature, if any.
func newReasoningPart(text string, sig []byte) *part {
	p := &part{Text: text, reasoning: true}
	if len(sig) > 0 {
		p.Metadata = map[string]any{"signature": sig}
	}
	return p
}

func toolRequestPart(r *toolRequest) *part   { return &part{ToolRequest: r} }
func toolResponsePart(r *toolResponse) *part { return &part{ToolResponse: r} }

func userMessage(parts ...*part) *message  { return &message{Role: roleUser, Content: parts} }
func modelMessage(parts ...*part) *message { return &message{Role: roleModel, Content: parts} }
func userText(s string) *message           { return userMessage(textPart(s)) }
func modelText(s string) *message          { return modelMessage(textPart(s)) }

func (p *part) IsText() bool {
	return !p.reasoning && p.ToolRequest == nil && p.ToolResponse == nil && p.other == nil
}
func (p *part) IsReasoning() bool    { return p.reasoning }
func (p *part) IsToolRequest() bool  { return p.ToolRequest != nil }
func (p *part) IsToolResponse() bool { return p.ToolResponse != nil }

// IsInterrupt reports whether p is a tool request a run interrupted and that
// waits for its result, marked as Genkit marked one.
func (p *part) IsInterrupt() bool {
	if !p.IsToolRequest() {
		return false
	}
	switch v := p.Metadata["interrupt"].(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	}
	return true
}

// responseFor is the response to tool request p.
func responseFor(p *part, out any) *part {
	return toolResponsePart(&toolResponse{Name: p.ToolRequest.Name, Ref: p.ToolRequest.Ref, Output: out})
}

func (p *part) MarshalJSON() ([]byte, error) {
	switch {
	case p.other != nil:
		return p.other, nil
	case p.reasoning:
		return json.Marshal(struct {
			Metadata  map[string]any `json:"metadata,omitempty"`
			Reasoning string         `json:"reasoning"`
		}{p.Metadata, p.Text})
	case p.ToolRequest != nil:
		return json.Marshal(struct {
			Metadata    map[string]any `json:"metadata,omitempty"`
			ToolRequest *toolRequest   `json:"toolRequest"`
		}{p.Metadata, p.ToolRequest})
	case p.ToolResponse != nil:
		return json.Marshal(struct {
			Metadata     map[string]any `json:"metadata,omitempty"`
			ToolResponse *toolResponse  `json:"toolResponse"`
		}{p.Metadata, p.ToolResponse})
	}
	return json.Marshal(struct {
		Metadata map[string]any `json:"metadata,omitempty"`
		Text     string         `json:"text,omitempty"`
	}{p.Metadata, p.Text})
}

// UnmarshalJSON reads a part as Genkit does, its kind from the keys it has;
// tool inputs and outputs are decoded exactly (large integers stay whole).
func (p *part) UnmarshalJSON(b []byte) error {
	var w struct {
		Text        string
		Reasoning   *string
		ToolRequest *struct {
			Input   json.RawMessage
			Name    string
			Partial bool
			Ref     string
		}
		ToolResponse *struct {
			Content json.RawMessage
			Name    string
			Output  json.RawMessage
			Ref     string
		}
		Metadata                      map[string]any
		Media, Data, Custom, Resource json.RawMessage
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*p = part{Metadata: w.Metadata}
	switch {
	case w.Media != nil, w.Resource != nil, w.Custom != nil:
		p.other = bytes.Clone(b)
	case w.ToolRequest != nil:
		p.ToolRequest = &toolRequest{Name: w.ToolRequest.Name, Partial: w.ToolRequest.Partial, Ref: w.ToolRequest.Ref}
		return exactOrNone(w.ToolRequest.Input, &p.ToolRequest.Input)
	case w.ToolResponse != nil:
		p.ToolResponse = &toolResponse{Content: w.ToolResponse.Content, Name: w.ToolResponse.Name, Ref: w.ToolResponse.Ref}
		return exactOrNone(w.ToolResponse.Output, &p.ToolResponse.Output)
	case w.Reasoning != nil:
		p.Text, p.reasoning = *w.Reasoning, true
	case w.Data != nil && w.Text == "":
		p.other = bytes.Clone(b)
	default:
		p.Text = w.Text
	}
	return nil
}

// exactOrNone decodes b exactly into v; nothing for none or null.
func exactOrNone(b json.RawMessage, v *any) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return exactJSON(b, v)
}

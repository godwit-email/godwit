// Package ai talks to model services. It must not import core.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ModelRef names a model as "service/model"; the first "/" ends the service name.
type ModelRef struct{ Service, Model string }

func ParseModelRef(s string) (ModelRef, error) {
	svc, model, ok := strings.Cut(s, "/")
	if !ok || svc == "" || model == "" {
		return ModelRef{}, fmt.Errorf("invalid model reference %q: want service/model", s)
	}
	return ModelRef{svc, model}, nil
}

// ResolvedModel is a model reference resolved against a stored service.
type ResolvedModel struct {
	ServiceID, Version           int64
	Kind, BaseURL, Model, APIKey string
}

// Message is a conversation message for display; it is never used to resume.
type Message struct {
	Role    string // user, model or tool
	Text    string
	Calls   []ToolCall
	Results []ToolResult
}

type ToolCall struct {
	ID, Name string
	Args     json.RawMessage
}

type ToolResult struct {
	CallID, Name string
	Output       json.RawMessage
	Error        string
}

// Decision is a permission gate's verdict on one tool call.
type Decision string

const (
	Allow   Decision = "allow"
	Approve Decision = "approve"
	Deny    Decision = "deny"
)

// ToolSpec is a tool offered to the model for one Run. Gate, if set, decides
// each call first: Deny (or a Gate error) gives an error result, Approve
// interrupts, Allow goes on. A NeedsApproval tool's Run is never called: its
// calls interrupt the run instead.
type ToolSpec struct {
	Name, Description     string
	Schema                map[string]any
	Strict, NeedsApproval bool
	Gate                  func(ctx context.Context, args json.RawMessage) (Decision, error)
	Run                   func(ctx context.Context, args json.RawMessage) (any, error)
}

type Code string

const (
	CodeCanceled     Code = "canceled"
	CodeUnauthorized Code = "unauthorized"
	CodeRateLimited  Code = "rate_limited"
	CodeUnavailable  Code = "unavailable"
	CodeBadResponse  Code = "bad_response"
	CodeInternal     Code = "internal"
	// CodeStale: the model service changed or was removed since the model was
	// resolved, or while the run was in flight.
	CodeStale Code = "stale"
	// CodeIncomplete: the service ended the reply abnormally (cut off at the
	// output limit, filtered, failed part-way); nothing of it is kept.
	CodeIncomplete Code = "incomplete"
)

// Error is a run failure. Msg never holds a key or a request or response body.
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Is makes errors.Is(err, context.Canceled) hold for a canceled run.
func (e *Error) Is(target error) bool { return e.Code == CodeCanceled && target == context.Canceled }

// Event kinds: text, tool_call, tool_result, interrupted (every call of the round
// awaiting approval, in call order), done, error. Exactly one of the last three
// ends the stream.
type Event struct {
	Kind       string
	Text       string
	Call       *ToolCall
	Result     *ToolResult
	Interrupts []ToolCall
	Err        *Error
	Model      string // context only: the "slug/model" the run uses
}

// Request is one run. State is the previous run's opaque state (empty = new
// conversation); Responses answer the calls that interrupted it.
type Request struct {
	Model  ResolvedModel
	System string
	State  []byte
	Prompt string
	// Context goes to the model before Prompt, in the same user turn, and
	// stays in the state; Messages never show it (quoted mail, for example).
	Context   string
	Tools     []ToolSpec
	Responses []ToolResult
	MaxTurns  int
}

type Result struct {
	State      []byte
	Messages   []Message
	Interrupts []ToolCall
	Err        *Error
}

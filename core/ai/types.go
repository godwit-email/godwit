// Package ai talks to model services. It must not import core.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	gai "github.com/firebase/genkit/go/ai"
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

// ResolvedModel is a model reference resolved against a stored service, or
// a plugin's model: Client, when set, is its own HTTP client, and the engine
// does not check its version.
type ResolvedModel struct {
	ServiceID, Version           int64
	Kind, BaseURL, Model, APIKey string
	Client                       *http.Client
}

// Message is a conversation message for display; it is never used to resume.
// Seq is its place in its History; Command is set for a command (its Text
// is then empty).
type Message struct {
	Seq       int64
	Role      string // user, model or tool
	Text      string
	Command   *Command
	Reasoning string // the model's readable reasoning, if it showed any
	Calls     []ToolCall
	Results   []ToolResult
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

const DetailChatGPTUsage = "chatgpt_usage"

// DetailChatGPTEligibility on a CodeUnauthorized Error: the ChatGPT account
// signed in is not eligible for subscription sharing, so its plan can't be used.
const DetailChatGPTEligibility = "chatgpt_not_eligible"

// Error is a run failure. Msg never holds a key or a request or response body.
type Error struct {
	Code   Code
	Msg    string
	Detail string
}

// Error tolerates a nil receiver: fmt calls it on an Event's nil Err, and the
// recovered nil dereference is a hardware fault Go 1.27 can't take safely on
// some Windows hosts (golang/go#81238).
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Msg
}

// Is makes errors.Is(err, context.Canceled) hold for a canceled run.
func (e *Error) Is(target error) bool { return e.Code == CodeCanceled && target == context.Canceled }

// Event kinds: text, reasoning (Request.Reasoning only), tool_call, tool_result,
// interrupted (every call of the round awaiting approval, in call order), done,
// error. Exactly one of the last three ends the stream.
type Event struct {
	Kind       string
	Text       string
	Call       *ToolCall
	Result     *ToolResult
	Interrupts []ToolCall
	Err        *Error
	Model      string // context only: the "slug/model" the run uses
	// Translations replaces original text nodes in a reader body; markup is never generated.
	Translations map[string]string `json:",omitempty"`
}

// Request is one run. History is what it sends of a conversation (nil: a
// one-off on Prompt alone), with its question asked (Sent.Ask) unless the
// run resumes it: then Responses answer the calls that interrupted it.
type Request struct {
	Model     ResolvedModel
	System    string
	History   *Sent
	Prompt    string // a one-off run's question; a History asks its own
	Tools     []ToolSpec
	Responses []ToolResult
	MaxTurns  int
	// Reasoning streams the model's readable reasoning as reasoning events.
	Reasoning bool
	// Args, if set, rewrites each tool call's arguments before they are
	// validated and the tool runs; the state keeps what the model wrote. An
	// error refuses the call: the model gets it as the call's result.
	Args func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
	// Effort is the reasoning effort asked of the model: "low", "medium" or
	// "high"; "" asks for none (models that do not reason refuse it).
	Effort string
	// Pi runs the request on pi (Labs: Settings.PiEngine) when pi covers it
	// (piCovers); anything else runs on Genkit as without it.
	Pi bool
}

// Result is how a run ended. A successful run of a History is saved through
// its Sent's Restore; Messages are what it ended with, for display.
type Result struct {
	out        []*gai.Message // the run's whole view as it ended
	raws       [][]byte       // a run on pi: the pi messages of out it added (nil for the others)
	Messages   []Message
	Interrupts []ToolCall
	Err        *Error
	Usage      Usage
}

// Usage is the tokens a run's model calls took, as the service reported them:
// Input and Output summed over its calls, Last the last call's input (the
// history the model was last shown). 0 where the service reports none.
type Usage struct{ Input, Output, Last int }

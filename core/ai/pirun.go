package ai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	gai "github.com/firebase/genkit/go/ai"

	"github.com/godwit-email/godwit/core/ai/pi"
)

// PiConversation reports whether a run of history h on m goes on pi when
// Request.Pi asks: pi speaks m's protocol and h is new or was started on
// pi. Such a run offers no tool that needs an approval and asks no question
// (#233).
func PiConversation(m ResolvedModel, h *History) bool { return piAPI(m) != "" && h.Pi() }

// piCovers reports whether pi runs req: a one-off prompt to a stored model
// service of any kind (#229, #230) or a plugin's model, whose client prepares
// and sends each request (#231), with no tools; or a question asked of a
// PiConversation whose tools need no approval (#232). Each migration step
// widens it.
func piCovers(req Request) bool {
	switch {
	case piAPI(req.Model) == "" || len(req.Responses) > 0:
		return false
	case req.History == nil:
		return req.Prompt != "" && len(req.Tools) == 0
	}
	return req.Prompt == "" && req.History.asked() && req.History.h.Pi() &&
		!slices.ContainsFunc(req.Tools, func(s ToolSpec) bool { return s.NeedsApproval })
}

// piAPI is the pi-ai API that speaks m's protocol, "" for a kind it does not know.
func piAPI(m ResolvedModel) string {
	switch m.Kind {
	case KindOpenAI:
		return "openai-completions"
	case KindAnthropic:
		return "anthropic-messages"
	case KindResponses:
		if strings.HasSuffix(m.BaseURL+"/", codexBase) {
			return "openai-codex-responses"
		}
		return "openai-responses"
	}
	return ""
}

// runPi is run on pi, for a request piCovers. A one-off's Result carries no
// Messages: nothing resumes or shows it.
func (r *runner) runPi(req Request) Result {
	if req.History != nil {
		if err := checkTools(req.Tools); err != nil {
			return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
		}
	}
	// shortcut: the run takes a Genkit instance for its lifecycle (the stale
	// check, Forget and Close end it, keys are redacted); give pi its own when
	// Genkit goes (#236).
	in, err := r.e.instance(r.ctx, req.Model)
	if errors.Is(err, errStale) {
		return Result{Err: &Error{Code: CodeStale, Msg: "the model service changed; try again"}}
	}
	if err != nil {
		return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
	}
	defer r.e.release(in)
	ctx, cancel := context.WithCancelCause(r.ctx)
	defer cancel(nil)
	stop := context.AfterFunc(in.ctx, func() { cancel(context.Cause(in.ctx)) })
	defer stop()
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()

	preq := pi.Request{API: piAPI(req.Model), BaseURL: req.Model.BaseURL, Model: req.Model.Model, APIKey: req.Model.APIKey,
		System: req.System, Prompt: req.Prompt, Effort: req.Effort}
	if req.Model.Kind == KindAnthropic { // max_tokens is required: Genkit's default, raised by the thinking budget (effortConfig)
		preq.BaseURL = AnthropicBase(req.Model.BaseURL)
		preq.ThinkingBudget = thinkingBudget[req.Effort]
		preq.MaxTokens = preq.ThinkingBudget + 4096
	}
	emit := func(kind, text string) {
		if kind == "text" || r.reasoning {
			r.send(Event{Kind: kind, Text: text})
		}
	}
	if req.History != nil {
		return r.agentPi(ctx, req, preq, emit)
	}
	res, err := pi.Generate(ctx, cmp.Or(req.Model.Client, r.e.client), preq, emit)
	if err != nil {
		return r.piFailed(ctx, err)
	}
	r.mu.Lock()
	r.usage = Usage{Input: res.Input, Output: res.Output, Last: res.Input}
	r.mu.Unlock()
	if e := piError(res, preq.API == "openai-codex-responses"); e != nil {
		return Result{Err: e}
	}
	return Result{}
}

func (r *runner) piFailed(ctx context.Context, err error) Result {
	if ctx.Err() != nil {
		return Result{Err: classify(ctx, err)}
	}
	return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
}

// defaultMaxTurns is Genkit's, for a Request without MaxTurns.
const defaultMaxTurns = 5

// errNeedsApproval refuses a call a gate sends for approval: runs on pi take
// none yet (#233).
var errNeedsApproval = errors.New("this call needs an approval, which this conversation cannot take yet")

// agentPi runs the question req.History asks on pi-agent-core's loop; the
// tools run here, as on Genkit.
func (r *runner) agentPi(ctx context.Context, req Request, preq pi.Request, emit func(kind, text string)) Result {
	s := req.History
	msgs := make([]json.RawMessage, len(s.msgs))
	for k, src := range s.src {
		if src >= 0 {
			msgs[k] = s.h.raws[src]
			continue
		}
		bs, err := piMessages(s.msgs[k]) // the note, the question
		if err != nil {
			return Result{Err: &Error{Code: CodeInternal, Msg: err.Error()}}
		}
		msgs[k] = bs[0]
	}
	specs := map[string]ToolSpec{}
	tools := make([]pi.Tool, 0, len(req.Tools))
	for _, t := range req.Tools {
		specs[t.Name] = t
		schema, desc := r.schema(t)
		if t.Strict && strings.HasSuffix(preq.API, "responses") { // as the Genkit plugin sends it
			schema = strictSchema(schema)
		}
		tools = append(tools, pi.Tool{Name: t.Name, Description: desc, Parameters: schema, Strict: t.Strict})
	}
	hooks := pi.Hooks{
		Emit: emit,
		Tool: func(ctx context.Context, id, name string, args json.RawMessage) (json.RawMessage, string) {
			res := r.runTool(context.WithValue(ctx, callIDKey{}, id), specs[name], id, args)
			return res.Output, res.Error
		},
		Refused: func(id, name string, args json.RawMessage, msg string) {
			r.send(Event{Kind: "tool_call", Call: &ToolCall{ID: id, Name: name, Args: args}})
			r.send(Event{Kind: "tool_result", Result: &ToolResult{CallID: id, Name: name, Error: r.redact(msg)}})
		},
	}
	if r.args != nil {
		hooks.Args = func(args json.RawMessage) (json.RawMessage, error) {
			out, err := r.args(ctx, args)
			if err != nil {
				return nil, errors.New(r.redact(err.Error()))
			}
			return out, nil
		}
	}
	res, err := pi.Agent(ctx, cmp.Or(req.Model.Client, r.e.client), pi.AgentRequest{Request: preq, Messages: msgs, Tools: tools,
		MaxTurns: cmp.Or(req.MaxTurns, defaultMaxTurns), LimitNote: limitNote}, hooks)
	if err != nil {
		return r.piFailed(ctx, err)
	}
	r.mu.Lock()
	r.usage = Usage{Input: res.Input, Output: res.Output, Last: res.Last}
	r.mu.Unlock()
	if res.Limited && (res.StopReason != "stop" || res.Calls) {
		return Result{Err: errLimit()}
	}
	if e := piError(res.Result, preq.API == "openai-codex-responses"); e != nil {
		return Result{Err: e} // the history stays as it was: a failed run leaves no half turn behind
	}
	// The view as sent, then what the turn added from its question on.
	out := slices.Clone(s.msgs[:len(s.msgs)-1])
	raws := make([][]byte, len(out), len(out)+len(res.Messages))
	for _, b := range res.Messages {
		m, err := genkitMessage(b)
		if err != nil {
			return Result{Err: &Error{Code: CodeBadResponse, Msg: "the model service returned an unusable response"}}
		}
		if len(m.Content) > 0 { // an empty reply carries nothing, as on Genkit
			out, raws = append(out, m), append(raws, b)
		}
	}
	if len(out) < len(s.msgs) || out[len(s.msgs)-1].Role != gai.RoleUser {
		return Result{Err: &Error{Code: CodeInternal, Msg: "ai: the turn lost its question"}}
	}
	return Result{out: out, raws: raws, Messages: display(out)}
}

// runTool runs one call of a run on pi as tool does on Genkit, sending its
// tool_call and tool_result events; a call its gate sends for approval is
// refused.
func (r *runner) runTool(ctx context.Context, s ToolSpec, id string, args json.RawMessage) ToolResult {
	r.send(Event{Kind: "tool_call", Call: &ToolCall{ID: id, Name: s.Name, Args: args}})
	d := Allow
	var err error
	if s.Gate != nil {
		if d, err = s.Gate(ctx, args); err == nil && d != Allow && d != Approve {
			err = errDenied
		}
	}
	var out any
	switch {
	case err != nil:
	case d == Approve || s.NeedsApproval || s.Run == nil:
		err = errNeedsApproval
	default:
		if out, err = s.Run(ctx, args); errors.Is(err, ErrInterrupt) {
			err = errNeedsApproval
		}
	}
	res := ToolResult{CallID: id, Name: s.Name}
	if err != nil {
		res.Error = r.redact(err.Error())
	} else {
		res.Output = rawJSON(out)
	}
	r.send(Event{Kind: "tool_result", Result: &res})
	return res
}

// piError is the Error of how a pi reply ended, nil for a whole one. The
// messages are classify's, and the subscription codes respError's, so a run
// reads the same on either engine. codex: ChatGPT's Codex backend.
func piError(res pi.Result, codex bool) *Error {
	switch res.StopReason {
	case "stop":
		return nil
	case "length":
		return incomplete(gai.FinishReasonLength)
	case "blocked":
		return incomplete(gai.FinishReasonBlocked)
	case "error":
	default:
		return incomplete(gai.FinishReasonOther)
	}
	usage := &Error{Code: CodeRateLimited, Msg: "the ChatGPT plan's usage limit is reached", Detail: DetailChatGPTUsage}
	switch res.Code {
	case "subscription_sharing_user_not_eligible":
		return &Error{Code: CodeUnauthorized, Msg: "the ChatGPT account is not eligible for subscription sharing", Detail: DetailChatGPTEligibility}
	case "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable":
		return usage
	case "usage_limit_reached", "usage_not_included", "rate_limit_exceeded":
		if codex {
			return usage
		}
	}
	if codex && res.Status == http.StatusTooManyRequests {
		return usage
	}
	switch s := res.Status; {
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return &Error{Code: CodeUnauthorized, Msg: "the model service rejected the API key"}
	case s == http.StatusTooManyRequests:
		return &Error{Code: CodeRateLimited, Msg: "the model service is rate limiting requests"}
	case s == http.StatusRequestTimeout || s >= 500:
		return &Error{Code: CodeUnavailable, Msg: "the model service is unavailable"}
	case s == 0 && res.Transport != nil:
		return &Error{Code: CodeUnavailable, Msg: "could not reach the model service"}
	}
	return &Error{Code: CodeBadResponse, Msg: "the model service returned an unusable response"}
}

// checkTools refuses invalid or duplicate tool names and tools that cannot run.
func checkTools(tools []ToolSpec) error {
	seen := map[string]bool{}
	for _, s := range tools {
		if !toolName.MatchString(s.Name) || seen[s.Name] {
			return fmt.Errorf("ai: invalid or duplicate tool name %q", s.Name)
		}
		if s.Run == nil && !s.NeedsApproval && s.Gate == nil {
			return fmt.Errorf("ai: tool %q has no Run", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

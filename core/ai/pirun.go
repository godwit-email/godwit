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
	"sync"

	"github.com/godwit-email/godwit/core/ai/pi"
)

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

// runPi runs req, checked, on pi. A one-off's Result carries its prompt and
// reply as Messages (an Assist skill reads it). The run ends with its model
// service's instance: a changed or removed service sends nothing more.
func (r *runner) runPi(req Request) Result {
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
	if req.Model.Kind == KindAnthropic { // max_tokens is required: Genkit's default, raised by the thinking budget
		preq.BaseURL = AnthropicBase(req.Model.BaseURL)
		preq.ThinkingBudget = thinkingBudget[req.Effort]
		preq.MaxTokens = preq.ThinkingBudget + 4096
	}
	var reply strings.Builder
	emit := func(kind, text string) {
		if kind == "text" && req.History == nil {
			reply.WriteString(text)
		}
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
	return Result{Messages: []Message{{Role: "user", Text: req.Prompt}, {Role: "model", Text: reply.String()}}}
}

func (r *runner) piFailed(ctx context.Context, err error) Result {
	if ctx.Err() != nil {
		return Result{Err: classify(ctx)}
	}
	return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
}

// defaultMaxTurns is Genkit's, for a Request without MaxTurns.
const defaultMaxTurns = 5

// errNeedsUser refuses a call held back with an interrupted round that needs
// the user once it runs: the round was answered without it.
var errNeedsUser = errors.New("this call now needs the user's approval or answer and did not run; make it again")

// gated is what a call's gate decided before its round ran: err refuses it.
type gated struct{ err error }

// gate decides a call of s: hold, it waits for the user.
func gate(ctx context.Context, s ToolSpec, args json.RawMessage) (g gated, hold bool) {
	d := Allow
	if s.Gate != nil {
		if d, g.err = s.Gate(ctx, args); g.err == nil && d != Allow && d != Approve {
			g.err = errDenied
		}
	}
	return g, g.err == nil && (d == Approve || s.NeedsApproval)
}

// agentPi runs the question req.History asks on pi-agent-core's loop, or
// resumes the round a run interrupted (req.Responses; Genkit's runs too); the
// tools run here. A round with a call that needs the user runs none of its
// calls: the run ends interrupted, its state as an interrupted Genkit run
// left one, the held calls marked interrupted and the calls held back with
// them open.
func (r *runner) agentPi(ctx context.Context, req Request, preq pi.Request, emit func(kind, text string)) Result {
	s := req.History
	areq := pi.AgentRequest{Request: preq, Tools: make([]pi.Tool, 0, len(req.Tools)), MaxTurns: cmp.Or(req.MaxTurns, defaultMaxTurns), LimitNote: limitNote}
	if len(req.Responses) > 0 {
		var err error
		if areq.Answers, err = piAnswers(s.msgs, req.Responses, r.redact); err != nil {
			return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
		}
	}
	msgs := make([]json.RawMessage, 0, len(s.msgs))
	for k, src := range s.src {
		if src >= 0 && s.h.pi {
			msgs = append(msgs, replayed(s.h.raws[src], preq.API, preq.Model))
			continue
		}
		// The note, the question, or a message of a conversation that did not
		// convert to pi messages (#234): sent as what of it pi messages carry.
		bs, err := piMessages(s.msgs[k])
		if err != nil {
			return Result{Err: &Error{Code: CodeInternal, Msg: err.Error()}}
		}
		for _, b := range bs {
			msgs = append(msgs, replayed(b, preq.API, preq.Model))
		}
	}
	areq.Messages = msgs
	specs := map[string]ToolSpec{}
	for _, t := range req.Tools {
		specs[t.Name] = t
		schema, desc := r.schema(t)
		if t.Strict && strings.HasSuffix(preq.API, "responses") { // as the Genkit plugin sends it
			schema = strictSchema(schema)
		}
		areq.Tools = append(areq.Tools, pi.Tool{Name: t.Name, Description: desc, Parameters: schema, Strict: t.Strict})
	}
	var gates sync.Map // call ID -> gated
	// A round's calls are all gated before any runs; results bind to call
	// IDs, so a round that repeats one is unusable and none of it runs.
	var roundMu sync.Mutex
	round, repeated := map[string]bool{}, false
	hooks := pi.Hooks{
		Emit: emit,
		Gate: func(ctx context.Context, id, name string, args json.RawMessage) bool {
			ref, _, _ := strings.Cut(id, "|") // a Responses call's ID carries its item's after the call ID
			roundMu.Lock()
			dup := round[ref] || repeated
			round[ref], repeated = true, dup
			roundMu.Unlock()
			if dup {
				return true
			}
			g, hold := gate(context.WithValue(ctx, callIDKey{}, id), specs[name], args)
			if hold {
				r.send(Event{Kind: "tool_call", Call: &ToolCall{ID: id, Name: name, Args: args}})
				return true
			}
			gates.Store(id, g)
			return false
		},
		Tool: func(ctx context.Context, id, name string, args json.RawMessage) (json.RawMessage, string, bool) {
			roundMu.Lock()
			clear(round) // the round's gating is over
			roundMu.Unlock()
			var g *gated
			if v, ok := gates.LoadAndDelete(id); ok {
				g = new(v.(gated))
			}
			res, interrupt := r.runTool(context.WithValue(ctx, callIDKey{}, id), specs[name], id, args, g)
			return res.Output, res.Error, interrupt
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
	res, err := pi.Agent(ctx, cmp.Or(req.Model.Client, r.e.client), areq, hooks)
	if err != nil {
		return r.piFailed(ctx, err)
	}
	r.mu.Lock()
	r.usage = Usage{Input: res.Input, Output: res.Output, Last: res.Last}
	r.mu.Unlock()
	if repeated {
		return Result{Err: &Error{Code: CodeBadResponse, Msg: "the model repeated a tool call ID"}}
	}
	if res.Limited && (res.StopReason != "stop" || res.Calls) {
		return Result{Err: errLimit()}
	}
	if len(res.Held) > 0 {
		if res.Messages, err = heldRound(res.Messages, res.Held, res.Deferred); err != nil {
			return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
		}
	} else if e := piError(res.Result, preq.API == "openai-codex-responses"); e != nil {
		return Result{Err: e} // the history stays as it was: a failed run leaves no half turn behind
	}
	// The view as sent, then what the turn added from its question on (a
	// resume: from the held round's results on).
	out := slices.Clone(s.msgs)
	if s.asked() {
		out = out[:len(out)-1]
	}
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
	if s.asked() && (len(out) < len(s.msgs) || out[len(s.msgs)-1].Role != roleUser) {
		return Result{Err: &Error{Code: CodeInternal, Msg: "ai: the turn lost its question"}}
	}
	end := Result{out: out, raws: raws, Messages: display(out)}
	if len(res.Held) > 0 {
		for _, p := range out[len(out)-1].Content {
			if p.IsInterrupt() {
				end.Interrupts = append(end.Interrupts, toolCall(p.ToolRequest))
			}
		}
	}
	return end
}

// heldRound is the messages of a turn that ended at a held round as an
// interrupted Genkit run leaves them: they end with the round, its held calls
// marked interrupted, each call that ran or was refused carrying its result
// (pendingOutput) and those held back with them left open.
func heldRound(msgs []json.RawMessage, held, deferred []string) ([]json.RawMessage, error) {
	k := len(msgs) - 1
	for ; k >= 0; k-- {
		var m piMessage
		if json.Unmarshal(msgs[k], &m) == nil && m.Role == "assistant" {
			break
		}
	}
	if k < 0 {
		return nil, errors.New("ai: the held round is missing")
	}
	meta := map[string]json.RawMessage{}
	for _, id := range held {
		meta[id] = json.RawMessage(`{"interrupt":true}`)
	}
	for _, b := range msgs[k+1:] {
		var m piMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		if m.Role != "toolResult" || slices.Contains(held, m.ToolCallID) || slices.Contains(deferred, m.ToolCallID) {
			continue
		}
		g, err := genkitMessage(b)
		if err != nil {
			return nil, err
		}
		if meta[m.ToolCallID], err = json.Marshal(map[string]any{"pendingOutput": g.Content[0].ToolResponse.Output}); err != nil {
			return nil, err
		}
	}
	var round map[string]json.RawMessage // the fields Godwit does not read stay as pi wrote them
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(msgs[k], &round); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(round["content"], &blocks); err != nil {
		return nil, err
	}
	for _, b := range blocks {
		var typ, id string
		json.Unmarshal(b["type"], &typ)
		json.Unmarshal(b["id"], &id)
		if typ == "toolCall" && meta[id] != nil {
			b["godwitMetadata"] = meta[id]
		}
	}
	var err error
	if round["content"], err = json.Marshal(blocks); err != nil {
		return nil, err
	}
	b, err := json.Marshal(round)
	return append(msgs[:k:k], b), err
}

// piAnswers are the results a resume gives the interrupted round msgs ends
// with: responses answer its interrupted calls one to one by ID and name, and
// each call that ran keeps its output.
func piAnswers(msgs []*message, responses []ToolResult, redact func(string) string) (map[string]pi.Answer, error) {
	if !openCalls(msgs) {
		return nil, errors.New("ai: no open tool calls")
	}
	out := map[string]pi.Answer{}
	held := map[string]string{} // ID -> tool name
	for _, p := range msgs[len(msgs)-1].Content {
		if v, ran := p.Metadata["pendingOutput"]; ran && p.IsToolRequest() {
			out[p.ToolRequest.Ref] = piAnswer(v)
		} else if p.IsInterrupt() {
			held[p.ToolRequest.Ref] = p.ToolRequest.Name
		}
	}
	for _, res := range responses {
		name, ok := held[res.CallID]
		switch {
		case !ok:
			return nil, fmt.Errorf("ai: no open tool call %q", res.CallID)
		case res.Name != name:
			return nil, fmt.Errorf("ai: result for %q names tool %q, the call is to %q", res.CallID, res.Name, name)
		}
		delete(held, res.CallID)
		res.Error = redact(res.Error)
		v, err := resultOutput(res)
		if err != nil {
			return nil, err
		}
		out[res.CallID] = piAnswer(v)
	}
	if len(held) > 0 {
		return nil, errors.New("ai: every interrupted tool call needs exactly one result")
	}
	return out, nil
}

// piAnswer is a tool's output as a pi tool result, as piMessages writes one.
func piAnswer(v any) pi.Answer {
	o, _ := v.(map[string]any)
	_, failed := o["error"]
	return pi.Answer{Text: string(rawJSON(v)), IsError: failed}
}

// runTool runs one call of a run, sending its
// tool_call and tool_result events. g is what its gate decided as its round
// began; without it (a call held back with an interrupted round) the gate
// decides now, and a call that needs the user then is refused (errNeedsUser),
// as is one that interrupts as it runs. interrupt: Run interrupted the call
// (ErrInterrupt), which has no result.
func (r *runner) runTool(ctx context.Context, s ToolSpec, id string, args json.RawMessage, g *gated) (res ToolResult, interrupt bool) {
	r.send(Event{Kind: "tool_call", Call: &ToolCall{ID: id, Name: s.Name, Args: args}})
	late := g == nil
	if late {
		now, hold := gate(ctx, s, args)
		if hold {
			now.err = errNeedsUser
		}
		g = &now
	}
	err := g.err
	var out any
	switch {
	case err != nil:
	case s.Run == nil: // a gate allowed a tool that can only be approved
		err = errDenied
	default:
		if out, err = s.Run(ctx, args); errors.Is(err, ErrInterrupt) {
			if !late {
				return ToolResult{}, true
			}
			err = errNeedsUser
		}
	}
	res = ToolResult{CallID: id, Name: s.Name}
	if err != nil {
		res.Error = r.redact(err.Error())
	} else {
		res.Output = rawJSON(out)
	}
	r.send(Event{Kind: "tool_result", Result: &res})
	return res, false
}

// piError is the Error of how a pi reply ended, nil for a whole one. The
// messages are classify's, and the subscription codes respError's, so a run
// reads the same on either engine. codex: ChatGPT's Codex backend.
func piError(res pi.Result, codex bool) *Error {
	switch res.StopReason {
	case "stop":
		return nil
	case "error":
	default:
		return incomplete(res.StopReason)
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

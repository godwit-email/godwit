package ai

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	gai "github.com/firebase/genkit/go/ai"

	"github.com/godwit-email/godwit/core/ai/pi"
)

// piCovers reports whether pi runs req: a one-off prompt to a stored model
// service of any kind (#229, #230) or a plugin's model, whose client prepares
// and sends each request (#231). Each migration step widens it.
func piCovers(req Request) bool {
	return piAPI(req.Model) != "" &&
		req.History == nil && req.Prompt != "" && len(req.Tools) == 0 && len(req.Responses) == 0
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
	res, err := pi.Generate(ctx, cmp.Or(req.Model.Client, r.e.client), preq, func(kind, text string) {
		if kind == "text" || r.reasoning {
			r.send(Event{Kind: kind, Text: text})
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{Err: classify(ctx, err)}
		}
		return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
	}
	r.mu.Lock()
	r.usage = Usage{Input: res.Input, Output: res.Output, Last: res.Input}
	r.mu.Unlock()
	if e := piError(res, preq.API == "openai-codex-responses"); e != nil {
		return Result{Err: e}
	}
	return Result{}
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

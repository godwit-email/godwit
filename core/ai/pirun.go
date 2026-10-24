package ai

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	gai "github.com/firebase/genkit/go/ai"

	"github.com/godwit-email/godwit/core/ai/pi"
)

// piCovers reports whether pi runs req: a one-off prompt to an
// OpenAI-compatible service with a key (#229). Each migration step widens it.
func piCovers(req Request) bool {
	return req.Model.Kind == "openai" && req.Model.Client == nil && req.Model.APIKey != "" &&
		req.History == nil && req.Prompt != "" && len(req.Tools) == 0 && len(req.Responses) == 0
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

	res, err := pi.Generate(ctx, cmp.Or(req.Model.Client, r.e.client), pi.Request{
		BaseURL: req.Model.BaseURL, Model: req.Model.Model, APIKey: req.Model.APIKey,
		System: req.System, Prompt: req.Prompt, Effort: req.Effort,
	}, func(kind, text string) {
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
	if e := piError(res); e != nil {
		return Result{Err: e}
	}
	return Result{}
}

// piError is the Error of how a pi reply ended, nil for a whole one. The
// messages are classify's, so a run reads the same on either engine.
func piError(res pi.Result) *Error {
	switch res.StopReason {
	case "stop":
		return nil
	case "length":
		return incomplete(gai.FinishReasonLength)
	case "error":
	default:
		return incomplete(gai.FinishReasonOther)
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

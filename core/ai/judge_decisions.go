package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// decisions asks the OpenAI Decisions API through the SDK, which owns the
// wire format. The transport stays the judge's: this client, retries as post
// makes them (the SDK's own are off), a capped body and errors without text.
func (h judgeHTTP) decisions(ctx context.Context, input string, qs []Question) ([]Answer, error) {
	opts := []option.RequestOption{option.WithBaseURL(h.base + "/"), option.WithHTTPClient(h.c), option.WithMaxRetries(0),
		option.WithMiddleware(capBody)}
	if h.key != "" {
		opts = append(opts, option.WithAPIKey(h.key))
	}
	svc := openai.NewDecisionService(opts...)
	params := openai.DecisionNewParams{Model: h.model, Input: openai.DecisionNewParamsInputUnion{OfString: param.NewOpt(input)}}
	for _, q := range qs {
		params.Questions = append(params.Questions, decisionQuestion(q))
	}
	for attempt := 1; ; attempt++ {
		resp, err := svc.New(ctx, params)
		if err == nil {
			out := make([]Answer, len(resp.Answers))
			for i, a := range resp.Answers {
				out[i] = decisionAnswer(a)
			}
			return out, nil
		}
		if err := ctxError(err); err != nil {
			return nil, err
		}
		var ae *openai.Error
		if errors.As(err, &ae) {
			if err := h.retryAfter(ctx, attempt, ae.StatusCode, ae.Response.Header); err != nil {
				return nil, err
			}
			continue
		}
		if ue := (*url.Error)(nil); errors.As(err, &ue) {
			return nil, errUnreachable
		}
		return nil, errors.Join(ErrJudgeAnswer, errors.New("unreadable response"))
	}
}

// capBody reads replies as post does: a successful one as JSON whatever its
// Content-Type, cut at maxJudgeBody (a longer one fails to parse); a failed
// one's body is dropped for "{}", so the SDK's error has the status alone
// (a gateway's non-standard error body would break the SDK's parse, and its
// text never reaches our errors).
func capBody(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if resp == nil {
		return resp, err
	}
	resp.Header.Set("Content-Type", "application/json")
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader("{}"))
		return resp, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, maxJudgeBody), resp.Body}
	return resp, err
}

func decisionQuestion(q Question) openai.DecisionNewParamsQuestionUnion {
	name := param.NewOpt(q.Name)
	switch q.Type {
	case Choice:
		c := &openai.DecisionNewParamsQuestionChoice{Name: name, Instructions: q.Instructions}
		for _, o := range q.Choices {
			c.Choices = append(c.Choices, openai.DecisionNewParamsQuestionChoiceChoice{
				Value: openai.DecisionNewParamsQuestionChoiceChoiceValueUnion{OfString: param.NewOpt(o.Value)}, Description: optional(o.Description)})
		}
		return openai.DecisionNewParamsQuestionUnion{OfChoice: c}
	case Score:
		s := &openai.DecisionNewParamsQuestionScore{Name: name, Instructions: q.Instructions}
		for _, l := range q.Levels {
			s.Levels = append(s.Levels, openai.DecisionNewParamsQuestionScoreLevel{Label: l.Label, Description: optional(l.Description)})
		}
		return openai.DecisionNewParamsQuestionUnion{OfScore: s}
	}
	return openai.DecisionNewParamsQuestionUnion{OfPredicate: &openai.DecisionNewParamsQuestionPredicate{Name: name, Instructions: q.Instructions}}
}

// decisionAnswer is the SDK's answer as ours, unvalidated. A field the reply
// lacks stays unset (the SDK reads it as zero), so validation rejects it.
func decisionAnswer(u openai.DecisionAnswerUnion) Answer {
	a := Answer{Type: QuestionType(u.Type), Name: u.Name}
	switch v := u.AsAny().(type) {
	case openai.DecisionAnswerPredicate:
		a.Probability = valid(v.JSON.Probability.Valid(), v.Probability)
	case openai.DecisionAnswerChoice:
		a.Choice, a.Confidence = v.Choice.OfString, valid(v.JSON.Confidence.Valid(), v.Confidence)
		for _, p := range v.Probabilities {
			var val any = p.Value.OfString
			if p.Value.JSON.OfBool.Valid() { // we offer strings only: validation rejects it
				val = p.Value.OfBool
			}
			a.Probabilities = append(a.Probabilities, Probability{Value: val, Probability: p.Probability})
		}
	case openai.DecisionAnswerScore:
		a.Score, a.Confidence = valid(v.JSON.Score.Valid(), v.Score), valid(v.JSON.Confidence.Valid(), v.Confidence)
		for _, p := range v.Probabilities {
			var val any = int(p.Value)
			if !p.JSON.Value.Valid() { // not an index: the SDK reads it as 0
				val = nil
			}
			a.Probabilities = append(a.Probabilities, Probability{Value: val, Label: p.Label, Probability: p.Probability})
		}
	}
	return a
}

func valid(ok bool, v float64) *float64 {
	if !ok {
		return nil
	}
	return &v
}

func optional(s string) param.Opt[string] {
	if s == "" {
		return param.Opt[string]{}
	}
	return param.NewOpt(s)
}

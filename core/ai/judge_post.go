package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/godwit-email/godwit/core/ai/pi"
)

// post sends body to h.url through pi-ai's classifier transport, which
// retries 429, 529 and 5xx, and decodes the reply into
// out. Errors never carry the key, the body or *url.Error text.
func (h judgeHTTP) post(ctx context.Context, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	res, err := pi.Judge(ctx, h.c, pi.JudgeRequest{URL: h.url, APIKey: h.key, Body: b}, maxJudgeBody, pi.Hooks{Mask: maskHook(h.m, false)})
	if err != nil {
		if err := ctxError(err); err != nil {
			return err
		}
		return fmt.Errorf("judge: %w", err)
	}
	switch st := res.Status; {
	case res.Body != nil:
		if json.Unmarshal(res.Body, out) != nil {
			return fmt.Errorf("%w: unreadable response", ErrJudgeAnswer)
		}
		return nil
	case st == http.StatusUnauthorized:
		return ErrModelAuth
	case st == http.StatusForbidden:
		return ErrModelForbidden
	case st != 0:
		return &HTTPError{st}
	case res.Transport != nil:
		return errUnreachable
	}
	return fmt.Errorf("%w: unreadable response", ErrJudgeAnswer)
}

// decisions asks the OpenAI Decisions API in the wire format its SDK encodes.
func (h judgeHTTP) decisions(ctx context.Context, input string, qs []Question) ([]Answer, error) {
	var resp struct {
		Answers []struct {
			Type                           QuestionType
			Name                           string
			Probability, Score, Confidence *float64
			Choice                         any
			Probabilities                  []struct {
				Value       any
				Label       string
				Probability *float64
			}
		}
	}
	if err := h.post(ctx, map[string]any{"model": h.model, "input": input, "questions": qs}, &resp); err != nil {
		return nil, err
	}
	// A value of the wrong JSON type or a missing probability is left for
	// validation to reject.
	out := make([]Answer, len(resp.Answers))
	for i, a := range resp.Answers {
		v := Answer{Type: a.Type, Name: a.Name, Probability: a.Probability, Score: a.Score, Confidence: a.Confidence}
		v.Choice, _ = a.Choice.(string)
		for _, p := range a.Probabilities {
			v.Probabilities = append(v.Probabilities, Probability{Value: p.Value, Label: p.Label, Probability: deref(p.Probability)})
		}
		out[i] = v
	}
	return out, nil
}

// deref is *p, or NaN (which validation rejects) when p is nil.
func deref(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

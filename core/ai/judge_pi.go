package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/godwit-email/godwit/core/ai/pi"
)

// NewPiJudge is NewJudge on pi (Labs: Settings.PiEngine): pi-ai's classifier
// transport sends each batch, encoded and read here as on the Genkit path,
// so the answers and probabilities are the same.
func NewPiJudge(c *http.Client, format JudgeFormat, base, key, model string) (Judge, error) {
	return newJudge(c, format, base, key, model, true)
}

// piPost is post on pi: body to h.url, the reply decoded into out, retried
// and failing as post does.
func (h judgeHTTP) piPost(ctx context.Context, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	res, err := pi.Judge(ctx, h.c, pi.JudgeRequest{URL: h.url, APIKey: h.key, Body: b}, maxJudgeBody)
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

// piDecisions asks the OpenAI Decisions API on pi, in the wire format the
// SDK encodes on the Genkit path (decisions).
func (h judgeHTTP) piDecisions(ctx context.Context, input string, qs []Question) ([]Answer, error) {
	type option struct {
		Value       string `json:"value,omitempty"`
		Label       string `json:"label,omitempty"`
		Description string `json:"description,omitempty"`
	}
	type question struct {
		Type         QuestionType `json:"type"`
		Name         string       `json:"name"`
		Instructions string       `json:"instructions"`
		Choices      []option     `json:"choices,omitempty"`
		Levels       []option     `json:"levels,omitempty"`
	}
	wire := make([]question, len(qs))
	for i, q := range qs {
		w := question{Type: q.Type, Name: q.Name, Instructions: q.Instructions}
		for _, o := range q.Choices {
			w.Choices = append(w.Choices, option{Value: o.Value, Description: o.Description})
		}
		for _, l := range q.Levels {
			w.Levels = append(w.Levels, option{Label: l.Label, Description: l.Description})
		}
		wire[i] = w
	}
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
	if err := h.piPost(ctx, map[string]any{"model": h.model, "input": input, "questions": wire}, &resp); err != nil {
		return nil, err
	}
	// As decisionAnswer reads the SDK's: a value of the wrong JSON type or a
	// missing probability is left for validation to reject.
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

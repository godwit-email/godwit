package ai

import (
	"context"
	"encoding/json"
)

// decisions asks the OpenAI Decisions API (research §1): input plus an array
// of questions; answers come back as an array echoing name and type.
func (h judgeHTTP) decisions(ctx context.Context, state string, qs []Question) ([]raw, error) {
	type opt struct {
		Value       string `json:"value,omitempty"`
		Label       string `json:"label,omitempty"`
		Description string `json:"description,omitempty"`
	}
	type question struct {
		Type         string `json:"type"`
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
		Choices      []opt  `json:"choices,omitempty"`
		Levels       []opt  `json:"levels,omitempty"`
	}
	wire := make([]question, len(qs))
	for i, q := range qs {
		w := question{Name: q.Name, Instructions: q.Instructions}
		switch q.Kind {
		case YesNo:
			w.Type = "predicate"
		case Choice:
			w.Type = "choice"
			for _, o := range q.Choices {
				w.Choices = append(w.Choices, opt{Value: o.Value, Description: o.Description})
			}
		case Score:
			w.Type = "score"
			for _, o := range q.Levels {
				w.Levels = append(w.Levels, opt{Label: o.Value, Description: o.Description})
			}
		}
		wire[i] = w
	}
	var resp struct {
		Answers []struct {
			Type, Name    string
			Probability   *float64
			Choice        string
			Score         *float64
			Probabilities []struct {
				Value       json.RawMessage
				Probability float64
			}
		}
	}
	if err := h.post(ctx, map[string]any{"model": h.model, "input": state, "questions": wire}, &resp); err != nil {
		return nil, err
	}
	out := make([]raw, 0, len(resp.Answers))
	for _, a := range resp.Answers {
		r := raw{Answer: Answer{Name: a.Name}}
		switch a.Type {
		case "refusal":
			r.Refused = true
		case "predicate":
			r.kind, r.Probability = YesNo, num(a.Probability)
		case "choice":
			r.kind, r.Choice = Choice, a.Choice
			for _, p := range a.Probabilities {
				var v string
				if json.Unmarshal(p.Value, &v) == nil && v == a.Choice {
					r.Probability = p.Probability
				}
			}
		case "score":
			r.kind, r.Score = Score, num(a.Score)
		default:
			r.kind = -1
		}
		out = append(out, r)
	}
	return out, nil
}

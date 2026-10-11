package ai

import "context"

// systemOne asks TypeSafe System One (research §2): state plus a map of
// questions keyed by name; answers come back as a map keyed by name.
func (h judgeHTTP) systemOne(ctx context.Context, state string, qs []Question) ([]raw, error) {
	type question struct {
		Type         string `json:"type"`
		Instructions string `json:"instructions"`
		Criteria     any    `json:"criteria,omitempty"`
	}
	wire := make(map[string]question, len(qs))
	for _, q := range qs {
		w := question{Instructions: q.Instructions}
		switch q.Kind {
		case YesNo:
			w.Type = "noul"
		case Choice:
			w.Type = "choice"
			c := make(map[string]string, len(q.Choices))
			for _, o := range q.Choices {
				c[o.Value] = o.Description
			}
			w.Criteria = c
		case Score:
			w.Type = "score"
			c := make([]string, len(q.Levels))
			for i, o := range q.Levels {
				c[i] = levelText(o)
			}
			w.Criteria = c
		}
		wire[q.Name] = w
	}
	var resp struct {
		Answers map[string]struct {
			Type          string
			Noul          *float64
			Choice        string
			Score         *float64
			Probabilities map[string]float64
		}
	}
	if err := h.post(ctx, map[string]any{"model": h.model, "state": state, "questions": wire}, &resp); err != nil {
		return nil, err
	}
	out := make([]raw, 0, len(resp.Answers))
	for name, a := range resp.Answers {
		r := raw{Answer: Answer{Name: name}}
		switch a.Type {
		case "refusal": // undocumented for System One; accepted like Decisions'
			r.Refused = true
		case "noul":
			r.kind, r.Probability = YesNo, num(a.Noul)
		case "choice":
			r.kind, r.Choice, r.Probability = Choice, a.Choice, a.Probabilities[a.Choice]
		case "score":
			r.kind, r.Score = Score, num(a.Score)
		default:
			r.kind = -1
		}
		out = append(out, r)
	}
	return out, nil
}

// levelText describes a score level; System One takes descriptions only.
func levelText(o Option) string {
	switch {
	case o.Description == "":
		return o.Value
	case o.Value == "":
		return o.Description
	}
	return o.Value + ": " + o.Description
}

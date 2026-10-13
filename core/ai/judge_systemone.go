package ai

import (
	"context"
	"slices"
	"strconv"
)

// systemOne asks TypeSafe System One (research §2), converting to and from
// the Decisions shape: noul is a predicate; questions and answers are maps
// keyed by name; probabilities are maps keyed by value or level index.
func (h judgeHTTP) systemOne(ctx context.Context, input string, qs []Question) ([]Answer, error) {
	type question struct {
		Type         string `json:"type"`
		Instructions string `json:"instructions"`
		Criteria     any    `json:"criteria,omitempty"`
	}
	wire := make(map[string]question, len(qs))
	byName := make(map[string]Question, len(qs))
	for _, q := range qs {
		w := question{Type: string(q.Type), Instructions: q.Instructions}
		switch q.Type {
		case Predicate:
			w.Type = "noul"
		case Choice:
			c := make(map[string]string, len(q.Choices))
			for _, o := range q.Choices {
				c[o.Value] = o.Description
			}
			w.Criteria = c
		case Score:
			c := make([]string, len(q.Levels))
			for i, l := range q.Levels {
				c[i] = levelText(l)
			}
			w.Criteria = c
		}
		wire[q.Name], byName[q.Name] = w, q
	}
	var resp struct {
		Answers map[string]struct {
			Type          string
			Noul          *float64
			Choice        string
			Score         *float64
			Probabilities map[string]float64
			Confidence    *float64
		}
	}
	if err := h.post(ctx, map[string]any{"model": h.model, "state": input, "questions": wire}, &resp); err != nil {
		return nil, err
	}
	out := make([]Answer, 0, len(resp.Answers))
	for name, a := range resp.Answers {
		v := Answer{Type: QuestionType(a.Type), Name: name, Choice: a.Choice, Score: a.Score, Confidence: a.Confidence}
		switch a.Type {
		case "noul":
			v.Type, v.Probability = Predicate, a.Noul
		case "choice":
			v.Probabilities = probsByValue(byName[name].Choices, a.Probabilities)
		case "score":
			v.Probabilities = probsByLevel(a.Probabilities)
		}
		out = append(out, v)
	}
	return out, nil
}

// probsByValue lists a choice's probabilities in the choices' order; keys
// that are no choice come last, for validation to reject.
func probsByValue(choices []ChoiceOption, m map[string]float64) []Probability {
	var out []Probability
	for _, o := range choices {
		if p, ok := m[o.Value]; ok {
			out = append(out, Probability{Value: o.Value, Probability: p})
			delete(m, o.Value)
		}
	}
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}) {
		out = append(out, Probability{Value: k, Probability: m[k]})
	}
	return out
}

// probsByLevel lists a score's probabilities by level index ("0", "1", ...);
// a key that is no index stays a string, for validation to reject.
func probsByLevel(m map[string]float64) []Probability {
	out := make([]Probability, 0, len(m))
	for k, p := range m {
		var v any = k
		if i, err := strconv.Atoi(k); err == nil {
			v = i
		}
		out = append(out, Probability{Value: v, Probability: p})
	}
	slices.SortFunc(out, func(a, b Probability) int {
		ai, aok := a.Value.(int)
		bi, bok := b.Value.(int)
		switch {
		case aok && bok:
			return ai - bi
		case aok:
			return -1
		case bok:
			return 1
		}
		return 0
	})
	return out
}

// levelText describes a score level; System One takes descriptions only.
func levelText(l Level) string {
	switch {
	case l.Description == "":
		return l.Label
	case l.Label == "":
		return l.Description
	}
	return l.Label + ": " + l.Description
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Generator is one plain text generation: no tools, one turn.
type Generator func(ctx context.Context, system, prompt string) (string, error)

const genJudgeSystem = `You answer typed questions about the text in the user message. ` +
	`Reply with one JSON object and nothing else: {"answers":[{"name":"<question name>","answer":<value>}]}, one entry per question. ` +
	`A yes_no answer is "yes" or "no". A choice answer is exactly one of the listed values. ` +
	`A score answer is the integer index of the level, 0 being the lowest. ` +
	`To decline a question, give {"name":"<question name>","refused":true} instead.`

// NewGenJudge answers with the generation model when no judge service is
// configured. Its yes/no probabilities are 0 or 1 and its scores are integer
// levels; the answers pass the same validation as the judge APIs'.
func NewGenJudge(gen Generator) Judge {
	return judge{func(ctx context.Context, state string, qs []Question) ([]raw, error) {
		return genAsk(ctx, gen, state, qs)
	}, 60 * time.Second}
}

func genAsk(ctx context.Context, gen Generator, state string, qs []Question) ([]raw, error) {
	type opt struct {
		Value       string `json:"value,omitempty"`
		Index       *int   `json:"index,omitempty"`
		Label       string `json:"label,omitempty"`
		Description string `json:"description,omitempty"`
	}
	type question struct {
		Name         string `json:"name"`
		Type         string `json:"type"`
		Instructions string `json:"instructions"`
		Choices      []opt  `json:"choices,omitempty"`
		Levels       []opt  `json:"levels,omitempty"`
	}
	wire := make([]question, len(qs))
	kinds := map[string]QuestionKind{}
	for i, q := range qs {
		w := question{Name: q.Name, Type: "yes_no", Instructions: q.Instructions}
		switch q.Kind {
		case Choice:
			w.Type = "choice"
			for _, o := range q.Choices {
				w.Choices = append(w.Choices, opt{Value: o.Value, Description: o.Description})
			}
		case Score:
			w.Type = "score"
			for j, o := range q.Levels {
				w.Levels = append(w.Levels, opt{Index: &j, Label: o.Value, Description: o.Description})
			}
		}
		wire[i], kinds[q.Name] = w, q.Kind
	}
	qj, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	// Always wrapped: Untrusted escapes any wrapper markers inside, so mail
	// can't fake the boundary. Wit's pre-wrapped blocks just nest, escaped.
	state = Untrusted("judge", state, SuspectInjection(state))
	text, err := gen(ctx, genJudgeSystem+"\n\n"+UntrustedSystemNote, state+"\n\nQuestions:\n"+string(qj))
	if err != nil {
		return nil, err
	}
	if len(text) > maxJudgeBody {
		return nil, fmt.Errorf("%w: answer too long", ErrJudgeAnswer)
	}
	var resp struct {
		Answers []struct {
			Name    string
			Answer  json.RawMessage
			Refused bool
		}
	}
	// Lenient parse: the first "{" that starts a JSON object, past any prose
	// or code fence; bounded so a brace-heavy reply can't cost n² decodes.
	parsed := false
	for i, tries := 0, 0; !parsed && tries < 8; tries++ {
		j := strings.IndexByte(text[i:], '{')
		if j < 0 {
			break
		}
		i += j
		parsed = json.NewDecoder(strings.NewReader(text[i:])).Decode(&resp) == nil
		i++
	}
	if !parsed {
		return nil, fmt.Errorf("%w: no JSON answer", ErrJudgeAnswer)
	}
	out := make([]raw, 0, len(resp.Answers))
	for _, a := range resp.Answers {
		r := raw{Answer: Answer{Name: a.Name, Refused: a.Refused}, kind: -1}
		if k, ok := kinds[a.Name]; ok && !a.Refused {
			r.kind = k
			switch k {
			case YesNo:
				r.Probability = genYesNo(a.Answer)
			case Choice:
				if json.Unmarshal(a.Answer, &r.Choice) != nil {
					r.kind = -1
				}
				r.Choice, r.Probability = strings.TrimSpace(r.Choice), 1
			case Score:
				r.Score = genScore(a.Answer)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// genYesNo reads "yes"/"no", a bool, or 0/1; anything else, a hedged 0.7
// included, is NaN so it is rejected rather than passed off as calibrated.
func genYesNo(m json.RawMessage) float64 {
	var v any
	if json.Unmarshal(m, &v) != nil {
		return math.NaN()
	}
	switch v := v.(type) {
	case bool:
		if v {
			return 1
		}
		return 0
	case float64:
		if v == 0 || v == 1 {
			return v
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "yes", "true":
			return 1
		case "no", "false":
			return 0
		}
	}
	return math.NaN()
}

// genScore reads an integer level, as a number or a numeric string.
func genScore(m json.RawMessage) float64 {
	var v any
	if json.Unmarshal(m, &v) != nil {
		return math.NaN()
	}
	f := math.NaN()
	switch v := v.(type) {
	case float64:
		f = v
	case string:
		if p, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			f = p
		}
	}
	if f != math.Trunc(f) {
		return math.NaN()
	}
	return f
}

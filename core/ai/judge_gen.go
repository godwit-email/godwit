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
	`A predicate answer is "yes" or "no". A choice answer is exactly one of the listed values. ` +
	`A score answer is the integer index of the level, 0 being the lowest. ` +
	`To decline a question, give {"name":"<question name>","refused":true} instead. ` +
	`Omitted predicate questions default to "no".`

// NewGenJudge answers with the generation model when no judge service is
// configured. Its yes/no probabilities are 0 or 1 and its scores are integer
// levels; the answers pass the same validation as the judge APIs'.
func NewGenJudge(gen Generator) Judge {
	return judge{func(ctx context.Context, input string, qs []Question) ([]Answer, error) {
		return genAsk(ctx, gen, input, qs)
	}, 60 * time.Second}
}

// GenJudgeQuestions is the questions as NewGenJudge's prompt carries them,
// for callers that must budget its size.
func GenJudgeQuestions(qs []Question) ([]byte, error) {
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
	for i, q := range qs {
		w := question{Name: q.Name, Type: string(q.Type), Instructions: q.Instructions}
		switch q.Type {
		case Choice:
			for _, o := range q.Choices {
				w.Choices = append(w.Choices, opt{Value: o.Value, Description: o.Description})
			}
		case Score:
			for j, l := range q.Levels {
				w.Levels = append(w.Levels, opt{Index: &j, Label: l.Label, Description: l.Description})
			}
		}
		wire[i] = w
	}
	return json.Marshal(wire)
}

func genAsk(ctx context.Context, gen Generator, input string, qs []Question) ([]Answer, error) {
	types := map[string]QuestionType{}
	for _, q := range qs {
		types[q.Name] = q.Type
	}
	qj, err := GenJudgeQuestions(qs)
	if err != nil {
		return nil, err
	}
	// Always wrapped: Untrusted escapes any wrapper markers inside, so mail
	// can't fake the boundary. Wit's pre-wrapped blocks just nest, escaped.
	input = Untrusted("judge", input, SuspectInjection(input))
	text, err := gen(ctx, genJudgeSystem+"\n\n"+UntrustedSystemNote, input+"\n\nQuestions:\n"+string(qj))
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
	out := make([]Answer, 0, len(resp.Answers))
	for _, a := range resp.Answers {
		v := Answer{Name: a.Name}
		switch t := types[a.Name]; {
		case a.Refused:
			v.Type = Refusal
		case t == Predicate:
			v.Type, v.Probability = t, ptr(genYesNo(a.Answer))
		case t == Choice:
			if json.Unmarshal(a.Answer, &v.Choice) == nil {
				v.Type, v.Choice = t, strings.TrimSpace(v.Choice)
			}
		case t == Score:
			v.Type, v.Score = t, ptr(genScore(a.Answer))
		}
		out = append(out, v)
	}
	// Fill omitted predicates with "no" only when the model answered at least
	// one asked question; an empty or unmatched reply stays an ErrJudgeAnswer
	// so callers keep existing labels instead of treating it as all-no.
	seen := make(map[string]bool, len(out))
	valid := false
	for _, a := range out {
		seen[a.Name] = true
		if _, ok := types[a.Name]; ok {
			valid = true
		}
	}
	for _, q := range qs {
		if valid && !seen[q.Name] && q.Type == Predicate {
			out = append(out, Answer{Name: q.Name, Type: Predicate, Probability: ptr(0.0)})
		}
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

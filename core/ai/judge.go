package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// QuestionType is a question's type. Questions and answers take the OpenAI
// Decisions API's shape and names; the other formats convert to it.
type QuestionType string

const (
	Predicate QuestionType = "predicate" // the probability that it holds
	Choice    QuestionType = "choice"    // one value of Question.Choices
	Score     QuestionType = "score"     // a 0-based, possibly fractional, index into Question.Levels
	// Refusal is the type of an answer the judge declined to give.
	Refusal QuestionType = "refusal"
)

// ChoiceOption is one value a choice question may answer.
type ChoiceOption struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// Level is one level of a score question.
type Level struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one typed question. Instructions come from the caller, never
// from mail; mail goes into the input.
type Question struct {
	Name         string         `json:"name"`
	Type         QuestionType   `json:"type"`
	Instructions string         `json:"instructions"`
	Choices      []ChoiceOption `json:"choices,omitempty"` // choice: 2–255 unique values
	Levels       []Level        `json:"levels,omitempty"`  // score: 2–10, lowest first
}

// Answer is a validated answer, in the Decisions API's shape: Type is the
// question's, or Refusal with nothing else set. A predicate sets Probability;
// a choice sets Choice; a score sets Score in [0, len(Levels)-1]. Choices
// and scores may add Probabilities and Confidence when the judge gives them.
type Answer struct {
	Type          QuestionType  `json:"type"`
	Name          string        `json:"name"`
	Probability   *float64      `json:"probability,omitempty"`
	Choice        string        `json:"choice,omitempty"`
	Score         *float64      `json:"score,omitempty"`
	Probabilities []Probability `json:"probabilities,omitempty"`
	Confidence    *float64      `json:"confidence,omitempty"`
}

// Probability is the probability of one choice value (Value a string) or
// one score level (Value its index, Label its label).
type Probability struct {
	Value       any     `json:"value"`
	Label       string  `json:"label,omitempty"`
	Probability float64 `json:"probability"`
}

// Judge answers typed questions about input, which is untrusted data.
type Judge interface {
	Ask(ctx context.Context, input string, qs []Question) ([]Answer, error)
}

// JudgeFormat is a judge wire format.
type JudgeFormat string

const (
	FormatDecisions JudgeFormat = "decisions" // OpenAI Decisions API
	FormatSystemOne JudgeFormat = "systemone" // TypeSafe System One
)

// Path is the endpoint path appended to the base URL.
func (f JudgeFormat) Path() string { return "/" + string(f) }

// Models is the static model list for the format (design FORMATS.models).
func (f JudgeFormat) Models() []string {
	switch f {
	case FormatDecisions:
		return []string{"gpt-6-luna"}
	case FormatSystemOne:
		return []string{"jev-latest", "jev-preview"}
	}
	return nil
}

var (
	ErrModelForbidden = errors.New("the model service refused this key for that API")
	// ErrJudgeAnswer: the service answered, but not with a valid typed answer.
	ErrJudgeAnswer   = errors.New("the judge gave an invalid answer")
	ErrJudgeQuestion = errors.New("invalid judge question")
)

// HTTPError is a judge HTTP failure; it never carries the body.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("judge: HTTP %d", e.Status) }

const (
	maxQuestions  = 128
	maxJudgeBody  = 4 << 20
	judgeAttempts = 3
	maxRetryAfter = 2 * time.Second
)

// sleep waits between retries; tests replace it.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// batchFunc asks one batch of at most maxQuestions and returns its answers,
// converted to the Decisions shape but not yet validated.
type batchFunc func(ctx context.Context, input string, qs []Question) ([]Answer, error)

// judge validates the questions, splits them into batches under one deadline
// and validates every answer.
type judge struct {
	ask     batchFunc
	timeout time.Duration
}

func (j judge) Ask(ctx context.Context, input string, qs []Question) ([]Answer, error) {
	if err := validateQuestions(qs); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	out := make([]Answer, 0, len(qs))
	for len(qs) > 0 {
		n := min(len(qs), maxQuestions)
		got, err := j.ask(ctx, input, qs[:n])
		if err != nil {
			return nil, err
		}
		as, err := validateAnswers(qs[:n], got)
		if err != nil {
			return nil, err
		}
		out, qs = append(out, as...), qs[n:]
	}
	return out, nil
}

// NewJudge returns a client for a judge API at base+format.Path(). c is the
// no-redirect model client (nil means NoRedirectClient).
func NewJudge(c *http.Client, format JudgeFormat, base, key, model string) (Judge, error) {
	if c == nil {
		c = NoRedirectClient
	}
	h := judgeHTTP{c: c, url: strings.TrimRight(base, "/") + format.Path(), key: key, model: model}
	switch format {
	case FormatDecisions:
		return judge{h.decisions, 15 * time.Second}, nil
	case FormatSystemOne:
		return judge{h.systemOne, 15 * time.Second}, nil
	}
	return nil, fmt.Errorf("unknown judge format %q", format)
}

// ListJudgeModels lists the judge models for a format. Decisions has no
// listing (research §1), so it is the static list; System One asks
// base+"/models". On error callers may fall back to format.Models().
func ListJudgeModels(ctx context.Context, c *http.Client, format JudgeFormat, base, key string) ([]string, error) {
	switch format {
	case FormatDecisions:
		return format.Models(), nil
	case FormatSystemOne:
		return ListModels(ctx, c, KindOpenAI, base, key)
	}
	return nil, fmt.Errorf("unknown judge format %q", format)
}

type judgeHTTP struct {
	c               *http.Client
	url, key, model string
}

// post sends body and decodes the reply into out, retrying 429, 529 and 5xx.
// Errors never carry the key, the body or *url.Error text.
func (h judgeHTTP) post(ctx context.Context, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(b))
		if err != nil {
			return errors.New("judge: bad base URL")
		}
		req.Header.Set("Content-Type", "application/json")
		if h.key != "" {
			req.Header.Set("Authorization", "Bearer "+h.key)
		}
		resp, err := h.c.Do(req)
		if err != nil {
			for _, ce := range []error{context.Canceled, context.DeadlineExceeded} {
				if errors.Is(err, ce) {
					return fmt.Errorf("judge: %w", ce)
				}
			}
			return errors.New("judge: could not reach the model service")
		}
		st := resp.StatusCode
		if st == http.StatusOK {
			err := json.NewDecoder(io.LimitReader(resp.Body, maxJudgeBody)).Decode(out)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("%w: unreadable response", ErrJudgeAnswer)
			}
			return nil
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		switch {
		case st == http.StatusUnauthorized:
			return ErrModelAuth
		case st == http.StatusForbidden:
			return ErrModelForbidden
		case (st == 429 || st == 529 || st >= 500) && attempt < judgeAttempts:
			if err := sleep(ctx, backoff(attempt, resp.Header.Get("Retry-After"))); err != nil {
				return fmt.Errorf("judge: %w", err)
			}
		default:
			return &HTTPError{st}
		}
	}
}

// backoff honors Retry-After seconds, capped at maxRetryAfter.
func backoff(attempt int, retryAfter string) time.Duration {
	d := time.Duration(250<<attempt) * time.Millisecond
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s >= 0 {
		d = time.Duration(s) * time.Second
	}
	return min(d, maxRetryAfter)
}

// questionName is the plugin-facing name rule (review #10), enforced here so
// every caller gets it.
var questionName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

// ValidateQuestions checks qs as Ask does, for callers that ask many times.
func ValidateQuestions(qs []Question) error { return validateQuestions(qs) }

func validateQuestions(qs []Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("%w: no questions", ErrJudgeQuestion)
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if !questionName.MatchString(q.Name) || seen[q.Name] {
			return fmt.Errorf("%w: bad or duplicate name %q", ErrJudgeQuestion, q.Name)
		}
		seen[q.Name] = true
		if strings.TrimSpace(q.Instructions) == "" {
			return fmt.Errorf("%w: %q has no instructions", ErrJudgeQuestion, q.Name)
		}
		// Each type carries only its own options: the Decisions API rejects the others.
		if q.Type != Choice && len(q.Choices) > 0 || q.Type != Score && len(q.Levels) > 0 {
			return fmt.Errorf("%w: %q has options its type does not take", ErrJudgeQuestion, q.Name)
		}
		switch q.Type {
		case Predicate:
		case Choice:
			if len(q.Choices) < 2 || len(q.Choices) > 255 {
				return fmt.Errorf("%w: %q needs 2–255 choices", ErrJudgeQuestion, q.Name)
			}
			vals := map[string]bool{}
			for _, o := range q.Choices {
				if o.Value == "" || vals[o.Value] {
					return fmt.Errorf("%w: %q has an empty or duplicate choice", ErrJudgeQuestion, q.Name)
				}
				vals[o.Value] = true
			}
		case Score:
			if len(q.Levels) < 2 || len(q.Levels) > 10 {
				return fmt.Errorf("%w: %q needs 2–10 levels", ErrJudgeQuestion, q.Name)
			}
			for _, o := range q.Levels {
				if o.Label == "" && o.Description == "" {
					return fmt.Errorf("%w: %q has an empty level", ErrJudgeQuestion, q.Name)
				}
			}
		default:
			return fmt.Errorf("%w: %q has an unknown type", ErrJudgeQuestion, q.Name)
		}
	}
	return nil
}

// validateAnswers checks answers against qs: exactly one answer per question,
// the right type, values in range. Probabilities come out in the Decisions
// shape: a choice's by value, a score's by level index with its label.
// Answer values never reach error text.
func validateAnswers(qs []Question, answers []Answer) ([]Answer, error) {
	byName := map[string]Answer{}
	for _, a := range answers {
		if _, dup := byName[a.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate answer", ErrJudgeAnswer)
		}
		byName[a.Name] = a
	}
	if len(byName) != len(qs) {
		return nil, fmt.Errorf("%w: %d answers for %d questions", ErrJudgeAnswer, len(byName), len(qs))
	}
	out := make([]Answer, len(qs))
	for i, q := range qs {
		a, ok := byName[q.Name]
		if !ok {
			return nil, fmt.Errorf("%w: no answer for %q", ErrJudgeAnswer, q.Name)
		}
		if a.Type == Refusal {
			out[i] = Answer{Type: Refusal, Name: q.Name}
			continue
		}
		bad := func(what string) error { return fmt.Errorf("%w: %q has %s", ErrJudgeAnswer, q.Name, what) }
		if a.Type != q.Type {
			return nil, bad("the wrong answer type")
		}
		if a.Confidence != nil && !prob(*a.Confidence) {
			return nil, bad("a confidence outside 0..1")
		}
		v := Answer{Type: q.Type, Name: q.Name, Confidence: a.Confidence}
		switch q.Type {
		case Predicate:
			if a.Probability == nil || !prob(*a.Probability) {
				return nil, bad("a probability outside 0..1")
			}
			v.Probability, v.Confidence = a.Probability, nil
		case Choice:
			if !hasValue(q.Choices, a.Choice) {
				return nil, bad("a value outside the choices")
			}
			v.Choice = a.Choice
			for _, p := range a.Probabilities {
				s, ok := p.Value.(string)
				if !ok || !hasValue(q.Choices, s) || !prob(p.Probability) {
					return nil, bad("a probability outside the choices or 0..1")
				}
				v.Probabilities = append(v.Probabilities, Probability{Value: s, Probability: p.Probability})
			}
		case Score:
			if a.Score == nil || math.IsNaN(*a.Score) || *a.Score < 0 || *a.Score > float64(len(q.Levels)-1) {
				return nil, bad("a score outside the levels")
			}
			v.Score = a.Score
			for _, p := range a.Probabilities {
				i, ok := levelIndex(p.Value, len(q.Levels))
				if !ok || !prob(p.Probability) {
					return nil, bad("a probability outside the levels or 0..1")
				}
				v.Probabilities = append(v.Probabilities, Probability{Value: i, Label: q.Levels[i].Label, Probability: p.Probability})
			}
		}
		out[i] = v
	}
	return out, nil
}

// levelIndex reads a score probability's level: an integer in [0, n).
func levelIndex(v any, n int) (int, bool) {
	var f float64
	switch v := v.(type) {
	case int:
		f = float64(v)
	case float64:
		f = v
	default:
		return 0, false
	}
	if f != math.Trunc(f) || f < 0 || f >= float64(n) {
		return 0, false
	}
	return int(f), true
}

func prob(p float64) bool { return !math.IsNaN(p) && p >= 0 && p <= 1 }

func hasValue(os []ChoiceOption, v string) bool {
	for _, o := range os {
		if o.Value == v {
			return true
		}
	}
	return false
}

// ptr is a pointer to v.
func ptr(v float64) *float64 { return &v }

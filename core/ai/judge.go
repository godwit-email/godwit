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
	"strconv"
	"strings"
	"time"
)

// QuestionKind is what a judge question asks for.
type QuestionKind int

const (
	YesNo  QuestionKind = iota // a probability that the answer is yes
	Choice                     // one value of Question.Choices
	Score                      // a 0-based index into Question.Levels
)

// Option is a choice value or a score level.
type Option struct{ Value, Description string }

// Question is one typed question. Instructions come from the caller, never
// from mail; mail goes into the state.
type Question struct {
	Name, Instructions string
	Kind               QuestionKind
	Choices            []Option // Choice: 2–255 unique values
	Levels             []Option // Score: 2–10, lowest first
}

// Answer is a validated answer. YesNo sets Probability; Choice sets Choice
// and, when the service gives it, its Probability; Score sets Score in
// [0, len(Levels)-1]. Refused answers carry no value.
type Answer struct {
	Name        string
	Choice      string
	Probability float64
	Score       float64
	Refused     bool
}

// Judge answers typed questions about state, which is untrusted data.
type Judge interface {
	Ask(ctx context.Context, state string, qs []Question) ([]Answer, error)
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

// batchFunc asks one batch of at most maxQuestions and returns raw answers.
type batchFunc func(ctx context.Context, state string, qs []Question) ([]raw, error)

// raw is an adapter's parse before validation, with the kind the service
// says it answered.
type raw struct {
	Answer
	kind QuestionKind
}

// judge validates the questions, splits them into batches under one deadline
// and validates every answer.
type judge struct {
	ask     batchFunc
	timeout time.Duration
}

func (j judge) Ask(ctx context.Context, state string, qs []Question) ([]Answer, error) {
	if err := validateQuestions(qs); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	out := make([]Answer, 0, len(qs))
	for len(qs) > 0 {
		n := min(len(qs), maxQuestions)
		raw, err := j.ask(ctx, state, qs[:n])
		if err != nil {
			return nil, err
		}
		as, err := validateAnswers(qs[:n], raw)
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

func validateQuestions(qs []Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("%w: no questions", ErrJudgeQuestion)
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if q.Name == "" || seen[q.Name] {
			return fmt.Errorf("%w: empty or duplicate name %q", ErrJudgeQuestion, q.Name)
		}
		seen[q.Name] = true
		switch q.Kind {
		case YesNo:
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
		default:
			return fmt.Errorf("%w: %q has an unknown kind", ErrJudgeQuestion, q.Name)
		}
	}
	return nil
}

// validateAnswers checks raws against qs: exactly one answer per question,
// the right kind, values in range. Answer values never reach error text.
func validateAnswers(qs []Question, raws []raw) ([]Answer, error) {
	byName := map[string]raw{}
	for _, a := range raws {
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
		if a.Refused {
			out[i] = Answer{Name: q.Name, Refused: true}
			continue
		}
		bad := func(what string) error { return fmt.Errorf("%w: %q has %s", ErrJudgeAnswer, q.Name, what) }
		if a.kind != q.Kind {
			return nil, bad("the wrong answer type")
		}
		if !prob(a.Probability) {
			return nil, bad("a probability outside 0..1")
		}
		switch q.Kind {
		case YesNo:
			out[i] = Answer{Name: q.Name, Probability: a.Probability}
		case Choice:
			if !hasValue(q.Choices, a.Choice) {
				return nil, bad("a label outside the choices")
			}
			out[i] = Answer{Name: q.Name, Choice: a.Choice, Probability: a.Probability}
		case Score:
			if math.IsNaN(a.Score) || a.Score < 0 || a.Score > float64(len(q.Levels)-1) {
				return nil, bad("a score outside the levels")
			}
			out[i] = Answer{Name: q.Name, Score: a.Score}
		}
	}
	return out, nil
}

func prob(p float64) bool { return !math.IsNaN(p) && p >= 0 && p <= 1 }

func hasValue(os []Option, v string) bool {
	for _, o := range os {
		if o.Value == v {
			return true
		}
	}
	return false
}

// num is a parsed number; a missing one is NaN, which validation rejects.
func num(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

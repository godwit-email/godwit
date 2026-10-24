package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
)

// askQuestionTool asks the user questions (as Claude Code's AskUserQuestion):
// an approval whose acceptance carries the answers (AnswerQuestion) and whose
// rejection is a skip. No grant or 全部允许 answers it.
const askQuestionTool = "ask_user_question"

// msgUnanswered is what the model is told for a skipped question.
const msgUnanswered = "the user did not answer these questions: make reasonable assumptions and state them in your reply"

// ErrNeedsAnswer: a question is accepted only with its answers (AnswerQuestion).
var ErrNeedsAnswer = errors.New("a question needs its answers")

// questionOption is a choice of a question.
type questionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// question is one question with its choices; MultiSelect takes several answers.
type question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []questionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

// questionArgs are the tool's arguments; Answers, one list per question, are
// added by AnswerQuestion.
type questionArgs struct {
	Questions []question `json:"questions"`
	Answers   [][]string `json:"answers,omitempty"`
}

// registerQuestionTool offers ask_user_question to Ask runs.
func (s *Service) registerQuestionTool() {
	option := object(map[string]any{
		"label":       map[string]any{"type": "string", "description": "1-5 words."},
		"description": map[string]any{"type": "string", "description": "What choosing it means."},
	}, "label", "description")
	q := object(map[string]any{
		"question": map[string]any{"type": "string", "description": "The full question, ending with a question mark."},
		"header":   map[string]any{"type": "string", "description": "A very short label for it, at most 12 characters."},
		"options": map[string]any{"type": "array", "minItems": 2, "maxItems": 4, "items": option,
			"description": "2-4 mutually exclusive choices. Put the one you recommend first and end its label with \"(Recommended)\". Do not add an \"Other\" choice: the user can always answer in their own words."},
		"multiSelect": map[string]any{"type": "boolean", "description": "Whether the user may pick several options."},
	}, "question", "header", "options", "multiSelect")
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{Name: askQuestionTool,
		Description: "Ask the user 1-4 questions with choices when you need a decision only they can make, and wait for the answers. Do not ask what the mail or a sensible default already answers.",
		Schema:      object(map[string]any{"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": q}}, "questions")},
		Write: true, Exec: &executor{Prepare: prepareQuestion, Apply: applyQuestion}})
}

// prepareQuestion checks the questions; a bad call goes back to the model.
func prepareQuestion(_ context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
	var in questionArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return prepared{}, err
	}
	if len(in.Questions) < 1 || len(in.Questions) > 4 {
		return prepared{}, errors.New("ask 1-4 questions")
	}
	asked := map[string]bool{}
	for _, q := range in.Questions {
		if strings.TrimSpace(q.Question) == "" || asked[q.Question] {
			return prepared{}, errors.New("each question must be distinct and not empty")
		}
		asked[q.Question] = true
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return prepared{}, errors.New("give each question 2-4 options")
		}
		labels := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" || labels[o.Label] {
				return prepared{}, errors.New("each option of a question needs its own label")
			}
			labels[o.Label] = true
		}
	}
	in.Answers = nil
	b, err := json.Marshal(in)
	return prepared{Args: b, Summary: in.Questions[0].Question}, err
}

// applyQuestion tells the model the answers, each with its question.
func applyQuestion(_ context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in questionArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	if len(in.Answers) != len(in.Questions) {
		return nil, nil, nil, ErrNeedsAnswer
	}
	lines := []string{"The user answered your questions:"}
	for i, q := range in.Questions {
		lines = append(lines, fmt.Sprintf("%q=%q", q.Question, strings.Join(in.Answers[i], ", ")))
	}
	return nil, strings.Join(lines, "\n"), nil, nil
}

// AnswerQuestion accepts pending question id with answers, one list per
// question: one answer each, several where it is multiSelect. An answer is
// an option's label or the user's own words.
func (s *Service) AnswerQuestion(ctx context.Context, id int64, answers [][]string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	a, err := s.approvalRow(ctx, id)
	if err != nil {
		return err
	}
	if a.Tool != askQuestionTool {
		return fmt.Errorf("approval %d is not a question", id)
	}
	var in questionArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return err
	}
	if len(answers) != len(in.Questions) {
		return errors.New("answer every question")
	}
	for i, q := range in.Questions {
		for _, ans := range answers[i] {
			if strings.TrimSpace(ans) == "" {
				return errors.New("an answer is empty")
			}
		}
		if n := len(answers[i]); n == 0 || n > 1 && !q.MultiSelect {
			return fmt.Errorf("question %d takes one answer", i+1)
		}
	}
	in.Answers = answers
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return s.cleanError(s.decide(ctx, id, true, b, true))
}

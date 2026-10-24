package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/store"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP servers ask the user through multi round-trip requests (SEP-2322,
// protocol 2026-07-28): a call answers input_required with a form and an
// opaque state, and is called again with the user's answer and that state.
// The form waits as a pending approval (docs/research/extension-questions.md):
// answering it accepts the call again, as an approved external call. Nothing
// stays open meanwhile, so it survives a restart and needs no timeout.

const (
	maxMCPRounds   = 3  // forms one call may ask in a row
	maxFormFields  = 8  // fields of one form
	maxFormOptions = 50 // choices of one field
	maxFormBytes   = 16 << 10
)

// mcpInput is a call's form: the input request's ID, the form, the server's
// state, the round (1 for the call's first form), and once answered the
// user's answers (one list per field, as the card sent them) and the answer.
type mcpInput struct {
	ID      string            `json:"id"`
	Form    *mcp.ElicitParams `json:"form"`
	State   string            `json:"state"`
	Round   int               `json:"round"`
	Answers [][]string        `json:"answers,omitempty"`
	Answer  *mcp.ElicitResult `json:"answer,omitempty"`
}

// needsInput: the server asked the user in (callMCP); nothing ran.
type needsInput struct{ in mcpInput }

func (e *needsInput) Error() string { return "the MCP server asked the user a question" }

// mcpInputOf is res's form, after prev (nil for the call's first). Only one
// form per round is taken, and only a form: no URL, sampling or roots.
func mcpInputOf(res *mcp.CallToolResult, prev *mcpInput) (mcpInput, error) {
	round := 1
	if prev != nil {
		round = prev.Round + 1
	}
	switch {
	case len(res.InputRequests) == 0:
		return mcpInput{}, errors.New("the MCP server is busy; try again later")
	case round > maxMCPRounds:
		return mcpInput{}, fmt.Errorf("the MCP server asked more than %d questions in a row", maxMCPRounds)
	case len(res.InputRequests) > 1: // shortcut: one form per round; join them into one card if a server needs several
		return mcpInput{}, errors.New("the MCP server asked for several forms at once, which Godwit does not answer")
	}
	for id, r := range res.InputRequests {
		p, ok := r.(*mcp.ElicitParams)
		if !ok || cmp.Or(p.Mode, "form") != "form" {
			return mcpInput{}, errors.New("the MCP server asked for input Godwit does not give (only forms are answered)")
		}
		if b, err := json.Marshal(p); err != nil || len(b) > maxFormBytes {
			return mcpInput{}, errors.New("the MCP server's form is too large")
		}
		if _, _, err := formQuestions(p); err != nil {
			return mcpInput{}, err
		}
		return mcpInput{ID: id, Form: p, State: res.RequestState, Round: round}, nil
	}
	panic("unreachable")
}

// formQuestion is a form field as the question card shows it. Kind is
// choice (Options only), text, number, integer or boolean (no options: the
// card offers yes and no); values are the choices' values, by option.
type formQuestion struct {
	question
	Kind     string `json:"kind"`
	Required bool   `json:"required,omitempty"`
	key      string
	values   []any
}

// formQuestions are p's fields, required ones first, then by key: the schema
// arrives as a map, without its order. A field Godwit cannot show fails it.
func formQuestions(p *mcp.ElicitParams) ([]formQuestion, *jsonschema.Schema, error) {
	var sch *jsonschema.Schema
	b, err := json.Marshal(p.RequestedSchema)
	if err == nil {
		err = json.Unmarshal(b, &sch)
	}
	if err != nil || sch == nil || len(sch.Properties) == 0 {
		return nil, nil, errors.New("the MCP server's form has no fields")
	}
	if len(sch.Properties) > maxFormFields {
		return nil, nil, fmt.Errorf("the MCP server's form has more than %d fields", maxFormFields)
	}
	keys := make([]string, 0, len(sch.Properties))
	for k := range sch.Properties {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(-cmpBool(slices.Contains(sch.Required, a), slices.Contains(sch.Required, b)), strings.Compare(a, b))
	})
	out := make([]formQuestion, 0, len(keys))
	for _, k := range keys {
		ps := sch.Properties[k]
		if ps == nil {
			return nil, nil, fmt.Errorf("the MCP server's form field %q has no schema", k)
		}
		q := formQuestion{question: question{Question: cmp.Or(ps.Title, k), Description: ps.Description},
			Required: slices.Contains(sch.Required, k), key: k}
		choices := ps
		if ps.Type == "array" && ps.Items != nil {
			q.MultiSelect, choices = true, ps.Items
		}
		for _, v := range choices.Enum {
			q.Options, q.values = append(q.Options, questionOption{Label: fmt.Sprint(v)}), append(q.values, v)
		}
		for _, c := range append(choices.OneOf, choices.AnyOf...) {
			if c == nil || c.Const == nil {
				return nil, nil, fmt.Errorf("the MCP server's form field %q has a choice without a value", k)
			}
			q.Options, q.values = append(q.Options, questionOption{Label: cmp.Or(c.Title, fmt.Sprint(*c.Const))}), append(q.values, *c.Const)
		}
		switch {
		case len(q.Options) > 0:
			q.Kind = "choice"
		case q.MultiSelect:
			return nil, nil, fmt.Errorf("the MCP server's form field %q is a list without choices", k)
		case ps.Type == "string" || ps.Type == "number" || ps.Type == "integer" || ps.Type == "boolean":
			q.Kind = ps.Type
			if q.Kind == "string" {
				q.Kind = "text"
			}
		default:
			return nil, nil, fmt.Errorf("the MCP server's form field %q is not a field Godwit can show", k)
		}
		if len(q.Options) > maxFormOptions {
			return nil, nil, fmt.Errorf("the MCP server's form field %q has more than %d choices", k, maxFormOptions)
		}
		labels := map[string]bool{}
		for _, o := range q.Options {
			if labels[o.Label] {
				return nil, nil, fmt.Errorf("the MCP server's form field %q has two choices named %q", k, o.Label)
			}
			labels[o.Label] = true
		}
		out = append(out, q)
	}
	return out, sch, nil
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// formContent is the answer's content: answers, one list per field (a
// choice's label, the text, a number, "true" or "false"), typed as the
// schema has them and checked against it. An unanswered field is left out.
func formContent(qs []formQuestion, sch *jsonschema.Schema, answers [][]string) (map[string]any, error) {
	if len(answers) != len(qs) {
		return nil, errors.New("answer every question")
	}
	content := map[string]any{}
	for i, q := range qs {
		ans := answers[i]
		if len(ans) == 0 {
			if q.Required {
				return nil, fmt.Errorf("%q needs an answer", q.Question)
			}
			continue
		}
		if len(ans) > 1 && !q.MultiSelect {
			return nil, fmt.Errorf("%q takes one answer", q.Question)
		}
		var v any
		var err error
		switch q.Kind {
		case "text":
			v = ans[0]
		case "number":
			v, err = strconv.ParseFloat(strings.TrimSpace(ans[0]), 64)
		case "integer":
			var n int64
			n, err = strconv.ParseInt(strings.TrimSpace(ans[0]), 10, 64)
			if n > 1<<53 || n < -(1<<53) { // beyond what a float64 holds exactly: sent, it would be another number
				err = errors.New("too large")
			}
			v = float64(n) // as JSON decodes it, which the schema check expects
		case "boolean":
			v, err = strconv.ParseBool(ans[0])
		case "choice":
			picked := make([]any, 0, len(ans))
			for _, a := range ans {
				j := slices.IndexFunc(q.Options, func(o questionOption) bool { return o.Label == a })
				if j < 0 {
					return nil, fmt.Errorf("%q takes one of its choices", q.Question)
				}
				picked = append(picked, q.values[j])
			}
			v = picked[0]
			if q.MultiSelect {
				v = picked
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%q needs a %s", q.Question, q.Kind)
		}
		content[q.key] = v
	}
	r, err := sch.Resolve(nil)
	if err == nil {
		err = r.Validate(content)
	}
	if err != nil {
		return nil, fmt.Errorf("the answers do not fit the MCP server's form: %w", err)
	}
	return content, nil
}

// formView is what the question card shows of an MCP form: who asks, what it
// says, its fields, and once answered the answers.
type formView struct {
	Server    string         `json:"server"`
	Round     int            `json:"round"`
	Message   string         `json:"message"`
	Questions []formQuestion `json:"questions"`
	Answers   [][]string     `json:"answers,omitempty"`
}

// form is a's form as the card shows it, nil without one.
func (a approvalRow) form() json.RawMessage {
	var in mcpInput
	if a.input == "" || json.Unmarshal([]byte(a.input), &in) != nil || in.Form == nil {
		return nil
	}
	qs, _, err := formQuestions(in.Form)
	if err != nil {
		return nil
	}
	b, _ := json.Marshal(formView{Server: a.label, Round: in.Round, Message: in.Form.Message, Questions: qs, Answers: in.Answers})
	return b
}

// unanswered: a is an MCP form not answered yet; only answerForm accepts it.
func (a approvalRow) unanswered() bool {
	var in mcpInput
	return a.input != "" && (json.Unmarshal([]byte(a.input), &in) != nil || in.Answer == nil)
}

// askMCP interrupts a trusted tool's in-run call for its form: it becomes an
// approval, as an untrusted call's would, that answering it accepts.
func (s *Service) askMCP(ctx context.Context, srv MCPServer, hash string, args json.RawMessage, in mcpInput, summary string) error {
	if runOf(ctx).conv.Kind == "assist" {
		return errNoFormHost
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	runOf(ctx).prepared.Store(ai.CallID(ctx), prepared{Args: args, Summary: summary, Binding: fmt.Sprintf("%d:%s", srv.Version, hash),
		Destinations: []string{"mcp:" + srv.Name}, source: gateway.SourceMCP, extension: strconv.FormatInt(srv.ID, 10), Input: b})
	return ai.ErrInterrupt
}

// nullText is b as text, NULL when empty.
func nullText(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// errNoFormHost: the compose window shows no MCP form yet (it answers no
// questions), so a form asked in an assist run ends the call. shortcut: drop
// it once the compose window answers questions (godwit-email/godwit#206).
var errNoFormHost = errors.New("the MCP server asked the user a question, which only Wit's own window can show: ask there")

// askAgain puts a claimed approval back to pending with the server's new form
// and tells the conversation's followers, whose run stays interrupted: the
// card shows again (the panel tells the round from the one it answered).
// ok is false where no form can show (errNoFormHost): the caller fails it.
func (s *Service) askAgain(ctx context.Context, a approvalRow, in mcpInput) (ok bool, err error) {
	rows, err := s.db.Query(ctx, "SELECT kind FROM conversations WHERE id = ?", a.ConversationID)
	if err != nil || len(rows.Values) == 0 || store.Str(rows.Values[0][0]) == "assist" {
		return false, err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return false, err
	}
	if _, err = s.db.Transaction(ctx, []sqlite.Statement{{SQL: "UPDATE approvals SET state = 'pending', input = ?, updated_at = ? WHERE id = ? AND state = 'executing'",
		Args: []any{string(b), time.Now().Unix(), a.ID}}, s.auditRow(a, "input_required")}); err != nil {
		return false, err
	}
	if as, err := s.approvals(ctx, "a.id = ?", a.ID); err == nil && len(as) == 1 {
		s.publishWit(a.ConversationID, WitEvent{ConversationID: a.ConversationID, RunID: a.RunID, Turn: s.runTurn(ctx, a.RunID), Ends: true,
			Event: ai.Event{Kind: "interrupted", Interrupts: []ai.ToolCall{{ID: a.CallID, Name: a.Tool, Args: a.Args}}}, Approvals: as})
	}
	return true, nil
}

// answerForm answers a's form: every answer empty rejects the call (the
// server is not called again); else it is called again with the answer.
func (s *Service) answerForm(ctx context.Context, a approvalRow, round int, answers [][]string) error {
	var in mcpInput
	if err := json.Unmarshal([]byte(a.input), &in); err != nil {
		return err
	}
	if in.Round != round { // the user saw an earlier form: its answer is not this one's
		return ErrInvalidState
	}
	qs, sch, err := formQuestions(in.Form)
	if err != nil {
		return err
	}
	skipped := !slices.ContainsFunc(answers, func(l []string) bool { return len(l) > 0 })
	if !skipped {
		content, err := formContent(qs, sch, answers)
		if err != nil {
			return err
		}
		in.Answer = &mcp.ElicitResult{Action: "accept", Content: content}
	}
	in.Answers = answers
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	// only the form read, answered or skipped: an answer to an earlier round, or a second
	// answer, changes nothing (it never decides a form the call asked since)
	r, err := s.db.Execute(ctx, "UPDATE approvals SET input = ?, updated_at = ? WHERE id = ? AND state = 'pending' AND input = ?",
		string(b), time.Now().Unix(), a.ID, a.input)
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return ErrInvalidState
	}
	return s.decide(ctx, a.ID, !skipped, nil, true)
}

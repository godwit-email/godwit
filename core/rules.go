package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

// Mail rules (docs/adr/0030-mail-rules.md): a rule is a versioned JSON
// document, its fixed constants stable English ids and its labels slugs, so
// the UI language never reaches it. The rule consumer reads events_log after
// its cursor and runs each enabled rule once per message: the run row goes
// in first, under UNIQUE(rule_id, message_id), then each action in its own
// transaction with its result, so a redelivery resumes only unfinished ones.

// RuleVersion is the rule document version SaveRule writes.
const RuleVersion = 1

// Rule document ids.
const (
	TriggerReceive = "receive"

	MatchAll = "all"
	MatchAny = "any"

	FieldFrom    = "from"
	FieldSubject = "subject"

	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpIs          = "is"

	ActionLabel = "label"
)

// Rule limits: the document stays a list of choices, not a language.
const (
	MaxRuleName       = 100
	MaxRuleConditions = 20
	MaxRuleActions    = 20
	MaxRuleValue      = 500
)

// RuleDoc is a rule as stored: when (Trigger), if (Match of Conditions; none
// matches every message), then (Actions, in order).
type RuleDoc struct {
	Version    int             `json:"version"`
	Name       string          `json:"name"`
	Trigger    string          `json:"trigger"`
	Match      string          `json:"match"`
	Conditions []RuleCondition `json:"conditions"`
	Actions    []RuleAction    `json:"actions"`
}

// RuleCondition compares a field of the message with Value (typed by the user).
type RuleCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// RuleAction is one step; Label is a label's slug (ActionLabel).
type RuleAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

// Rule is a stored rule, in the order rules run.
type Rule struct {
	ID      int64
	Enabled bool
	Doc     RuleDoc
}

// ErrInvalidRule is a rule document SaveRule refuses.
var ErrInvalidRule = errors.New("invalid rule")

func invalidRule(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRule, fmt.Sprintf(format, args...))
}

// validRule checks d against the allowlists; labels must exist.
func (s *Service) validRule(ctx context.Context, d RuleDoc) error {
	if d.Version != RuleVersion {
		return invalidRule("version %d", d.Version)
	}
	if utf8.RuneCountInString(d.Name) > MaxRuleName {
		return invalidRule("name longer than %d", MaxRuleName)
	}
	if d.Trigger != TriggerReceive {
		return invalidRule("trigger %q", d.Trigger)
	}
	if d.Match != MatchAll && d.Match != MatchAny {
		return invalidRule("match %q", d.Match)
	}
	if len(d.Conditions) > MaxRuleConditions {
		return invalidRule("more than %d conditions", MaxRuleConditions)
	}
	for _, c := range d.Conditions {
		switch {
		case c.Field != FieldFrom && c.Field != FieldSubject:
			return invalidRule("field %q", c.Field)
		case c.Op != OpContains && c.Op != OpNotContains && c.Op != OpIs:
			return invalidRule("op %q", c.Op)
		case strings.TrimSpace(c.Value) == "":
			return invalidRule("empty value")
		case utf8.RuneCountInString(c.Value) > MaxRuleValue:
			return invalidRule("value longer than %d", MaxRuleValue)
		}
	}
	if len(d.Actions) == 0 || len(d.Actions) > MaxRuleActions {
		return invalidRule("%d actions", len(d.Actions))
	}
	for _, a := range d.Actions {
		switch a.Kind {
		case ActionLabel:
			rows, err := s.db.Query(ctx, "SELECT 1 FROM labels WHERE slug = ?", a.Label)
			if err != nil {
				return err
			}
			if len(rows.Values) == 0 {
				return invalidRule("label %q", a.Label)
			}
		default:
			return invalidRule("action %q", a.Kind)
		}
	}
	return nil
}

// Rules returns the rules in the order they run.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	return s.rules(ctx, false)
}

func (s *Service) rules(ctx context.Context, enabledOnly bool) ([]Rule, error) {
	rows, err := s.db.Query(ctx, "SELECT id, enabled, doc FROM rules WHERE enabled >= ? ORDER BY position, id", b2i(enabledOnly))
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows.Values))
	for _, v := range rows.Values {
		r := Rule{ID: store.Int(v[0]), Enabled: store.Int(v[1]) != 0}
		if err := json.Unmarshal([]byte(store.Str(v[2])), &r.Doc); err != nil {
			return nil, fmt.Errorf("rule %d: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// SaveRule validates r and stores it: a new one (ID 0) after the others.
// It returns the rule's id.
func (s *Service) SaveRule(ctx context.Context, r Rule) (int64, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	r.Doc.Name = strings.TrimSpace(r.Doc.Name)
	if err := s.validRule(ctx, r.Doc); err != nil {
		return 0, err
	}
	doc, err := json.Marshal(r.Doc)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	if r.ID == 0 {
		res, err := s.db.Execute(ctx, `INSERT INTO rules(position, enabled, doc, created_at, updated_at)
			SELECT COALESCE(MAX(position), -1) + 1, ?, ?, ?, ? FROM rules`, b2i(r.Enabled), string(doc), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertID, nil
	}
	res, err := s.db.Execute(ctx, "UPDATE rules SET enabled = ?, doc = ?, updated_at = ? WHERE id = ?", b2i(r.Enabled), string(doc), now, r.ID)
	if err != nil {
		return 0, err
	}
	if res.Changes == 0 {
		return 0, fmt.Errorf("rule %d not found", r.ID)
	}
	return r.ID, nil
}

// SetRuleEnabled turns a rule on or off; an off rule keeps its setup and runs on nothing.
func (s *Service) SetRuleEnabled(ctx context.Context, id int64, on bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	return s.exec(ctx, sqlite.Statement{SQL: "UPDATE rules SET enabled = ?, updated_at = ? WHERE id = ?", Args: []any{b2i(on), time.Now().Unix(), id}})
}

// ruleMail is what conditions read of a message.
type ruleMail struct{ from, subject string }

// matches evaluates the conditions; none matches every message. Text is
// compared without case.
func (d RuleDoc) matches(m ruleMail) bool {
	if len(d.Conditions) == 0 {
		return true
	}
	for _, c := range d.Conditions {
		if c.matches(m) == (d.Match == MatchAny) {
			return d.Match == MatchAny
		}
	}
	return d.Match == MatchAll
}

func (c RuleCondition) matches(m ruleMail) bool {
	want := strings.ToLower(strings.TrimSpace(c.Value))
	var got []string // the field's values; "is" holds when one equals want
	switch c.Field {
	case FieldFrom:
		got = []string{strings.ToLower(m.from)}
		if a, err := mail.ParseAddress(m.from); err == nil {
			got = append(got, strings.ToLower(a.Address))
		}
	case FieldSubject:
		got = []string{strings.ToLower(m.subject)}
	}
	switch c.Op {
	case OpContains:
		return strings.Contains(got[0], want)
	case OpNotContains:
		return !strings.Contains(got[0], want)
	case OpIs:
		for _, g := range got {
			if strings.TrimSpace(g) == want {
				return true
			}
		}
	}
	return false
}

// startRules runs the rule consumer until Close.
func (s *Service) startRules(ctx context.Context) error {
	return s.startConsumer(ctx, "rules", func(ctx context.Context, ev hookEvent) error {
		if rulesHook != nil && rulesHook("rules.deliver") != nil {
			return errStopConsumer // tests: a crash before the rules run
		}
		if err := s.runRules(ctx, ev); err != nil {
			return err
		}
		if rulesHook != nil && rulesHook("rules.ran") != nil {
			return errStopConsumer // tests: a crash before the cursor moves
		}
		return nil
	})
}

// rulesHook, if set, is called at each stage of a delivery (tests); an error stops the consumer there.
var rulesHook func(stage string) error

// runRules runs the enabled receive rules, in order, on a new message. Mail
// that lands in Sent or Drafts is not received mail.
func (s *Service) runRules(ctx context.Context, ev hookEvent) error {
	if ev.deleted {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT COALESCE(f.role, '') FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?`, ev.messageID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	if role := store.Str(rows.Values[0][0]); role == "sent" || role == "drafts" {
		return nil
	}
	rules, err := s.rules(ctx, true)
	if err != nil {
		return err
	}
	m := ruleMail{from: ev.from, subject: ev.subject}
	for _, r := range rules {
		if r.Doc.Trigger != TriggerReceive || !r.Doc.matches(m) {
			continue
		}
		if err := s.runRule(ctx, r, ev.messageID, TriggerReceive); err != nil {
			return err
		}
	}
	return nil
}

// runRule writes the run, if there is none yet, then runs its actions not
// done yet, stopping at the first that fails. An error is the database's:
// the event is delivered again and the run resumes.
func (s *Service) runRule(ctx context.Context, r Rule, msgID int64, trigger string) error {
	if _, err := s.db.Execute(ctx, `INSERT INTO rule_runs(rule_id, message_id, trigger, state, at) VALUES (?, ?, ?, 'running', ?)
		ON CONFLICT(rule_id, message_id) DO NOTHING`, r.ID, msgID, trigger, time.Now().Unix()); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT id, state, (SELECT COALESCE(MAX(idx) + 1, 0) FROM rule_action_results WHERE run_id = rule_runs.id AND state = 'done')
		FROM rule_runs WHERE rule_id = ? AND message_id = ?`, r.ID, msgID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	runID, from := store.Int(rows.Values[0][0]), int(store.Int(rows.Values[0][2]))
	if store.Str(rows.Values[0][1]) != "running" {
		return nil
	}
	state := "done"
	for i := from; i < len(r.Doc.Actions); i++ {
		a := r.Doc.Actions[i]
		stmts, failure, err := s.ruleAction(ctx, a, msgID)
		if err != nil {
			return err
		}
		result := sqlite.Statement{SQL: `INSERT OR REPLACE INTO rule_action_results(run_id, idx, kind, state, prior, result, at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, Args: []any{runID, i, a.Kind, "done", stmts.prior, nil, time.Now().Unix()}}
		if failure != "" {
			result.Args[3], result.Args[5] = "failed", failure
		}
		if _, err := s.db.Transaction(ctx, append(stmts.writes, result)); err != nil {
			return err
		}
		if failure != "" {
			state = "failed"
			break
		}
	}
	if err := s.exec(ctx, sqlite.Statement{SQL: "UPDATE rule_runs SET state = ? WHERE id = ?", Args: []any{state, runID}}); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return nil
}

// actionWrites is an action's statements and the prior state they change
// (JSON, for undo).
type actionWrites struct {
	writes []sqlite.Statement
	prior  any
}

// ruleAction prepares a; failure is why it cannot run (the run stops there).
func (s *Service) ruleAction(ctx context.Context, a RuleAction, msgID int64) (actionWrites, string, error) {
	switch a.Kind {
	case ActionLabel:
		rows, err := s.db.Query(ctx, `SELECT l.id, ml.source, ml.active FROM labels l
			LEFT JOIN message_labels ml ON ml.label_id = l.id AND ml.message_id = ? WHERE l.slug = ?`, msgID, a.Label)
		if err != nil {
			return actionWrites{}, "", err
		}
		if len(rows.Values) == 0 {
			return actionWrites{}, "label not found", nil
		}
		v := rows.Values[0]
		prior := "null" // no row
		if v[1] != nil {
			b, _ := json.Marshal(map[string]any{"source": store.Str(v[1]), "active": store.Int(v[2]) != 0})
			prior = string(b)
		}
		// A hand-set row wins, a label taken off by hand included (ADR 0014).
		return actionWrites{prior: prior, writes: []sqlite.Statement{{SQL: `INSERT INTO message_labels(message_id, label_id, source, active)
			VALUES (?, ?, 'rule', 1) ON CONFLICT(message_id, label_id) DO UPDATE SET source = 'rule', active = 1 WHERE source != 'manual'`,
			Args: []any{msgID, store.Int(v[0])}}}}, "", nil
	}
	return actionWrites{}, "unknown action", nil
}

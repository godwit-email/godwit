package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strconv"
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
	TriggerReceive  = "receive"
	TriggerLabel    = "label"    // TriggerArg is the label's slug
	TriggerSecurity = "security" // TriggerArg is SecuritySuspicious or SecurityDangerous
	TriggerDaily    = "daily"

	SecuritySuspicious = "suspicious" // needs_review or warning
	SecurityDangerous  = "dangerous"  // warning

	MatchAll = "all"
	MatchAny = "any"

	FieldFrom        = "from"
	FieldTo          = "to" // To and Cc
	FieldSubject     = "subject"
	FieldBody        = "body"
	FieldAttachments = "attachments" // is RuleYes or RuleNo
	FieldLabel       = "label"       // is or not_contains a label's slug
	FieldFolder      = "folder"      // is or not_contains a folder role or a folder's id

	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpIs          = "is"

	RuleYes = "yes"
	RuleNo  = "no"

	// The move actions are named after the role of the folder they move to.
	ActionLabel   = "label"
	ActionUnlabel = "unlabel"
	ActionRead    = "read"
	ActionStar    = "star"
	ActionArchive = "archive"
	ActionJunk    = "junk"
	ActionTrash   = "trash"
	ActionStop    = "stop" // later rules do not run on the message

	// The file actions write to Dir (rulefiles.go); they cannot be undone.
	ActionPDF         = "pdf"
	ActionEML         = "eml"
	ActionAttachments = "attachments"
)

// Run and action states, and why an action failed.
const (
	RuleRunRunning = "running"
	RuleRunDone    = "done"
	RuleRunFailed  = "failed"
	RuleRunUndone  = "undone"

	RuleActionDone   = "done"
	RuleActionFailed = "failed"
	RuleActionNotRun = "not_run" // after a failed one
	RuleActionUndone = "undone"
	RuleActionKept   = "kept" // not undone: its target changed since

	RuleFailNoFolder = "no_folder"
	RuleFailNoLabel  = "no_label"
	RuleFailGone     = "gone" // the message
	RuleFailUnknown  = "unknown"

	RuleFailNoDestination = "no_destination" // the file action's folder is not there
	RuleFailNoPDF         = "no_pdf"         // nothing renders PDFs here (headless)
	RuleFailUnavailable   = "unavailable"    // the mail could not be read from the server
	RuleFailTooLarge      = "too_large"
	RuleFailWrite         = "write_failed"
)

// ruleFolderRoles are the roles a folder condition may name.
var ruleFolderRoles = []string{"inbox", "archive", "junk", "trash"}

// ruleFlags are the flag each flag action sets.
var ruleFlags = map[string]string{ActionRead: `\Seen`, ActionStar: `\Flagged`}

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
	TriggerArg string          `json:"trigger_arg,omitempty"`
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

// RuleAction is one step; Label is a label's slug (ActionLabel). A file
// action writes to Dir, an absolute path, into a subfolder per message when
// Subfolder is set; Naming, a 命名模板, names that subfolder and the PDF or
// EML. File actions with one Dir share Subfolder and Naming.
type RuleAction struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Subfolder bool   `json:"subfolder,omitempty"`
	Naming    string `json:"naming,omitempty"`
}

// Rule is a stored rule, in the order rules run. Background keeps Godwit
// running after the window closes (运行方式 后台运行).
type Rule struct {
	ID         int64
	Enabled    bool
	Background bool
	Doc        RuleDoc
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
	switch d.Trigger {
	case TriggerReceive, TriggerDaily:
		if d.TriggerArg != "" {
			return invalidRule("trigger_arg %q", d.TriggerArg)
		}
	case TriggerLabel:
		if err := s.labelExists(ctx, d.TriggerArg); err != nil {
			return err
		}
	case TriggerSecurity:
		if d.TriggerArg != SecuritySuspicious && d.TriggerArg != SecurityDangerous {
			return invalidRule("trigger_arg %q", d.TriggerArg)
		}
	default:
		return invalidRule("trigger %q", d.Trigger)
	}
	if d.Match != MatchAll && d.Match != MatchAny {
		return invalidRule("match %q", d.Match)
	}
	if len(d.Conditions) > MaxRuleConditions {
		return invalidRule("more than %d conditions", MaxRuleConditions)
	}
	for _, c := range d.Conditions {
		if err := s.validCondition(ctx, c); err != nil {
			return err
		}
	}
	if len(d.Actions) == 0 || len(d.Actions) > MaxRuleActions {
		return invalidRule("%d actions", len(d.Actions))
	}
	for _, a := range d.Actions {
		switch a.Kind {
		case ActionLabel, ActionUnlabel:
			if err := s.labelExists(ctx, a.Label); err != nil {
				return err
			}
		case ActionRead, ActionStar, ActionArchive, ActionJunk, ActionTrash, ActionStop:
		case ActionPDF, ActionEML, ActionAttachments:
			if err := validFileAction(d.Actions, a); err != nil {
				return err
			}
		default:
			return invalidRule("action %q", a.Kind)
		}
	}
	return nil
}

func (s *Service) validCondition(ctx context.Context, c RuleCondition) error {
	switch {
	case strings.TrimSpace(c.Value) == "":
		return invalidRule("empty value")
	case utf8.RuneCountInString(c.Value) > MaxRuleValue:
		return invalidRule("value longer than %d", MaxRuleValue)
	}
	switch c.Field {
	case FieldFrom, FieldTo, FieldSubject, FieldBody:
		if c.Op != OpContains && c.Op != OpNotContains && c.Op != OpIs {
			return invalidRule("op %q", c.Op)
		}
		return nil
	case FieldAttachments:
		if c.Op != OpIs || (c.Value != RuleYes && c.Value != RuleNo) {
			return invalidRule("attachments %s %q", c.Op, c.Value)
		}
		return nil
	case FieldLabel, FieldFolder:
		if c.Op != OpIs && c.Op != OpNotContains {
			return invalidRule("%s op %q", c.Field, c.Op)
		}
		if c.Field == FieldLabel {
			return s.labelExists(ctx, c.Value)
		}
		if slices.Contains(ruleFolderRoles, c.Value) {
			return nil
		}
		id, err := strconv.ParseInt(c.Value, 10, 64)
		if err != nil {
			return invalidRule("folder %q", c.Value)
		}
		rows, err := s.db.Query(ctx, "SELECT 1 FROM folders WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(rows.Values) == 0 {
			return invalidRule("folder %q", c.Value)
		}
		return nil
	}
	return invalidRule("field %q", c.Field)
}

func (s *Service) labelExists(ctx context.Context, slug string) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM labels WHERE slug = ?", slug)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return invalidRule("label %q", slug)
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
	rows, err := s.db.Query(ctx, "SELECT id, enabled, doc, background FROM rules WHERE enabled >= ? ORDER BY position, id", b2i(enabledOnly))
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows.Values))
	for _, v := range rows.Values {
		r := Rule{ID: store.Int(v[0]), Enabled: store.Int(v[1]) != 0, Background: store.Int(v[3]) != 0}
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
		res, err := s.db.Execute(ctx, `INSERT INTO rules(position, enabled, background, doc, created_at, updated_at)
			SELECT COALESCE(MAX(position), -1) + 1, ?, ?, ?, ?, ? FROM rules`, b2i(r.Enabled), b2i(r.Background), string(doc), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertID, nil
	}
	res, err := s.db.Execute(ctx, "UPDATE rules SET enabled = ?, background = ?, doc = ?, updated_at = ? WHERE id = ?",
		b2i(r.Enabled), b2i(r.Background), string(doc), now, r.ID)
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

// RulesInBackground reports whether an enabled rule runs in the background,
// so closing the window keeps Godwit running.
func (s *Service) RulesInBackground(ctx context.Context) (bool, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return false, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT 1 FROM rules WHERE enabled = 1 AND background = 1 LIMIT 1")
	if err != nil {
		return false, err
	}
	return len(rows.Values) > 0, nil
}

// ruleMail is what conditions read of a message.
type ruleMail struct {
	from, subject, body string
	to                  []string // To and Cc
	attachments         bool
	labels              []string // the slugs of the labels on it
	folderID            int64
	folderRole          string
}

// ruleMail reads the message as it is now; ok is false when it is gone.
func (s *Service) ruleMail(ctx context.Context, id int64) (m ruleMail, ok bool, err error) {
	rows, err := s.db.Query(ctx, `SELECT m.from_addr, m.subject, m.body_text, m.to_addrs, m.cc_addrs, m.has_attachments, m.folder_id, COALESCE(f.role, ''),
		(SELECT json_group_array(l.slug) FROM message_labels ml JOIN labels l ON l.id = ml.label_id WHERE ml.message_id = m.id AND ml.active = 1)
		FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?`, id)
	if err != nil || len(rows.Values) == 0 {
		return m, false, err
	}
	v := rows.Values[0]
	return ruleMail{from: store.Str(v[0]), subject: store.Str(v[1]), body: store.Str(v[2]),
		to: append(jsonList(store.Str(v[3])), jsonList(store.Str(v[4]))...), attachments: store.Int(v[5]) != 0,
		folderID: store.Int(v[6]), folderRole: store.Str(v[7]), labels: jsonList(store.Str(v[8]))}, true, nil
}

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
	var got []string // the field's values; the op holds on one of them
	switch c.Field {
	case FieldFrom:
		got = addrValues(m.from)
	case FieldTo:
		for _, a := range m.to {
			got = append(got, addrValues(a)...)
		}
	case FieldSubject:
		got = []string{strings.ToLower(m.subject)}
	case FieldBody:
		got = []string{strings.ToLower(m.body)}
	case FieldAttachments:
		return m.attachments == (c.Value == RuleYes)
	case FieldLabel:
		return slices.Contains(m.labels, c.Value) != (c.Op == OpNotContains)
	case FieldFolder:
		in := c.Value == m.folderRole || c.Value == strconv.FormatInt(m.folderID, 10)
		return in != (c.Op == OpNotContains)
	}
	hit := slices.ContainsFunc(got, func(g string) bool {
		if c.Op == OpIs {
			return strings.TrimSpace(g) == want
		}
		return strings.Contains(g, want)
	})
	return hit != (c.Op == OpNotContains)
}

// addrValues is an address and its bare address, lower-cased, so "is" holds on either.
func addrValues(s string) []string {
	out := []string{strings.ToLower(s)}
	if a, err := mail.ParseAddress(s); err == nil {
		out = append(out, strings.ToLower(a.Address))
	}
	return out
}

// startRules runs the rule consumer until Close.
func (s *Service) startRules(ctx context.Context) error {
	return s.startConsumer(ctx, "rules", ruleEvents, func(ctx context.Context, ev hookEvent) error {
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

// The events rules run on, logged in the transaction that makes them true:
// new mail at import, a label put on by hand (SetLabel) or by the classifier
// (labelWrites; never by a rule, so a label rule fires one level deep), the
// first verdict on new mail (import or scanner; never a re-scan), and the
// day's first sync (logDaily).
const (
	eventLabelApplied    = "label.applied"
	eventSecurityVerdict = "security.verdict"
	eventDaily           = "rules.daily"
)

var ruleEvents = []string{"message.received", eventLabelApplied, eventSecurityVerdict, eventDaily}

// eventTrigger is the trigger each event runs.
var eventTrigger = map[string]string{"message.received": TriggerReceive, eventLabelApplied: TriggerLabel, eventSecurityVerdict: TriggerSecurity}

// firesOn reports whether the rule's trigger takes the event's detail.
func (d RuleDoc) firesOn(detail string) bool {
	switch d.Trigger {
	case TriggerLabel:
		return d.TriggerArg == detail
	case TriggerSecurity:
		return detail == "warning" || detail == "needs_review" && d.TriggerArg == SecuritySuspicious
	}
	return true
}

// logDaily logs the day's rules.daily event on its first sync (local day)
// with an enabled daily rule, keeping the day in meta in the same transaction.
func (s *Service) logDaily(ctx context.Context) error {
	today := s.clk().Now().Format(time.DateOnly)
	due := `EXISTS (SELECT 1 FROM rules WHERE enabled = 1 AND json_extract(doc, '$.trigger') = 'daily')
		AND NOT EXISTS (SELECT 1 FROM meta WHERE key = 'rules_daily' AND value = ?)`
	_, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "INSERT INTO events_log(type, created_at) SELECT ?, unixepoch() WHERE " + due, Args: []any{eventDaily, today}},
		{SQL: "INSERT INTO meta(key, value) SELECT 'rules_daily', ? WHERE " + due + " ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			Args: []any{today, today}},
	})
	return err
}

// runDaily runs the enabled daily rules, in order, over the mail there is;
// a stop keeps the later ones off that message.
// shortcut: reads every message once per rule; compile conditions to SQL
// (as the editor preview will) once mailboxes are large.
func (s *Service) runDaily(ctx context.Context) error {
	rules, err := s.rules(ctx, true)
	if err != nil {
		return err
	}
	rules = slices.DeleteFunc(rules, func(r Rule) bool { return r.Doc.Trigger != TriggerDaily })
	if len(rules) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT m.id FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE COALESCE(f.role, '') NOT IN ('sent', 'drafts') ORDER BY m.id`)
	if err != nil {
		return err
	}
	stopped := map[int64]bool{}
	for _, r := range rules {
		for _, v := range rows.Values {
			id := store.Int(v[0])
			if stopped[id] {
				continue
			}
			m, ok, err := s.ruleMail(ctx, id)
			if err != nil {
				return err
			}
			if !ok || !r.Doc.matches(m) {
				continue
			}
			if err := s.runRule(ctx, r, id, TriggerDaily); err != nil {
				return err
			}
			if _, stopped[id], err = s.ruleRun(ctx, r.ID, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// runRules runs the enabled rules of the event's trigger, in order, on its
// message. Mail that lands in Sent or Drafts is not received mail. Each rule
// reads the message as the rules before it left it; one already run on it
// resumes.
func (s *Service) runRules(ctx context.Context, ev hookEvent) error {
	if ev.typ == eventDaily {
		return s.runDaily(ctx)
	}
	if ev.deleted {
		return nil
	}
	m, ok, err := s.ruleMail(ctx, ev.messageID)
	if err != nil || !ok || m.folderRole == "sent" || m.folderRole == "drafts" {
		return err
	}
	rules, err := s.rules(ctx, true)
	if err != nil {
		return err
	}
	trigger := eventTrigger[ev.typ]
	for _, r := range rules {
		if r.Doc.Trigger != trigger || !r.Doc.firesOn(ev.detail) {
			continue
		}
		ran, _, err := s.ruleRun(ctx, r.ID, ev.messageID)
		if err != nil {
			return err
		}
		if !ran && !r.Doc.matches(m) {
			continue
		}
		if err := s.runRule(ctx, r, ev.messageID, trigger); err != nil {
			return err
		}
		if _, stopped, err := s.ruleRun(ctx, r.ID, ev.messageID); err != nil || stopped {
			return err
		}
		if m, ok, err = s.ruleMail(ctx, ev.messageID); err != nil || !ok {
			return err
		}
	}
	return nil
}

// ruleRun reports whether the rule has a run on the message, and whether
// its stop action ran.
func (s *Service) ruleRun(ctx context.Context, ruleID, msgID int64) (ran, stopped bool, err error) {
	rows, err := s.db.Query(ctx, `SELECT EXISTS(SELECT 1 FROM rule_action_results WHERE run_id = r.id AND kind = ? AND state = ?)
		FROM rule_runs r WHERE rule_id = ? AND message_id = ?`, ActionStop, RuleActionDone, ruleID, msgID)
	if err != nil || len(rows.Values) == 0 {
		return false, false, err
	}
	return true, store.Int(rows.Values[0][0]) != 0, nil
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
	if store.Str(rows.Values[0][1]) != RuleRunRunning {
		return nil
	}
	state := RuleRunDone
	for i := from; i < len(r.Doc.Actions); i++ {
		// The action reads the message and writes it, as an edit does; a
		// file action only reads it, maybe from the server, so it goes unlocked.
		file := slices.Contains(RuleFileActions, r.Doc.Actions[i].Kind)
		if !file {
			s.editMu.Lock()
		}
		failed, err := s.runAction(ctx, runID, r.Doc.Actions, i, msgID)
		if !file {
			s.editMu.Unlock()
		}
		if err != nil {
			return err
		}
		if failed {
			state = RuleRunFailed
			break
		}
		if rulesHook != nil && rulesHook("rules.action") != nil {
			return errStopConsumer // tests: a crash between two actions
		}
	}
	if err := s.exec(ctx, sqlite.Statement{SQL: "UPDATE rule_runs SET state = ? WHERE id = ?", Args: []any{state, runID}}); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return nil
}

// runAction runs acts[i] on the message, its result written in the same
// transaction; a failure also writes the actions after it as not run.
func (s *Service) runAction(ctx context.Context, runID int64, acts []RuleAction, i int, msgID int64) (failed bool, err error) {
	acc, t, ok, err := s.ruleTarget(ctx, msgID)
	if err != nil {
		return false, err
	}
	w, failure := actionWrites{}, RuleFailGone
	var written any // the paths a file action wrote, as JSON
	switch {
	case ok && slices.Contains(RuleFileActions, acts[i].Kind):
		var paths []string
		if paths, failure, err = s.ruleFiles(ctx, runID, acts[i], msgID); err != nil {
			return false, err
		}
		b, err := json.Marshal(append([]string{}, paths...))
		if err != nil {
			return false, err
		}
		written = string(b)
	case ok:
		if w, failure, err = s.ruleAction(ctx, acts[i], acc, t); err != nil {
			return false, err
		}
	}
	result := func(j int, state string, prior, failure any) sqlite.Statement {
		return sqlite.Statement{SQL: `INSERT OR REPLACE INTO rule_action_results(run_id, idx, kind, state, prior, result, at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, Args: []any{runID, j, acts[j].Kind, state, prior, failure, time.Now().Unix()}}
	}
	stmts := w.writes
	if failure == "" {
		prior, err := json.Marshal(w.prior)
		if err != nil {
			return false, err
		}
		stmts = append(stmts, result(i, RuleActionDone, string(prior), written))
	} else {
		stmts = append(stmts, result(i, RuleActionFailed, nil, failure))
		for j := i + 1; j < len(acts); j++ {
			stmts = append(stmts, result(j, RuleActionNotRun, nil, nil))
		}
	}
	if !ok {
		_, err := s.db.Transaction(ctx, stmts)
		return true, err
	}
	return failure != "", s.apply(ctx, acc, []target{t}, stmts)
}

// ruleTarget loads the message an action works on; ok is false when it is gone.
func (s *Service) ruleTarget(ctx context.Context, id int64) (acc int64, t target, ok bool, err error) {
	rows, err := s.db.Query(ctx, "SELECT account_id, thread_id, folder_id, flags FROM messages WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return 0, t, false, err
	}
	v := rows.Values[0]
	return store.Int(v[0]), target{id: id, thread: store.Int(v[1]), folder: store.Int(v[2]), flags: store.Str(v[3])}, true, nil
}

// actionWrites is an action's statements and the prior state they change.
type actionWrites struct {
	writes []sqlite.Statement
	prior  rulePrior
}

// rulePrior is what an action found, for undo (JSON in rule_action_results):
// the label's row (Source "" for none), whether the flag was set, the folder
// it moved the message from and to.
type rulePrior struct {
	LabelID int64  `json:"label_id,omitempty"`
	Source  string `json:"source,omitempty"`
	Active  bool   `json:"active,omitempty"`
	Had     bool   `json:"had,omitempty"`
	From    int64  `json:"from,omitempty"`
	To      int64  `json:"to,omitempty"`
}

func hasFlag(flags, f string) bool {
	return slices.ContainsFunc(strings.Fields(flags), func(g string) bool { return strings.EqualFold(g, f) })
}

// ruleAction prepares a on the message; failure, a RuleFail id, is why it
// cannot run (the run stops there).
func (s *Service) ruleAction(ctx context.Context, a RuleAction, acc int64, t target) (actionWrites, string, error) {
	switch a.Kind {
	case ActionLabel, ActionUnlabel:
		rows, err := s.db.Query(ctx, `SELECT l.id, ml.source, ml.active FROM labels l
			LEFT JOIN message_labels ml ON ml.label_id = l.id AND ml.message_id = ? WHERE l.slug = ?`, t.id, a.Label)
		if err != nil {
			return actionWrites{}, "", err
		}
		if len(rows.Values) == 0 {
			return actionWrites{}, RuleFailNoLabel, nil
		}
		v := rows.Values[0]
		p := rulePrior{LabelID: store.Int(v[0])}
		if v[1] != nil {
			p.Source, p.Active = store.Str(v[1]), store.Int(v[2]) != 0
		}
		// A hand-set row wins, a label taken off by hand included (ADR 0014).
		sql := `INSERT INTO message_labels(message_id, label_id, source, active)
			VALUES (?, ?, 'rule', 1) ON CONFLICT(message_id, label_id) DO UPDATE SET source = 'rule', active = 1 WHERE source != 'manual'`
		if a.Kind == ActionUnlabel {
			sql = "UPDATE message_labels SET source = 'rule', active = 0 WHERE message_id = ? AND label_id = ? AND active = 1 AND source != 'manual'"
		}
		return actionWrites{prior: p, writes: []sqlite.Statement{{SQL: sql, Args: []any{t.id, p.LabelID}}}}, "", nil
	case ActionRead, ActionStar:
		p := rulePrior{Had: hasFlag(t.flags, ruleFlags[a.Kind])}
		if p.Had {
			return actionWrites{prior: p}, "", nil
		}
		stmts, err := flagWrites(acc, []target{t}, []string{ruleFlags[a.Kind]}, nil)
		return actionWrites{prior: p, writes: stmts}, "", err
	case ActionArchive, ActionJunk, ActionTrash:
		to, err := s.roleFolder(ctx, acc, a.Kind)
		if _, none := errors.AsType[*ErrNoRoleFolder](err); none {
			return actionWrites{}, RuleFailNoFolder, nil
		}
		if err != nil {
			return actionWrites{}, "", err
		}
		_, stmts := moveWrites(acc, []target{t}, to)
		return actionWrites{prior: rulePrior{From: t.folder, To: to}, writes: stmts}, "", nil
	case ActionStop:
		return actionWrites{}, "", nil
	}
	return actionWrites{}, RuleFailUnknown, nil
}

// RuleHitDays is how far back RuleHits looks.
const RuleHitDays = 7

// RuleHit is a rule's run on a message, its actions' results in order.
type RuleHit struct {
	RunID     int64
	MessageID int64 // 0: the message is gone
	From      string
	Subject   string
	At        time.Time
	State     string // a RuleRun state
	Actions   []RuleActionResult
}

// RuleActionResult is an action's outcome; Failure (a RuleFail id) is why it
// failed, Paths the files a file action wrote.
type RuleActionResult struct {
	Kind, State, Failure string
	Paths                []string
}

// RuleHits returns the rule's runs of the last RuleHitDays days, newest first.
func (s *Service) RuleHits(ctx context.Context, ruleID int64) ([]RuleHit, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, `SELECT r.id, r.message_id, m.from_addr, m.subject, r.at, r.state, a.kind, a.state, a.result
		FROM rule_runs r LEFT JOIN messages m ON m.id = r.message_id LEFT JOIN rule_action_results a ON a.run_id = r.id
		WHERE r.rule_id = ? AND r.at >= ? ORDER BY r.at DESC, r.id DESC, a.idx`, ruleID, time.Now().AddDate(0, 0, -RuleHitDays).Unix())
	if err != nil {
		return nil, err
	}
	var out []RuleHit
	for _, v := range rows.Values {
		if id := store.Int(v[0]); len(out) == 0 || out[len(out)-1].RunID != id {
			out = append(out, RuleHit{RunID: id, MessageID: store.Int(v[1]), From: store.Str(v[2]), Subject: store.Str(v[3]),
				At: time.Unix(store.Int(v[4]), 0), State: store.Str(v[5])})
		}
		if v[6] != nil {
			h := &out[len(out)-1]
			res := RuleActionResult{Kind: store.Str(v[6]), State: store.Str(v[7])}
			if res.State == RuleActionFailed {
				res.Failure = store.Str(v[8])
			} else if v[8] != nil {
				_ = json.Unmarshal([]byte(store.Str(v[8])), &res.Paths) // only file actions have one
			}
			h.Actions = append(h.Actions, res)
		}
	}
	return out, nil
}

// RetryRuleRun runs a failed run again from the action that failed, in the
// same run, with the rule as it is now.
func (s *Service) RetryRuleRun(ctx context.Context, runID int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, `SELECT r.rule_id, r.message_id, r.trigger, ru.doc FROM rule_runs r JOIN rules ru ON ru.id = r.rule_id
		WHERE r.id = ? AND r.state = ? AND r.message_id IS NOT NULL`, runID, RuleRunFailed)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("rule run %d: nothing to retry", runID)
	}
	v := rows.Values[0]
	r := Rule{ID: store.Int(v[0])}
	if err := json.Unmarshal([]byte(store.Str(v[3])), &r.Doc); err != nil {
		return fmt.Errorf("rule %d: %w", r.ID, err)
	}
	if err := s.exec(ctx, sqlite.Statement{SQL: "UPDATE rule_runs SET state = ? WHERE id = ?", Args: []any{RuleRunRunning, runID}}); err != nil {
		return err
	}
	return s.runRule(ctx, r, store.Int(v[1]), store.Str(v[2]))
}

// UndoRuleRun puts back what the run's local actions changed, last first.
// One whose target changed since (moved on, the flag or label changed by
// hand) is kept as it is; kept counts them. The checks and the writes hold
// the edit lock together, as UndoArchive's do.
func (s *Service) UndoRuleRun(ctx context.Context, runID int64) (kept int, err error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	rows, err := s.db.Query(ctx, `SELECT a.idx, a.kind, a.prior, r.message_id FROM rule_runs r JOIN rule_action_results a ON a.run_id = r.id
		WHERE r.id = ? AND a.state = ? AND a.kind NOT IN (?, ?, ?, ?) ORDER BY a.idx DESC`, runID, RuleActionDone, ActionStop, ActionPDF, ActionEML, ActionAttachments)
	if err != nil {
		return 0, err
	}
	for _, v := range rows.Values {
		var p rulePrior
		if err := json.Unmarshal([]byte(store.Str(v[2])), &p); err != nil {
			return kept, err
		}
		undone, err := s.undoAction(ctx, runID, int(store.Int(v[0])), store.Str(v[1]), p, store.Int(v[3]))
		if err != nil {
			return kept, err
		}
		if !undone {
			kept++
		}
	}
	if err := s.exec(ctx, sqlite.Statement{SQL: "UPDATE rule_runs SET state = ? WHERE id = ?", Args: []any{RuleRunUndone, runID}}); err != nil {
		return kept, err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return kept, nil
}

// undoAction undoes the action at idx, if its target is still as it left it.
func (s *Service) undoAction(ctx context.Context, runID int64, idx int, kind string, p rulePrior, msgID int64) (bool, error) {
	mark := func(state string) sqlite.Statement {
		return sqlite.Statement{SQL: "UPDATE rule_action_results SET state = ? WHERE run_id = ? AND idx = ?", Args: []any{state, runID, idx}}
	}
	acc, t, ok, err := s.ruleTarget(ctx, msgID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, s.exec(ctx, mark(RuleActionKept))
	}
	var stmts []sqlite.Statement
	switch kind {
	case ActionLabel, ActionUnlabel:
		on := kind == ActionLabel
		if p.Source == "manual" || (on && p.Source == "rule" && p.Active) || (!on && (p.Source == "" || !p.Active)) {
			break // the action changed nothing
		}
		rows, err := s.db.Query(ctx, "SELECT 1 FROM message_labels WHERE message_id = ? AND label_id = ? AND source = 'rule' AND active = ?", t.id, p.LabelID, b2i(on))
		if err != nil {
			return false, err
		}
		if ok = len(rows.Values) > 0; !ok {
			break
		}
		if p.Source == "" {
			stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM message_labels WHERE message_id = ? AND label_id = ?", Args: []any{t.id, p.LabelID}})
		} else {
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE message_labels SET source = ?, active = ? WHERE message_id = ? AND label_id = ?",
				Args: []any{p.Source, b2i(p.Active), t.id, p.LabelID}})
		}
	case ActionRead, ActionStar:
		if p.Had {
			break
		}
		if ok = hasFlag(t.flags, ruleFlags[kind]); ok {
			if stmts, err = flagWrites(acc, []target{t}, nil, []string{ruleFlags[kind]}); err != nil {
				return false, err
			}
		}
	case ActionArchive, ActionJunk, ActionTrash:
		if p.From == p.To {
			break
		}
		rows, err := s.db.Query(ctx, "SELECT 1 FROM folders WHERE id = ?", p.From)
		if err != nil {
			return false, err
		}
		if ok = t.folder == p.To && len(rows.Values) > 0; ok {
			_, stmts = moveWrites(acc, []target{t}, p.From)
		}
	}
	state := RuleActionUndone
	if !ok {
		state = RuleActionKept
	}
	return ok, s.apply(ctx, acc, []target{t}, append(stmts, mark(state)))
}

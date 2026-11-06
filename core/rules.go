package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	gomail "github.com/godwit-email/godwit/core/mail"
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
	FieldAge         = "age"         // older_than a number of whole days, by the message's date

	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpIs          = "is"
	OpOlderThan   = "older_than"

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
	ActionBlock   = "block"
	ActionStop    = "stop" // later rules do not run on the message
	// ActionForward forwards the message to RuleAction.To once the user
	// confirmed it (Rule.Confirmed); egress, so new mail only (newMailTriggers).
	ActionForward = "forward"

	// The file actions write to Dir (rulefiles.go); they cannot be undone.
	ActionPDF         = "pdf"
	ActionEML         = "eml"
	ActionAttachments = "attachments"

	// The AI actions send the message to the model, masked, and only while
	// Settings.RuleAI is on; new mail only (existingActions). draft_reply
	// saves a reply draft, never sent; key_points writes a file as the file
	// actions do.
	ActionDraftReply = "draft_reply"
	ActionKeyPoints  = "key_points"
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
	// RuleActionSkipped is a forward on mail it never forwards: not new
	// (a daily or one-time run), or forwarded by a rule (mail.RuleHeader).
	RuleActionSkipped = "skipped"

	RuleFailNoFolder    = "no_folder"
	RuleFailNoLabel     = "no_label"
	RuleFailGone        = "gone" // the message
	RuleFailUnknown     = "unknown"
	RuleFailBlock       = "block"
	RuleFailUnconfirmed = "unconfirmed" // the forward's recipient is not in Rule.Confirmed
	RuleFailSendCheck   = "send_check"  // a plugin warned about or blocked the forward
	RuleFailSend        = "send"        // the forward could not be queued

	RuleFailNoDestination = "no_destination" // the file action's folder is not there
	RuleFailNoPDF         = "no_pdf"         // nothing renders PDFs here (headless)
	RuleFailUnavailable   = "unavailable"    // the mail could not be read from the server
	RuleFailTooLarge      = "too_large"
	RuleFailWrite         = "write_failed"
	RuleFailUnsafeFolder  = "unsafe_folder" // a subfolder level in the destination is a symlink or not a folder
	RuleFailAIOff         = "ai_off"        // Settings.RuleAI is off
	RuleFailModel         = "model"         // no model, or it did not answer
)

// newMailTriggers are the triggers a forward and a Wit action run on: new
// mail, or a message just labeled or judged. On a daily run's or an
// existing-mail run's older mail they are skipped: neither sends old mail
// out unasked.
var newMailTriggers = []string{TriggerReceive, TriggerLabel, TriggerSecurity}

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

// RuleDoc is a rule as stored: when (Trigger, on mail of Account, an
// account's id; 0 every account), if (Match of Conditions; none matches
// every message), then (Actions, in order).
type RuleDoc struct {
	Version    int             `json:"version"`
	Name       string          `json:"name"`
	Trigger    string          `json:"trigger"`
	TriggerArg string          `json:"trigger_arg,omitempty"`
	Account    int64           `json:"account,omitempty"`
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

// RuleAction is one step; Label is a label's slug (ActionLabel), To an
// address (ActionForward). A file action writes to Dir, an absolute path,
// into a subfolder per message when Subfolder is set; Naming, a 命名模板,
// names that subfolder and the PDF or EML. File actions with one Dir share
// Subfolder and Naming.
type RuleAction struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	To        string `json:"to,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Subfolder bool   `json:"subfolder,omitempty"`
	Naming    string `json:"naming,omitempty"`
}

// Rule is a stored rule, in the order rules run. Background keeps Godwit
// running after the window closes (运行方式 后台运行). Confirmed are the
// forward recipients the user confirmed in Godwit, lower-cased bare
// addresses, kept apart from Doc: a forward to any other does not send.
type Rule struct {
	ID         int64
	Enabled    bool
	Background bool
	Doc        RuleDoc
	Confirmed  []string
	since      int64 // the events_log seq it was saved or turned on at: it starts runs on later events only
}

// rcptKey is a forward recipient as Rule.Confirmed holds it, "" if invalid.
func rcptKey(s string) string {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return ""
	}
	return strings.ToLower(a.Address)
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
	if d.Account != 0 {
		rows, err := s.db.Query(ctx, "SELECT 1 FROM accounts WHERE id = ?", d.Account)
		if err != nil {
			return err
		}
		if len(rows.Values) == 0 {
			return invalidRule("account %d", d.Account)
		}
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
	if slices.ContainsFunc(d.Actions, func(a RuleAction) bool { return slices.Contains(RuleAIActions, a.Kind) }) {
		if on, err := s.ruleAIOn(ctx); err != nil || !on {
			return cmp.Or(err, invalidRule("AI actions are turned off"))
		}
	}
	for _, a := range d.Actions {
		switch a.Kind {
		case ActionLabel, ActionUnlabel:
			if err := s.labelExists(ctx, a.Label); err != nil {
				return err
			}
		case ActionForward:
			if rcptKey(a.To) == "" {
				return invalidRule("forward to %q", a.To)
			}
		case ActionRead, ActionStar, ActionArchive, ActionJunk, ActionTrash, ActionBlock, ActionStop:
		case ActionPDF, ActionEML, ActionAttachments, ActionKeyPoints:
			if err := validFileAction(d.Actions, a); err != nil {
				return err
			}
		case ActionDraftReply:
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
	case FieldAge:
		if n, err := strconv.Atoi(c.Value); c.Op != OpOlderThan || err != nil || n < 1 {
			return invalidRule("age %s %q", c.Op, c.Value)
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
	rows, err := s.db.Query(ctx, "SELECT id, enabled, doc, background, confirmed_rcpts, since_seq FROM rules WHERE enabled >= ? ORDER BY position, id", b2i(enabledOnly))
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows.Values))
	for _, v := range rows.Values {
		r := Rule{ID: store.Int(v[0]), Enabled: store.Int(v[1]) != 0, Background: store.Int(v[3]) != 0, Confirmed: jsonList(store.Str(v[4])), since: store.Int(v[5])}
		if err := json.Unmarshal([]byte(store.Str(v[2])), &r.Doc); err != nil {
			return nil, fmt.Errorf("rule %d: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// SaveRule validates r and stores it: a new one (ID 0) after the others.
// Of r.Confirmed it keeps the recipients the rule forwards to: the caller
// passes only those the user confirmed in Godwit, with the address shown.
// It returns the rule's id.
func (s *Service) SaveRule(ctx context.Context, r Rule) (int64, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	st, err := s.ruleWrite(ctx, r)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Execute(ctx, st.SQL, st.Args...)
	if err != nil {
		return 0, err
	}
	if r.ID == 0 {
		return res.LastInsertID, nil
	}
	if res.Changes == 0 {
		return 0, fmt.Errorf("rule %d not found", r.ID)
	}
	return r.ID, nil
}

// ruleWrite is SaveRule's statement for r, validated.
func (s *Service) ruleWrite(ctx context.Context, r Rule) (sqlite.Statement, error) {
	r.Doc.Name = strings.TrimSpace(r.Doc.Name)
	if err := s.validRule(ctx, r.Doc); err != nil {
		return sqlite.Statement{}, err
	}
	doc, err := json.Marshal(r.Doc)
	if err != nil {
		return sqlite.Statement{}, err
	}
	confirmed := []string{}
	for _, k := range forwardRecipients(r.Doc) {
		if slices.ContainsFunc(r.Confirmed, func(c string) bool { return rcptKey(c) == k }) {
			confirmed = append(confirmed, k)
		}
	}
	now := time.Now().Unix()
	// Saved, it runs on what comes after: not on mail still in the backlog.
	if r.ID == 0 {
		return sqlite.Statement{SQL: `INSERT INTO rules(position, enabled, background, doc, confirmed_rcpts, created_at, updated_at, since_seq)
			SELECT COALESCE(MAX(position), -1) + 1, ?, ?, ?, ?, ?, ?, ` + lastSeq + ` FROM rules`,
			Args: []any{b2i(r.Enabled), b2i(r.Background), string(doc), encodeList(confirmed), now, now}}, nil
	}
	return sqlite.Statement{SQL: "UPDATE rules SET enabled = ?, background = ?, doc = ?, confirmed_rcpts = ?, updated_at = ?, since_seq = " + lastSeq + " WHERE id = ?",
		Args: []any{b2i(r.Enabled), b2i(r.Background), string(doc), encodeList(confirmed), now, r.ID}}, nil
}

// lastSeq is the newest event's seq, a rule's since_seq as it is saved.
const lastSeq = "(SELECT COALESCE(MAX(seq), 0) FROM events_log)"

// forwardRecipients are the addresses d forwards to, as Rule.Confirmed holds them.
func forwardRecipients(d RuleDoc) []string {
	var out []string
	for _, a := range d.Actions {
		if k := rcptKey(a.To); a.Kind == ActionForward && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// SetRuleEnabled turns a rule on or off; an off rule keeps its setup and runs on nothing.
func (s *Service) SetRuleEnabled(ctx context.Context, id int64, on bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	// Turned on, it runs on what comes after, not on mail that came while it was off.
	return s.exec(ctx, sqlite.Statement{SQL: "UPDATE rules SET since_seq = CASE WHEN ? AND enabled = 0 THEN " + lastSeq + " ELSE since_seq END, enabled = ?, updated_at = ? WHERE id = ?",
		Args: []any{b2i(on), b2i(on), time.Now().Unix(), id}})
}

// ReorderRules puts the rules in the order of ids, which names every rule
// once: rules run top to bottom, and a stop action skips those below.
func (s *Service) ReorderRules(ctx context.Context, ids []int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT id FROM rules")
	if err != nil {
		return err
	}
	have := make([]int64, len(rows.Values))
	for i, v := range rows.Values {
		have[i] = store.Int(v[0])
	}
	want := slices.Clone(ids)
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		return errors.New("the new order must name every rule once")
	}
	stmts := make([]sqlite.Statement, len(ids))
	for i, id := range ids {
		stmts[i] = sqlite.Statement{SQL: "UPDATE rules SET position = ? WHERE id = ?", Args: []any{i, id}}
	}
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

// DeleteRule deletes a rule with its hits (rule_runs cascade); files it
// wrote and mail it forwarded stay.
func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	res, err := s.db.Execute(ctx, "DELETE FROM rules WHERE id = ?", id)
	if err == nil && res.Changes == 0 {
		err = fmt.Errorf("rule %d not found", id)
	}
	return err
}

// FirstTimeRecipients are the addresses none of the accounts has written
// to, for the confirmation a rule's forward recipient needs.
func (s *Service) FirstTimeRecipients(ctx context.Context, addrs []string) ([]string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	accs, err := s.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, acc := range accs {
		if len(addrs) == 0 {
			break
		}
		if addrs, err = s.firstTime(ctx, acc, Draft{To: addrs}); err != nil {
			return nil, err
		}
	}
	return addrs, nil
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
	account             int64
	to                  []string // To and Cc
	attachments         bool
	labels              []string // the slugs of the labels on it
	folderID            int64
	folderRole          string
	date, now           int64 // unix seconds: the message's date, and when it was read
}

// ruleMail reads the message as it is now; ok is false when it is gone.
func (s *Service) ruleMail(ctx context.Context, id int64) (m ruleMail, ok bool, err error) {
	rows, err := s.db.Query(ctx, `SELECT m.from_addr, m.subject, m.body_text, m.to_addrs, m.cc_addrs, m.has_attachments, m.folder_id, COALESCE(f.role, ''),
		(SELECT json_group_array(l.slug) FROM message_labels ml JOIN labels l ON l.id = ml.label_id WHERE ml.message_id = m.id AND ml.active = 1), m.date, m.account_id
		FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?`, id)
	if err != nil || len(rows.Values) == 0 {
		return m, false, err
	}
	v := rows.Values[0]
	return ruleMail{from: store.Str(v[0]), subject: store.Str(v[1]), body: store.Str(v[2]),
		to: append(jsonList(store.Str(v[3])), jsonList(store.Str(v[4]))...), attachments: store.Int(v[5]) != 0,
		folderID: store.Int(v[6]), folderRole: store.Str(v[7]), labels: jsonList(store.Str(v[8])),
		date: store.Int(v[9]), account: store.Int(v[10]), now: s.clk().Now().Unix()}, true, nil
}

// matches evaluates the account and the conditions; none matches every
// message. Text is compared without case.
func (d RuleDoc) matches(m ruleMail) bool {
	if d.Account != 0 && m.account != d.Account {
		return false
	}
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
	case FieldAge:
		n, _ := strconv.Atoi(c.Value)
		return m.date < m.now-int64(n)*86400
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

// startRules runs the rule consumer until Close, and resumes the runs a
// close or a crash left unfinished.
func (s *Service) startRules(ctx context.Context) error {
	if rctx, end, err := s.begin(context.Background(), 0); err == nil {
		go func() {
			defer end()
			if err := s.resumeRuns(rctx); err != nil && rctx.Err() == nil {
				log.Printf("rules: resuming unfinished runs: %v", err)
			}
		}()
	}
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
func (s *Service) runDaily(ctx context.Context, seq int64) error {
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
			// A run there is resumes from its saved actions, as runRules does: the
			// ones done may have moved the mail off the conditions.
			ran, _, err := s.ruleRun(ctx, r.ID, id)
			if err != nil {
				return err
			}
			if !ran {
				if seq <= r.since { // the day's event came before the rule was saved
					continue
				}
				m, ok, err := s.ruleMail(ctx, id)
				if err != nil {
					return err
				}
				if !ok || !r.Doc.matches(m) {
					continue
				}
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

// resumeRuns resumes the enabled rules' runs left running. No event brings
// back a run over existing mail or a retry, which ran outside the consumer
// (a retry's event is long past); a new mail's the consumer may resume too,
// which then finds it done (one run at a time). Each goes on from its saved
// actions with its own trigger, whether its message still matches or not.
func (s *Service) resumeRuns(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT rule_id, message_id, trigger FROM rule_runs WHERE state = ? AND message_id IS NOT NULL ORDER BY id", RuleRunRunning)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	rules, err := s.rules(ctx, true)
	if err != nil {
		return err
	}
	for _, v := range rows.Values {
		if i := slices.IndexFunc(rules, func(r Rule) bool { return r.ID == store.Int(v[0]) }); i >= 0 {
			if err := s.runRule(ctx, rules[i], store.Int(v[1]), store.Str(v[2])); err != nil {
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
		return s.runDaily(ctx, ev.seq)
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
		// A new run only on an event after the rule was saved or turned on: mail
		// that came before it, still in the backlog, is not run on (nor forwarded).
		if !ran && (ev.seq <= r.since || !r.Doc.matches(m)) {
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
// the event is delivered again and the run resumes. One runs at a time, so
// a run the consumer, a retry and a run over existing mail all reach does
// each action once.
func (s *Service) runRule(ctx context.Context, r Rule, msgID int64, trigger string) error {
	s.ruleRunMu.Lock()
	defer s.ruleRunMu.Unlock()
	if trigger == TriggerExisting { // retries too: never a forward or Wit action
		r.Doc.Actions = slices.DeleteFunc(slices.Clone(r.Doc.Actions), func(a RuleAction) bool { return !RunsOnExisting(a.Kind) })
	}
	doc, err := json.Marshal(r.Doc)
	if err != nil {
		return err
	}
	// A new run only while the rule is on as it is now, not as the caller read
	// it: one turned off while an earlier rule's run was going starts none.
	if _, err := s.db.Execute(ctx, `INSERT INTO rule_runs(rule_id, message_id, trigger, state, at, doc)
		SELECT ?, ?, ?, 'running', ?, ? WHERE EXISTS (SELECT 1 FROM rules WHERE id = ? AND enabled = 1)
		ON CONFLICT(rule_id, message_id) DO NOTHING`, r.ID, msgID, trigger, time.Now().Unix(), string(doc), r.ID); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT id, state, (SELECT COALESCE(MAX(idx) + 1, 0) FROM rule_action_results WHERE run_id = rule_runs.id AND state = 'done'), doc
		FROM rule_runs WHERE rule_id = ? AND message_id = ?`, r.ID, msgID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	runID, from := store.Int(rows.Values[0][0]), int(store.Int(rows.Values[0][2]))
	if store.Str(rows.Values[0][1]) != RuleRunRunning {
		return nil
	}
	if v := rows.Values[0][3]; v != nil { // the result indexes are into the actions as the run started
		r.Doc = RuleDoc{}
		if err := json.Unmarshal([]byte(store.Str(v)), &r.Doc); err != nil {
			return fmt.Errorf("rule run %d: %w", runID, err)
		}
	}
	state := RuleRunDone
	for i := from; i < len(r.Doc.Actions); i++ {
		var failed bool
		switch kind := r.Doc.Actions[i].Kind; {
		case kind == ActionForward:
			failed, err = s.runForward(ctx, r, runID, i, msgID, trigger) // sending takes the edit lock itself
		case slices.Contains(RuleAIActions, kind) && !slices.Contains(newMailTriggers, trigger): // it would send old mail to the model
			err = s.exec(ctx, ruleResult(runID, r.Doc.Actions, i, RuleActionSkipped, nil, nil))
		case slices.Contains(RuleFileActions, kind), kind == ActionDraftReply:
			failed, err = s.runAction(ctx, runID, r.Doc.Actions, i, msgID) // it only reads the message, maybe from the server or a model
		default:
			s.editMu.Lock() // the action reads the message and writes it, as an edit does
			failed, err = s.runAction(ctx, runID, r.Doc.Actions, i, msgID)
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
	if acts[i].Kind == ActionBlock {
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
	}
	acc, t, ok, err := s.ruleTarget(ctx, msgID)
	if err != nil {
		return false, err
	}
	w, failure := actionWrites{}, RuleFailGone
	var written any // the paths a file action wrote, as JSON
	aiOff := false
	if ok && slices.Contains(RuleAIActions, acts[i].Kind) {
		on, err := s.ruleAIOn(ctx)
		if err != nil {
			return false, err
		}
		aiOff = !on
	}
	switch {
	case aiOff:
		failure = RuleFailAIOff
	case ok && acts[i].Kind == ActionDraftReply:
		if failure, err = s.ruleDraftReply(ctx, acc, msgID); err != nil {
			return false, err
		}
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
	stmts := w.writes
	if failure == "" {
		prior, err := json.Marshal(w.prior)
		if err != nil {
			return false, err
		}
		stmts = append(stmts, ruleResult(runID, acts, i, RuleActionDone, string(prior), written))
	} else {
		stmts = append(stmts, failedResults(runID, acts, i, failure)...)
	}
	if !ok {
		_, err := s.db.Transaction(ctx, stmts)
		return true, err
	}
	err = s.apply(ctx, acc, []target{t}, stmts)
	if err == nil && acts[i].Kind == ActionBlock && len(w.writes) > 0 {
		s.bus.Publish(events.Event{Type: events.SettingsChanged})
	}
	return failure != "", err
}

// ruleResult writes acts[j]'s result.
func ruleResult(runID int64, acts []RuleAction, j int, state string, prior, failure any) sqlite.Statement {
	return sqlite.Statement{SQL: `INSERT OR REPLACE INTO rule_action_results(run_id, idx, kind, state, prior, result, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, Args: []any{runID, j, acts[j].Kind, state, prior, failure, time.Now().Unix()}}
}

// failedResults writes acts[i] as failed for failure and the actions after it as not run.
func failedResults(runID int64, acts []RuleAction, i int, failure string) []sqlite.Statement {
	stmts := []sqlite.Statement{ruleResult(runID, acts, i, RuleActionFailed, nil, failure)}
	for j := i + 1; j < len(acts); j++ {
		stmts = append(stmts, ruleResult(runID, acts, j, RuleActionNotRun, nil, nil))
	}
	return stmts
}

// ruleSend is a rule's forward for enqueue: the marker header names the
// rule, and stmts (its result and audit) are written with the outbox item.
type ruleSend struct {
	ruleID int64
	stmts  []sqlite.Statement
}

// runForward forwards the message, with its attachments, to the action's
// recipient through the outbox, as a send of the user's would go. The
// result and the audit row are written with the outbox item, so a crash
// leaves all or none of them and the run resumed forwards at most once. A
// recipient not confirmed, or any plugin warning (nobody is there to read
// it), fails the action instead.
func (s *Service) runForward(ctx context.Context, r Rule, runID int64, i int, msgID int64, trigger string) (failed bool, err error) {
	acts, to := r.Doc.Actions, r.Doc.Actions[i].To
	fail := func(failure string) (bool, error) {
		_, err := s.db.Transaction(ctx, failedResults(runID, acts, i, failure))
		return true, err
	}
	rows, err := s.db.Query(ctx, "SELECT account_id, rule_sent FROM messages WHERE id = ?", msgID)
	if err != nil {
		return false, err
	}
	switch {
	case len(rows.Values) == 0:
		return fail(RuleFailGone)
	case !slices.Contains(newMailTriggers, trigger) || store.Int(rows.Values[0][1]) != 0:
		return false, s.exec(ctx, ruleResult(runID, acts, i, RuleActionSkipped, nil, nil))
	case !slices.Contains(r.Confirmed, rcptKey(to)):
		return fail(RuleFailUnconfirmed)
	}
	d, err := s.NewDraft(ctx, store.Int(rows.Values[0][0]), "forward", msgID)
	if err == nil {
		d.To = []string{to}
		if d, err = s.SaveDraft(ctx, d); err != nil {
			err = errors.Join(err, s.deleteDraft(context.WithoutCancel(ctx), d.ID))
		}
	}
	if err == nil {
		audit := gateway.Audit(gateway.Entry{Source: AuditRule, Tool: ActionForward, TargetKind: "rule", TargetID: r.ID,
			Destinations: []string{to}, Decision: "allow"}, s.secrets.Redact)
		rs := &ruleSend{ruleID: r.ID, stmts: []sqlite.Statement{ruleResult(runID, acts, i, RuleActionDone, "{}", nil), audit}}
		if err = s.send(ctx, d.ID, -1, rs); err != nil {
			err = errors.Join(err, s.deleteDraft(context.WithoutCancel(ctx), d.ID))
		}
	}
	var warned *SendWarningsError
	var blocked *SendBlockedError
	switch {
	case err == nil:
		return false, nil
	case ctx.Err() != nil:
		return false, err // closing: the run resumes
	case errors.As(err, &warned), errors.As(err, &blocked):
		return fail(RuleFailSendCheck)
	}
	slog.Warn("rules: forward not queued", "rule", r.ID, "err", err)
	return fail(RuleFailSend)
}

// ruleTarget loads the message an action works on; ok is false when it is gone.
func (s *Service) ruleTarget(ctx context.Context, id int64) (acc int64, t target, ok bool, err error) {
	rows, err := s.db.Query(ctx, "SELECT account_id, thread_id, folder_id, flags, from_addr FROM messages WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return 0, t, false, err
	}
	v := rows.Values[0]
	return store.Int(v[0]), target{id: id, thread: store.Int(v[1]), folder: store.Int(v[2]), flags: store.Str(v[3]), from: store.Str(v[4])}, true, nil
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
	LabelID  int64  `json:"label_id,omitempty"`
	Source   string `json:"source,omitempty"`
	Active   bool   `json:"active,omitempty"`
	Had      bool   `json:"had,omitempty"`
	From     int64  `json:"from,omitempty"`
	To       int64  `json:"to,omitempty"`
	FromAddr string `json:"from_addr,omitempty"`
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
	case ActionBlock:
		addr := gomail.AddrKey(t.from)
		blocked, err := s.readList(ctx, blockedSenders)
		if err != nil {
			return actionWrites{}, "", err
		}
		p := rulePrior{Had: slices.Contains(blocked, addr), FromAddr: addr}
		if p.Had {
			return actionWrites{prior: p}, "", nil
		}
		blocked, err = addBlockedSender(blocked, addr)
		if err != nil {
			return actionWrites{}, RuleFailBlock, nil
		}
		stmt, err := listWrite(blockedSenders, blocked)
		return actionWrites{prior: p, writes: []sqlite.Statement{stmt}}, "", err
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
// failed, Paths the files a file action wrote. Action is the action as the
// run started (its recipient, label, folder), the rule since edited or not;
// zero for a run from before runs kept it.
type RuleActionResult struct {
	Kind, State, Failure string
	Paths                []string
	Action               RuleAction
}

// RuleHits returns the rule's runs of the last RuleHitDays days, newest first.
func (s *Service) RuleHits(ctx context.Context, ruleID int64) ([]RuleHit, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, `SELECT r.id, r.message_id, m.from_addr, m.subject, r.at, r.state, a.kind, a.state, a.result, a.idx, r.doc
		FROM rule_runs r LEFT JOIN messages m ON m.id = r.message_id LEFT JOIN rule_action_results a ON a.run_id = r.id
		WHERE r.rule_id = ? AND r.at >= ? ORDER BY r.at DESC, r.id DESC, a.idx`, ruleID, time.Now().AddDate(0, 0, -RuleHitDays).Unix())
	if err != nil {
		return nil, err
	}
	var out []RuleHit
	var doc RuleDoc // the run's, as it started
	for _, v := range rows.Values {
		if id := store.Int(v[0]); len(out) == 0 || out[len(out)-1].RunID != id {
			out = append(out, RuleHit{RunID: id, MessageID: store.Int(v[1]), From: store.Str(v[2]), Subject: store.Str(v[3]),
				At: time.Unix(store.Int(v[4]), 0), State: store.Str(v[5])})
			doc = RuleDoc{}
			if v[10] != nil {
				_ = json.Unmarshal([]byte(store.Str(v[10])), &doc) // one that does not read names the action by its kind
			}
		}
		if v[6] != nil {
			h := &out[len(out)-1]
			res := RuleActionResult{Kind: store.Str(v[6]), State: store.Str(v[7])}
			if i := int(store.Int(v[9])); i < len(doc.Actions) && doc.Actions[i].Kind == res.Kind {
				res.Action = doc.Actions[i]
			}
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

// ruleUndoable are the actions an undo puts back: the local ones. A forward
// has gone, files written and drafts made stay, and a stop changed nothing.
var ruleUndoable = []string{ActionLabel, ActionUnlabel, ActionRead, ActionStar, ActionArchive, ActionJunk, ActionTrash, ActionBlock}

// RuleUndoable reports whether UndoRuleRun puts back an action of kind, for
// the hit history's 撤销, which shows only for a run with one done.
func RuleUndoable(kind string) bool { return slices.Contains(ruleUndoable, kind) }

// RetryRuleRun runs a failed run again from the action that failed, in the
// same run, with the actions as the run started and the rule's settings
// (confirmed recipients) as they are now. A disabled rule's run is not retried.
func (s *Service) RetryRuleRun(ctx context.Context, runID int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, `SELECT r.rule_id, r.message_id, r.trigger FROM rule_runs r
		WHERE r.id = ? AND r.state = ? AND r.message_id IS NOT NULL`, runID, RuleRunFailed)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("rule run %d: nothing to retry", runID)
	}
	v := rows.Values[0]
	rules, err := s.rules(ctx, true)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rules, func(r Rule) bool { return r.ID == store.Int(v[0]) })
	if i < 0 {
		return fmt.Errorf("rule run %d: its rule is off", runID)
	}
	r := rules[i]
	// Claimed failed → running: of two retries at once (a double click), only
	// the one that claimed it runs the actions, a forward among them.
	res, err := s.db.Execute(ctx, "UPDATE rule_runs SET state = ? WHERE id = ? AND state = ?", RuleRunRunning, runID, RuleRunFailed)
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return fmt.Errorf("rule run %d: nothing to retry", runID)
	}
	return s.runRule(ctx, r, store.Int(v[1]), store.Str(v[2]))
}

// UndoRuleRun puts back what the run's local actions changed, last first.
// One whose target changed since (moved on, the flag or label changed by
// hand) is kept as it is; kept counts them. The checks and the writes hold
// the edit lock together, as UndoArchive's do. It waits for a run still
// going (one at a time, ruleRunMu): undone in the middle, the actions after
// would still be applied.
func (s *Service) UndoRuleRun(ctx context.Context, runID int64) (kept int, err error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	s.ruleRunMu.Lock() // before editMu, as runRule takes them
	defer s.ruleRunMu.Unlock()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	args := []any{runID, RuleActionDone}
	for _, k := range ruleUndoable {
		args = append(args, k)
	}
	rows, err := s.db.Query(ctx, `SELECT a.idx, a.kind, a.prior, r.message_id FROM rule_runs r JOIN rule_action_results a ON a.run_id = r.id
		WHERE r.id = ? AND a.state = ? AND a.kind IN (`+placeholders(len(ruleUndoable))+`) ORDER BY a.idx DESC`, args...)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 { // nothing it can put back: the run stays as it is, a failed one retryable
		return 0, nil
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
	if kind == ActionBlock {
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
		rows, err := s.db.Query(ctx, "SELECT state, prior FROM rule_action_results WHERE run_id = ? AND idx = ?", runID, idx)
		if err != nil {
			return false, err
		}
		if len(rows.Values) == 0 || store.Str(rows.Values[0][0]) != RuleActionDone {
			return false, nil
		}
		if err := json.Unmarshal([]byte(store.Str(rows.Values[0][1])), &p); err != nil {
			return false, err
		}
		blocked, err := s.readList(ctx, blockedSenders)
		if err != nil {
			return false, err
		}
		if p.Had || p.FromAddr == "" || !slices.Contains(blocked, p.FromAddr) {
			return false, s.exec(ctx, mark(RuleActionKept))
		}
		stmt, err := listWrite(blockedSenders, slices.DeleteFunc(blocked, func(addr string) bool { return addr == p.FromAddr }))
		if err != nil {
			return false, err
		}
		if _, err := s.db.Transaction(ctx, []sqlite.Statement{stmt, mark(RuleActionUndone)}); err != nil {
			return false, err
		}
		s.bus.Publish(events.Event{Type: events.SettingsChanged})
		return true, nil
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
		// Only while the row is still as the rule left it, checked by the write
		// itself: SetLabel takes no lock an undo holds, and a label set by hand
		// in between is the user's, kept. The result follows what it changed.
		still := " WHERE message_id = ? AND label_id = ? AND source = 'rule' AND active = ?"
		w := sqlite.Statement{SQL: "DELETE FROM message_labels" + still, Args: []any{t.id, p.LabelID, b2i(on)}}
		if p.Source != "" {
			w = sqlite.Statement{SQL: "UPDATE message_labels SET source = ?, active = ?" + still, Args: []any{p.Source, b2i(p.Active), t.id, p.LabelID, b2i(on)}}
		}
		if err := s.apply(ctx, acc, []target{t}, []sqlite.Statement{w, {SQL: "UPDATE rule_action_results SET state = CASE WHEN changes() > 0 THEN ? ELSE ? END WHERE run_id = ? AND idx = ?",
			Args: []any{RuleActionUndone, RuleActionKept, runID, idx}}}); err != nil {
			return false, err
		}
		rows, err := s.db.Query(ctx, "SELECT state FROM rule_action_results WHERE run_id = ? AND idx = ?", runID, idx)
		if err != nil {
			return false, err
		}
		return len(rows.Values) > 0 && store.Str(rows.Values[0][0]) == RuleActionUndone, nil
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

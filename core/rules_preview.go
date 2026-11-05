package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/store"
)

// RulePreviewDays is how far back the mail PreviewRule lists goes.
const RulePreviewDays = 30

// TriggerExisting is the trigger of a run RunRuleOnExisting made; never a rule's.
const TriggerExisting = "existing"

// existingActions are the action kinds a run over existing mail does: local
// ones, and writing files. Any other kind (a forward, a Wit action) only
// ever runs on new mail, so a kind not listed here waits for it.
var existingActions = map[string]bool{ActionLabel: true, ActionUnlabel: true, ActionRead: true, ActionStar: true,
	ActionArchive: true, ActionJunk: true, ActionTrash: true, ActionBlock: true, ActionStop: true,
	ActionPDF: true, ActionEML: true, ActionAttachments: true}

// RunsOnExisting reports whether an action of kind runs over existing mail.
func RunsOnExisting(kind string) bool { return existingActions[kind] }

// RulePreview is the existing mail a rule matches: how many, and the newest
// of the last RulePreviewDays days.
type RulePreview struct {
	Count    int
	Messages []MessageSummary
}

// PreviewRule is the existing mail, Sent and Drafts aside, that d's trigger
// and conditions match: how many (all of it, as RunRuleOnExisting runs on),
// and the newest limit of it of the last RulePreviewDays days. It gives what
// the rule's evaluator does, read with SQL and the full-text index, so a
// cancelled ctx stops it.
func (s *Service) PreviewRule(ctx context.Context, d RuleDoc, limit int) (RulePreview, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return RulePreview{}, err
	}
	defer end()
	for _, c := range d.Conditions {
		if err := s.validCondition(ctx, c); err != nil {
			return RulePreview{}, err
		}
	}
	cond, args := s.existingSQL(d)
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM messages m WHERE "+cond, args...)
	if err != nil {
		return RulePreview{}, err
	}
	p := RulePreview{Count: int(store.Int(rows.Values[0][0]))}
	recent := cond + " AND m.date >= ?"
	if p.Messages, err = s.searchMessages(ctx, recent, append(args, s.clk().Now().AddDate(0, 0, -RulePreviewDays).Unix()), limit); err != nil {
		return RulePreview{}, err
	}
	return p, nil
}

// RunRuleOnExisting runs the stored rule once over the mail PreviewRule
// counts for it, with only the actions that RunsOnExisting, and returns how
// many messages it ran on. A message the rule already ran on is skipped.
func (s *Service) RunRuleOnExisting(ctx context.Context, ruleID int64) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	rules, err := s.rules(ctx, false)
	if err != nil {
		return 0, err
	}
	var r *Rule
	for i := range rules {
		if rules[i].ID == ruleID {
			r = &rules[i]
		}
	}
	if r == nil {
		return 0, fmt.Errorf("rule %d not found", ruleID)
	}
	if !hasExistingAction(r.Doc) {
		return 0, nil
	}
	cond, args := s.existingSQL(r.Doc)
	// Mail that arrived after the rule was saved is new mail, which the
	// consumer runs it on with every action (a forward too), not this run.
	rows, err := s.db.Query(ctx, "SELECT m.id FROM messages m WHERE "+cond+
		" AND NOT EXISTS (SELECT 1 FROM rule_runs WHERE rule_id = ? AND message_id = m.id)"+
		" AND NOT EXISTS (SELECT 1 FROM events_log WHERE type = 'message.received' AND message_id = m.id AND seq > ?) ORDER BY m.id",
		append(args, ruleID, r.since)...)
	if err != nil {
		return 0, err
	}
	for _, v := range rows.Values {
		if err := s.runRule(ctx, *r, store.Int(v[0]), TriggerExisting); err != nil {
			return 0, err
		}
	}
	return len(rows.Values), nil
}

func hasExistingAction(d RuleDoc) bool {
	for _, a := range d.Actions {
		if RunsOnExisting(a.Kind) && a.Kind != ActionStop {
			return true
		}
	}
	return false
}

// RuleHitCounts is each rule's number of runs in the last RuleHitDays days;
// a rule with none is absent.
func (s *Service) RuleHitCounts(ctx context.Context) (map[int64]int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT rule_id, count(*) FROM rule_runs WHERE at >= ? GROUP BY rule_id",
		time.Now().AddDate(0, 0, -RuleHitDays).Unix())
	if err != nil {
		return nil, err
	}
	out := map[int64]int{}
	for _, v := range rows.Values {
		out[store.Int(v[0])] = int(store.Int(v[1]))
	}
	return out, nil
}

// existingSQL is the condition on messages m for the existing mail d runs on.
func (s *Service) existingSQL(d RuleDoc) (string, []any) {
	cond, args := ruleSQL(d, s.clk().Now().Unix())
	return cond + " AND m.folder_id NOT IN (SELECT id FROM folders WHERE role IN ('sent', 'drafts'))", args
}

// ruleSQL is d's account, trigger and conditions as a condition on messages m, true
// where RuleDoc.matches is, and firesOn for a label or security trigger.
// shortcut: SQLite's lower() folds ASCII only, the evaluator all of Unicode;
// they differ on a non-ASCII capital in an "is" or a short "contains".
func ruleSQL(d RuleDoc, now int64) (string, []any) {
	parts, args := make([]string, 0, len(d.Conditions)), []any(nil)
	for _, c := range d.Conditions {
		sql, a := c.sql(now)
		parts, args = append(parts, sql), append(args, a...)
	}
	cond := "1"
	if len(parts) > 0 {
		join := " AND "
		if d.Match == MatchAny {
			join = " OR "
		}
		cond = "(" + strings.Join(parts, join) + ")"
	}
	if d.Account != 0 {
		cond += " AND m.account_id = ?"
		args = append(args, d.Account)
	}
	switch d.Trigger {
	case TriggerLabel:
		cond += " AND " + labelSlugSQL
		args = append(args, d.TriggerArg)
	case TriggerSecurity:
		if d.TriggerArg == SecurityDangerous {
			cond += " AND m.security_status = 'warning'"
		} else {
			cond += " AND m.security_status IN ('warning', 'needs_review')"
		}
	}
	return cond, args
}

const labelSlugSQL = "EXISTS (SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id WHERE ml.message_id = m.id AND ml.active = 1 AND l.slug = ?)"

func (c RuleCondition) sql(now int64) (string, []any) {
	want := strings.ToLower(strings.TrimSpace(c.Value))
	var sql string
	var args []any
	switch c.Field {
	case FieldFrom:
		sql, args = textSQL("m.from_addr", "from_addr", c.Op, want, true)
	case FieldTo:
		sql, args = textSQL("value", "", c.Op, want, true)
		sql = "EXISTS (SELECT 1 FROM (SELECT value FROM json_each(COALESCE(m.to_addrs, '[]')) UNION ALL SELECT value FROM json_each(COALESCE(m.cc_addrs, '[]'))) WHERE " + sql + ")"
	case FieldSubject:
		sql, args = textSQL("m.subject", "subject", c.Op, want, false)
	case FieldBody:
		sql, args = textSQL("m.body_text", "body", c.Op, want, false)
	case FieldAttachments:
		return "COALESCE(m.has_attachments, 0) = ?", []any{b2i(c.Value == RuleYes)}
	case FieldLabel:
		sql, args = labelSlugSQL, []any{c.Value}
	case FieldAge:
		n, _ := strconv.Atoi(c.Value)
		return "COALESCE(m.date, 0) < ?", []any{now - int64(n)*86400}
	case FieldFolder:
		sql, args = "(COALESCE((SELECT role FROM folders WHERE id = m.folder_id), '') = ? OR CAST(m.folder_id AS TEXT) = ?)", []any{c.Value, c.Value}
	default:
		return "0", nil
	}
	if c.Op == OpNotContains {
		sql = "NOT " + sql
	}
	return sql, args
}

// textSQL is "col contains or is want" (lower-cased, trimmed); a contains
// uses the full-text index's column ftsCol from 3 runes up (trigrams). An
// address also "is" its bare address.
func textSQL(col, ftsCol, op, want string, addr bool) (string, []any) {
	v := "COALESCE(" + col + ", '')"
	if op != OpIs {
		if ftsCol != "" && utf8.RuneCountInString(want) >= 3 {
			return "m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)", []any{ftsCol + ` : "` + strings.ReplaceAll(want, `"`, `""`) + `"`}
		}
		return "instr(lower(" + v + "), ?) > 0", []any{want}
	}
	sql := "lower(trim(" + v + ", ' ' || char(9, 10, 13))) = ?"
	if !addr {
		return sql, []any{want}
	}
	bare := "lower(CASE WHEN instr(" + v + ", '<') > 0 AND instr(" + v + ", '>') > instr(" + v + ", '<') THEN substr(" + v + ", instr(" + v + ", '<') + 1, instr(" + v + ", '>') - instr(" + v + ", '<') - 1) ELSE trim(" + v + ") END)"
	return "(" + sql + " OR " + bare + " = ?)", []any{want, want}
}

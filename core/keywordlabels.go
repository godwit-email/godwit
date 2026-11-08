package core

// IMAP keywords on the server become labels in Godwit, and labels set by hand
// become keywords, for the accounts that turned it on (ADR 0035).
//
// A keyword is any flag that is not a system flag (\Seen), not a $-flag
// ($Forwarded) and not a junk marker. Its lowercase form is the label slug, the
// identity docs/adr/0014 reserved for this. Rows get source 'imap': a label put on
// or taken off by hand ('manual') and one a rule set ('rule') are never overruled.

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

var (
	keywordSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	localSlug   = regexp.MustCompile(`^u[0-9]+$`)
)

// notLabelKeywords are plain flags servers and clients use for their own ends.
var notLabelKeywords = map[string]bool{"nonjunk": true, "junk": true, "notjunk": true, "forwarded": true, "mdnsent": true}

// keywordsOf returns the label slugs a message's flag string carries.
func keywordsOf(flags string) []string {
	var out []string
	for _, f := range strings.Fields(flags) {
		if strings.HasPrefix(f, `\`) || strings.HasPrefix(f, "$") {
			continue
		}
		slug := strings.ToLower(f)
		if notLabelKeywords[slug] || !keywordLabel(slug) {
			continue
		}
		out = append(out, slug)
	}
	return out
}

// keywordLabel reports whether slug can stand for a label as an IMAP keyword.
// A slug made by CreateLabel ("u" and the row id) differs from device to
// device, so it is neither pushed nor read back.
func keywordLabel(slug string) bool {
	return keywordSlug.MatchString(slug) && !localSlug.MatchString(slug)
}

// importKeywordLabels makes the account's cached messages carry the labels the
// server's keywords name, creating a label for a keyword it has none for.
func (s *Service) importKeywordLabels(ctx context.Context, accountID int64) error {
	s.labelsMu.Lock()
	defer s.labelsMu.Unlock()
	if on, err := s.db.Query(ctx, "SELECT 1 FROM accounts WHERE id = ? AND label_keywords = 1", accountID); err != nil || len(on.Values) == 0 {
		return err // off: the account's labels stay local
	}
	// shortcut: reads every flagged message of the account each sync; track the messages a sync changed if this shows up in profiles.
	rows, err := s.db.Query(ctx, "SELECT id, flags FROM messages WHERE account_id = ? AND flags != ''", accountID)
	if err != nil {
		return err
	}
	byMsg := map[int64][]string{}
	wanted := map[string]bool{}
	for _, v := range rows.Values {
		kws := keywordsOf(store.Str(v[1]))
		if len(kws) == 0 {
			continue
		}
		byMsg[store.Int(v[0])] = kws
		for _, k := range kws {
			wanted[k] = true
		}
	}
	var stmts []sqlite.Statement
	for slug := range wanted { // a name in use gets " (imap)"; if that is taken too, the keyword stays unlabeled
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO labels(slug, name, color, icon, builtin, position)
			SELECT ?1, CASE WHEN EXISTS (SELECT 1 FROM labels WHERE lower(name) = ?1) THEN ?1 || ' (imap)' ELSE ?1 END,
				'gray', 'folder-3', 0, (SELECT COALESCE(MAX(position) + 1, 0) FROM labels)
			WHERE NOT EXISTS (SELECT 1 FROM labels WHERE slug = ?1)
			AND NOT (EXISTS (SELECT 1 FROM labels WHERE lower(name) = ?1) AND EXISTS (SELECT 1 FROM labels WHERE lower(name) = ?1 || ' (imap)'))`,
			Args: []any{slug}})
	}
	for id, kws := range byMsg {
		for _, k := range kws {
			// New mail the label lands on runs label rules, as the classifier's does.
			stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO events_log(type, account_id, message_id, subject, from_addr, msg_date, detail, created_at)
				SELECT ?, account_id, id, subject, from_addr, date, ?, unixepoch() FROM messages m WHERE m.id = ? AND m.labeled_at IS NULL
				AND EXISTS (SELECT 1 FROM labels WHERE slug = ?)
				AND EXISTS (SELECT 1 FROM events_log WHERE type = 'message.received' AND message_id = m.id)
				AND NOT EXISTS (SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
					WHERE ml.message_id = m.id AND l.slug = ? AND (ml.active = 1 OR ml.source != 'imap'))`,
				Args: []any{eventLabelApplied, k, id, k, k}})
			// Only imap rows follow the keyword: hand-set, rule and classifier rows stay theirs.
			stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO message_labels(message_id, label_id, source, active)
				SELECT ?, id, 'imap', 1 FROM labels WHERE slug = ?
				ON CONFLICT(message_id, label_id) DO UPDATE SET active = 1 WHERE source = 'imap' AND active = 0`,
				Args: []any{id, k}})
		}
	}
	// A keyword gone from the server takes its imap label off; hand-made rows stay.
	stmts = append(stmts, sqlite.Statement{SQL: `UPDATE message_labels SET active = 0
		WHERE source = 'imap' AND active = 1
		AND message_id IN (SELECT id FROM messages WHERE account_id = ?)
		AND NOT EXISTS (SELECT 1 FROM messages m JOIN labels l ON l.id = message_labels.label_id
			WHERE m.id = message_labels.message_id AND instr(' ' || lower(m.flags) || ' ', ' ' || l.slug || ' ') > 0)`,
		Args: []any{accountID}})
	res, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(res, func(r sqlite.Result) bool { return r.Changes > 0 }) {
		s.bus.Publish(events.Event{Type: events.LabelsChanged})
	}
	return nil
}

// pushLabelKeyword queues the label's slug as an IMAP keyword on (or off) the
// messages that lack (or carry) it, so other clients and agents see a tag put
// on by hand. Nil ids means every message. A slug a server cannot hold as a
// keyword is left local.
func (s *Service) pushLabelKeyword(ctx context.Context, ids []int64, labelID int64, on bool) error {
	rows, err := s.db.Query(ctx, "SELECT slug FROM labels WHERE id = ?", labelID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	slug := store.Str(rows.Values[0][0])
	if !keywordLabel(slug) {
		return nil
	}
	q := "SELECT id, account_id FROM messages WHERE account_id IN (SELECT id FROM accounts WHERE label_keywords = 1) AND (instr(' ' || lower(flags) || ' ', ' ' || ? || ' ') > 0) != ?"
	args := []any{slug, b2i(on)}
	if ids != nil {
		q += " AND id IN (" + placeholders(len(ids)) + ")"
		args = append(args, int64Args(ids)...)
	}
	if rows, err = s.db.Query(ctx, q, args...); err != nil {
		return err
	}
	byAcc := map[int64][]int64{} // SetFlags takes one account's messages at a time
	for _, v := range rows.Values {
		byAcc[store.Int(v[1])] = append(byAcc[store.Int(v[1])], store.Int(v[0]))
	}
	var errs []error
	for _, ids := range byAcc {
		if on {
			errs = append(errs, s.SetFlags(ctx, ids, []string{slug}, nil))
		} else {
			errs = append(errs, s.SetFlags(ctx, ids, nil, []string{slug}))
		}
	}
	return errors.Join(errs...)
}

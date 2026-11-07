package mailsync

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/store"
)

// merge returns the statements that make messages drop, rows of the same
// server message as keep, part of keep (ADR 0032), for one transaction, and
// the threads they leave. keep takes their locations (on a folder both have,
// the confirmed one), their ops (re-chained by id, each keeping its source
// folder) and its flags with every pending flags change applied on top in
// order (sync then refetches the server's). Per label the highest source wins
// (manual > rule > auto), a manual removal over a manual add. Drafts, rule
// runs (one on any row counts as run on keep), Wit citations, events, approval
// and audit targets and cached attachments go to keep, and their IDs become
// aliases of it.
func merge(ctx context.Context, db *store.DB, keep int64, drop []int64) ([]sqlite.Statement, []int64, error) {
	all := append([]int64{keep}, drop...)
	in, args := "("+marks(len(drop))+")", anys(drop)
	flags, err := mergedFlags(ctx, db, keep, all)
	if err != nil {
		return nil, nil, err
	}
	r, err := db.Query(ctx, "SELECT DISTINCT thread_id FROM messages WHERE thread_id IS NOT NULL AND id IN "+in, args...)
	if err != nil {
		return nil, nil, err
	}
	var tids []int64
	for _, v := range r.Values {
		tids = append(tids, store.Int(v[0]))
	}
	to := func(sql string) sqlite.Statement {
		return sqlite.Statement{SQL: sql, Args: append([]any{keep}, args...)}
	}
	stmts := []sqlite.Statement{
		to("UPDATE message_aliases SET message_id = ? WHERE message_id IN " + in),
		to("INSERT OR REPLACE INTO message_aliases(id, message_id) SELECT id, ? FROM messages WHERE id IN " + in),
		{SQL: "UPDATE messages SET gmail_msgid = coalesce(gmail_msgid, (SELECT max(gmail_msgid) FROM messages WHERE id IN " + in + ")) WHERE id = ?",
			Args: append(slices.Clone(args), keep)},
		{SQL: "UPDATE messages SET flags = ? WHERE id = ?", Args: []any{flags, keep}},
		// Locations: a confirmed one wins a folder over an unconfirmed one. One
		// hidden there has a move of it queued, which shows it where it goes.
		to(`DELETE FROM message_locations WHERE message_id = ?1 AND uid IS NULL AND folder_id IN
			(SELECT folder_id FROM message_locations WHERE uid IS NOT NULL AND message_id IN ` + in + ")"),
		to("UPDATE OR IGNORE message_locations SET message_id = ? WHERE uid IS NOT NULL AND message_id IN " + in),
		to("UPDATE OR IGNORE message_locations SET message_id = ? WHERE message_id IN " + in),
		to("UPDATE ops SET message_id = ? WHERE message_id IN " + in),
		{SQL: `UPDATE ops SET depends_on = (SELECT max(p.id) FROM ops p WHERE p.message_id = ops.message_id AND p.state IN ('pending', 'awaiting_claim') AND p.id < ops.id)
			WHERE message_id = ? AND state = 'pending'`, Args: []any{keep}},
		// The rows' labels are deleted before the rows, so messages_label_stash
		// hands none of them to other rows sharing a label_fp.
		{SQL: `INSERT INTO message_labels(message_id, label_id, source, active)
			SELECT ?, label_id, source, active FROM (SELECT label_id, source, active, row_number() OVER (PARTITION BY label_id
				ORDER BY CASE source WHEN 'manual' THEN 0 WHEN 'rule' THEN 1 ELSE 2 END, source = 'manual' AND active, message_id != ?) AS n
				FROM message_labels WHERE message_id IN (` + marks(len(all)) + `)) WHERE n = 1
			ON CONFLICT(message_id, label_id) DO UPDATE SET source = excluded.source, active = excluded.active`,
			Args: append([]any{keep, keep}, anys(all)...)},
		{SQL: "DELETE FROM message_labels WHERE message_id IN " + in, Args: args},
		to("UPDATE drafts SET source_message_id = ? WHERE source_message_id IN " + in),
		to("UPDATE OR IGNORE rule_runs SET message_id = ? WHERE message_id IN " + in),
		to("UPDATE run_citations SET message_id = ? WHERE message_id IN " + in),
		to("UPDATE events_log SET message_id = ? WHERE message_id IN " + in),
		to("UPDATE approvals SET target_id = ? WHERE target_kind = 'message' AND target_id IN " + in),
		to("UPDATE audit_log SET target_id = ? WHERE target_kind = 'message' AND target_id IN " + in),
		{SQL: `UPDATE attachments SET (cache_path, last_used) = (SELECT d.cache_path, d.last_used FROM attachments d
				WHERE d.part = attachments.part AND d.cache_path IS NOT NULL AND d.message_id IN ` + in + ` LIMIT 1)
			WHERE message_id = ? AND cache_path IS NULL AND EXISTS (SELECT 1 FROM attachments d
				WHERE d.part = attachments.part AND d.cache_path IS NOT NULL AND d.message_id IN ` + in + ")",
			Args: append(append(slices.Clone(args), keep), args...)},
		// Files the kept message did not take are orphans the startup sweep collects.
		{SQL: "DELETE FROM messages WHERE id IN " + in, Args: args},
	}
	return stmts, tids, nil
}

// mergedFlags is keep's flags with the pending flags changes of all applied
// in the order they were made.
func mergedFlags(ctx context.Context, db *store.DB, keep int64, all []int64) (string, error) {
	r, err := db.Query(ctx, "SELECT flags FROM messages WHERE id = ?", keep)
	if err != nil || len(r.Values) == 0 {
		return "", err
	}
	set := strings.Fields(store.Str(r.Values[0][0]))
	ops, err := db.Query(ctx, "SELECT payload FROM ops WHERE kind = 'flags' AND state = 'pending' AND message_id IN ("+marks(len(all))+") ORDER BY id", anys(all)...)
	if err != nil {
		return "", err
	}
	for _, v := range ops.Values {
		var p FlagsPayload
		if json.Unmarshal([]byte(store.Str(v[0])), &p) != nil {
			continue // the push fails it
		}
		for _, f := range append(p.Add, p.Remove...) {
			set = slices.DeleteFunc(set, func(g string) bool { return strings.EqualFold(f, g) })
		}
		set = append(set, p.Add...)
	}
	slices.Sort(set)
	return strings.Join(set, " "), nil
}

// mergeShared merges the account's messages that share an X-GM-MSGID, of
// msgids or (nil) any, each into the one the old display preferred: shown in
// the inbox, else in another folder, else in All Mail, then the lowest id.
func mergeShared(ctx context.Context, db *store.DB, accountID int64, msgids []any, r *result) error {
	which := ""
	if msgids != nil {
		which = " AND gmail_msgid IN (" + marks(len(msgids)) + ")"
	}
	rows, err := db.Query(ctx, `SELECT gmail_msgid, id FROM messages m WHERE account_id = ?1 AND gmail_msgid IN
		(SELECT gmail_msgid FROM messages WHERE account_id = ?1 AND gmail_msgid IS NOT NULL`+which+` GROUP BY gmail_msgid HAVING count(*) > 1)
		ORDER BY gmail_msgid, coalesce((SELECT min(CASE f.role WHEN 'inbox' THEN 0 WHEN 'all' THEN 2 ELSE 1 END) FROM message_locations l
			JOIN folders f ON f.id = l.folder_id WHERE l.message_id = m.id AND l.shown), 3), id`, append([]any{accountID}, msgids...)...)
	if err != nil {
		return err
	}
	for i := 0; i < len(rows.Values); {
		j := i + 1
		for j < len(rows.Values) && store.Str(rows.Values[j][0]) == store.Str(rows.Values[i][0]) {
			j++
		}
		keep, drop := store.Int(rows.Values[i][1]), []int64{}
		for _, v := range rows.Values[i+1 : j] {
			drop = append(drop, store.Int(v[1]))
		}
		stmts, tids, err := merge(ctx, db, keep, drop)
		if err != nil {
			return err
		}
		if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
			return err
		}
		r.gone(append(drop, keep), tids)
		i = j
	}
	return nil
}

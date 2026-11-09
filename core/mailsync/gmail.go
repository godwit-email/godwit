package mailsync

import (
	"context"
	"slices"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
)

// locate stores messages reported in f without content (ADR 0034). One at a
// location f holds gives that location's message its server_id, which rows
// stored before a sync fetched it lack; the rows sharing it then merge. Any
// other is a stored message in one more folder: f becomes its location, with
// the server's flags, as Gmail shares them across folders. A message taken
// out of every folder by a move not yet pushed is not shown in f; one moved
// to f without a COPYUID is claimed. New mail in f that this round inserted
// from a folder where it was not new is new mail all the same.
func (s *syncRun) locate(ctx context.Context, f *folder, ms []backend.MessageAppeared) error {
	db, r := s.db, s.r
	refs := make([]backend.MessageRef, len(ms))
	for i, m := range ms {
		refs[i] = m.Ref
	}
	held, err := s.HeldRefs(ctx, f.ref, refs)
	if err != nil {
		return err
	}
	var identify []sqlite.Statement
	for _, m := range ms {
		if held[m.Ref] {
			identify = append(identify, sqlite.Statement{SQL: `UPDATE messages SET server_id = ? WHERE server_id IS NULL
				AND id = (SELECT message_id FROM message_locations WHERE folder_id = ? AND server_ref = ?)`,
				Args: []any{m.ServerID, f.id, string(m.Ref)}})
		}
	}
	if len(identify) > 0 {
		if _, err := db.Transaction(ctx, identify); err != nil {
			return err
		}
	}
	stored := map[string][2]int64{} // server_id → message, thread
	for page := range slices.Chunk(ms, 500) {
		var ids []any
		for _, m := range page {
			if m.ServerID != "" {
				ids = append(ids, m.ServerID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if err := mergeShared(ctx, db, s.a.id, ids, r); err != nil {
			return err
		}
		rows, err := db.Query(ctx, "SELECT server_id, min(id), thread_id FROM messages WHERE account_id = ? AND server_id IN ("+marks(len(ids))+") GROUP BY server_id",
			append([]any{s.a.id}, ids...)...)
		if err != nil {
			return err
		}
		for _, v := range rows.Values {
			stored[store.Str(v[0])] = [2]int64{store.Int(v[1]), store.Int(v[2])}
		}
	}
	var stmts []sqlite.Statement
	var ids, tids []int64
	for _, m := range ms {
		st, ok := stored[m.ServerID]
		if held[m.Ref] || !ok { // one gone meanwhile is reported again with content next round
			continue
		}
		id, fl := st[0], flagString(m.Flags)
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO message_locations(message_id, folder_id, shown, server_ref, entered_at, date)
				SELECT id, ?, ` + notMoving + `, ?, unixepoch(), date FROM messages WHERE id = ?
				ON CONFLICT(message_id, folder_id) DO UPDATE SET server_ref = excluded.server_ref`,
			Args: []any{f.id, string(m.Ref), id},
		}, sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE message_id = ? AND state = 'awaiting_claim' AND dst_folder_id = ?", Args: []any{id, f.id}},
			sqlite.Statement{SQL: "UPDATE messages SET flags = ? WHERE id = ? AND flags != ? AND NOT " + pendingFlags, Args: []any{fl, id, fl}})
		ids, tids = append(ids, id), append(tids, st[1])
		if !m.Backfill {
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE message_imports SET arrival = 1 WHERE message_id = ?", Args: []any{id}})
		}
	}
	if len(stmts) == 0 {
		return nil
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return err
	}
	r.gone(ids, tids)
	return nil
}

// notMoving is true for a messages row (unaliased) with no move or purge of
// it waiting: a folder the server adds it to shows it only then.
const notMoving = "NOT EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND kind IN ('move', 'purge') AND state IN ('pending', 'awaiting_claim'))"

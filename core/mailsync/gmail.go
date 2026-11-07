package mailsync

import (
	"context"
	"slices"
	"strconv"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/store"
)

// fetchImport fetches the messages of set to import into f, and the highest
// UID in set. From Gmail it first fetches only UID, FLAGS and X-GM-MSGID: a
// message stored already, from another folder, gains f as a location
// (gmailLocate) and only the others are fetched whole (ADR 0032).
func fetchImport(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, set imap.NumSet, r *result) ([]*imapclient.FetchMessageBuffer, uint32, error) {
	gmail := c.Caps().Has(capGmail)
	opts := importItems(c)
	if gmail {
		opts = &imap.FetchOptions{UID: true, Flags: true, GmailMsgID: true}
	}
	msgs, err := c.Fetch(set, opts).Collect()
	if err != nil {
		return nil, 0, err
	}
	var top uint32
	for _, m := range msgs {
		top = max(top, uint32(m.UID))
	}
	if !gmail {
		return msgs, top, nil
	}
	rest, err := gmailLocate(ctx, db, a, f, msgs, r)
	if err != nil || len(rest) == 0 {
		return nil, top, err
	}
	msgs, err = c.Fetch(imap.UIDSetNum(rest...), importItems(c)).Collect()
	return msgs, top, err
}

// gmailLocate gives each of msgs (UID, FLAGS, X-GM-MSGID) whose X-GM-MSGID
// the account stores f as a location and the server's flags, as Gmail shares
// them across folders, and returns the UIDs of the others, which f does not
// hold yet. A message taken out of every folder by a move not yet pushed is
// not shown in f; one moved to f without a COPYUID is claimed.
func gmailLocate(ctx context.Context, db *store.DB, a *account, f *folder, msgs []*imapclient.FetchMessageBuffer, r *result) ([]imap.UID, error) {
	have, err := db.Query(ctx, "SELECT uid FROM message_locations WHERE folder_id = ? AND uid IS NOT NULL", f.id)
	if err != nil {
		return nil, err
	}
	held := map[imap.UID]bool{}
	for _, v := range have.Values {
		held[imap.UID(store.Int(v[0]))] = true
	}
	stored := map[string][2]int64{} // X-GM-MSGID → message, thread
	for page := range slices.Chunk(msgs, 500) {
		var ids []any
		for _, m := range page {
			if m.GmailMsgID != 0 && !held[m.UID] {
				ids = append(ids, strconv.FormatUint(m.GmailMsgID, 10))
			}
		}
		if len(ids) == 0 {
			continue
		}
		// shortcut: rows of one message synced before this dedup keep apart (lowest id here) until #524 merges them.
		rows, err := db.Query(ctx, "SELECT gmail_msgid, min(id), thread_id FROM messages WHERE account_id = ? AND gmail_msgid IN ("+marks(len(ids))+") GROUP BY gmail_msgid",
			append([]any{a.id}, ids...)...)
		if err != nil {
			return nil, err
		}
		for _, v := range rows.Values {
			stored[store.Str(v[0])] = [2]int64{store.Int(v[1]), store.Int(v[2])}
		}
	}
	var rest []imap.UID
	var stmts []sqlite.Statement
	var ids, tids []int64
	for _, m := range msgs {
		if held[m.UID] {
			continue
		}
		s, ok := stored[strconv.FormatUint(m.GmailMsgID, 10)]
		if !ok {
			rest = append(rest, m.UID)
			continue
		}
		id, fl := s[0], flagString(m.Flags)
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO message_locations(message_id, folder_id, shown, uidvalidity, uid, entered_at, date)
				SELECT id, ?, ` + notMoving + `, ?, ?, unixepoch(), date FROM messages WHERE id = ?
				ON CONFLICT(message_id, folder_id) DO UPDATE SET uidvalidity = excluded.uidvalidity, uid = excluded.uid`,
			Args: []any{f.id, f.uidValidity, uint32(m.UID), id},
		}, sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE message_id = ? AND state = 'awaiting_claim' AND dst_folder_id = ?", Args: []any{id, f.id}},
			sqlite.Statement{SQL: "UPDATE messages SET flags = ? WHERE id = ? AND flags != ? AND NOT " + pendingFlags, Args: []any{fl, id, fl}})
		ids, tids = append(ids, id), append(tids, s[1])
	}
	if len(stmts) == 0 {
		return rest, nil
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return nil, err
	}
	r.gone(ids, tids)
	return rest, nil
}

// notMoving is true for a messages row (unaliased) with no move or purge of
// it waiting: a folder the server adds it to shows it only then.
const notMoving = "NOT EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND kind IN ('move', 'purge') AND state IN ('pending', 'awaiting_claim'))"

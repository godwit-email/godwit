package mailsync

import (
	"context"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
)

// importMessage claims the message for a local row awaiting it or inserts it,
// with its attachments and thread, in one transaction.
func importMessage(ctx context.Context, db *store.DB, a *account, f *folder, m *imapclient.FetchMessageBuffer, texts map[string][]byte, r *result) error {
	p := toParsed(m, texts)
	uid := uint32(m.UID)
	if id, err := claim(ctx, db, a.id, f, uid, p.MessageID, m.RFC822Size); err != nil || id != 0 {
		if id != 0 {
			r.changed = append(r.changed, id)
		}
		return err
	}
	plan, err := planThread(ctx, db, a, p)
	if err != nil {
		return err
	}
	ids, _ := mail.ThreadKeys(p)
	args := append([]any{a.id, f.id, f.id, f.uidValidity, uid}, plan.args...)
	args = append(args, nullable(p.MessageID), nullable(p.InReplyTo), nullable(strings.Join(p.References, " ")), p.Subject, p.From,
		jsonList(p.To), jsonList(p.Cc), nullable(p.ReplyTo), p.Date.Unix(), flagString(m.Flags), m.RFC822Size,
		mail.Snippet(p.Text), p.Text, nullable(p.HTML), p.Truncated, len(p.Attachments) > 0)
	stmts := append(plan.pre, sqlite.Statement{
		SQL: `INSERT INTO messages(account_id, folder_id, server_folder_id, server_uidvalidity, server_uid, thread_id,
			message_id, in_reply_to, refs, subject, from_addr, to_addrs, cc_addrs, reply_to, date, flags, size,
			snippet, body_text, body_html, body_truncated, has_attachments)
			VALUES (?, ?, ?, ?, ?, ` + plan.expr + `, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: args,
	})
	msgAt := len(stmts) - 1
	for _, at := range p.Attachments {
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO attachments(message_id, part, encoding, charset, filename, content_type, size, content_id, inline)
				VALUES ((SELECT id FROM messages WHERE server_folder_id = ? AND server_uid = ?), ?, ?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{f.id, uid, at.Part, at.Encoding, at.Charset, at.Filename, at.ContentType, at.Size, at.ContentID, at.Inline},
		})
	}
	stmts = append(stmts, threadTail(a.id, f.id, uid, ids)...)
	res, err := db.Transaction(ctx, stmts)
	if err != nil {
		return err
	}
	tid := plan.target
	if tid == 0 {
		tid = res[0].LastInsertID
	}
	r.received = append(r.received, res[msgAt].LastInsertID)
	r.threads = append(r.threads, tid)
	return nil
}

// claim gives a new UID in f to the local message whose MOVE finished without a
// COPYUID: exactly one awaiting_claim op targeting f whose message has the same
// non-empty Message-ID and size. It returns the claimed message id, or 0.
func claim(ctx context.Context, db *store.DB, accountID int64, f *folder, uid uint32, messageID string, size int64) (int64, error) {
	if messageID == "" {
		return 0, nil
	}
	rows, err := db.Query(ctx, `SELECT o.id, m.id FROM ops o JOIN messages m ON m.id = o.message_id
		WHERE o.account_id = ? AND o.state = 'awaiting_claim' AND o.dst_folder_id = ? AND m.message_id = ? AND m.size = ?`,
		accountID, f.id, messageID, size)
	if err != nil || len(rows.Values) != 1 {
		return 0, err
	}
	op, id := store.Int(rows.Values[0][0]), store.Int(rows.Values[0][1])
	_, err = db.Transaction(ctx, []sqlite.Statement{
		{SQL: "UPDATE messages SET server_folder_id = ?, server_uidvalidity = ?, server_uid = ? WHERE id = ?",
			Args: []any{f.id, f.uidValidity, uid, id}},
		{SQL: "UPDATE ops SET state = 'done' WHERE id = ?", Args: []any{op}},
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// syncFlags refreshes flags of 1:last_uid and deletes local rows whose UID the
// server no longer returns. It changes nothing unless the FETCH completed.
// ponytail: refetches every FLAGS each round; switch to CONDSTORE/QRESYNC.
func syncFlags(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, f *folder, r *result) error {
	if f.lastUID == 0 {
		return nil
	}
	var set imap.UIDSet
	set.AddRange(1, imap.UID(f.lastUID))
	bufs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return err // incomplete: a missing UID might only mean a dropped connection
	}
	remote := map[uint32]string{}
	for _, b := range bufs {
		remote[uint32(b.UID)] = flagString(b.Flags)
	}
	rows, err := db.Query(ctx, "SELECT id, server_uid, flags FROM messages WHERE server_folder_id = ? AND server_uid <= ?", f.id, f.lastUID)
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	var changed []int64
	deleted := false
	for _, v := range rows.Values {
		id, uid, flags := store.Int(v[0]), uint32(store.Int(v[1])), store.Str(v[2])
		fl, ok := remote[uid]
		switch {
		case !ok:
			stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM messages WHERE id = ?", Args: []any{id}})
			deleted = true
		case fl != flags:
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE messages SET flags = ? WHERE id = ?", Args: []any{fl, id}})
		default:
			continue
		}
		changed = append(changed, id)
	}
	if deleted {
		stmts = append(stmts, pruneThreads(accountID))
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return err
	}
	r.changed = append(r.changed, changed...)
	return nil
}

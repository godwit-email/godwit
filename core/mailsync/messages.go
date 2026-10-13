package mailsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// importMessage claims the message for a local row awaiting it or inserts it,
// with its attachments and thread, in one transaction. twins is how many messages
// of this fetch share its Message-ID and size. An inserted arrival also logs its
// message.received event (plugin hooks) in that transaction; a claim does not.
func importMessage(ctx context.Context, db *store.DB, a *account, f *folder, m *imapclient.FetchMessageBuffer, texts map[string][]byte, twins int, arrival bool, r *result) error {
	p := toParsed(m, texts)
	uid := uint32(m.UID)
	if id, err := claim(ctx, db, a.id, f, uid, p.MessageID, m.RFC822Size, twins, r); err != nil || id != 0 {
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
		mail.Snippet(p.Text), p.Text, nullable(p.HTML), p.Truncated, len(p.Attachments) > 0, labelFP(p, m.RFC822Size))
	stmts := append(plan.pre, sqlite.Statement{
		SQL: `INSERT INTO messages(account_id, folder_id, server_folder_id, server_uidvalidity, server_uid, thread_id,
			message_id, in_reply_to, refs, subject, from_addr, to_addrs, cc_addrs, reply_to, date, flags, size,
			snippet, body_text, body_html, body_truncated, has_attachments, label_fp, entered_at)
			VALUES (?, ?, ?, ?, ?, ` + plan.expr + `, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch())`,
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
	if arrival {
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO events_log(type, account_id, message_id, subject, from_addr, msg_date, created_at)
				VALUES ('message.received', ?, (SELECT id FROM messages WHERE account_id = ? AND server_folder_id = ?
					AND server_uidvalidity = ? AND server_uid = ?), ?, ?, ?, unixepoch())`,
			Args: []any{a.id, a.id, f.id, f.uidValidity, uid, p.Subject, p.From, p.Date.Unix()},
		})
	}
	res, err := db.Transaction(ctx, stmts)
	if err != nil {
		return err
	}
	tid := plan.target
	if tid == 0 {
		tid = res[0].LastInsertID
	}
	if len(plan.others) > 0 {
		r.gone(plan.moved, nil)
		r.merged(tid, plan.others)
	}
	if arrival { // only new mail is announced as received; older mail and backfills only change the list
		r.received = append(r.received, res[msgAt].LastInsertID)
		r.threads = append(r.threads, tid)
	} else {
		r.changed = append(r.changed, res[msgAt].LastInsertID)
		r.changedThreads = append(r.changedThreads, tid)
	}
	return nil
}

// claim gives a new UID in f to the local message whose MOVE finished without a
// COPYUID: exactly one awaiting_claim op targeting f whose message has the same
// non-empty Message-ID and size. It returns the claimed message id, or 0.
//
// When the arrival cannot be told apart from another copy (another local row
// waiting, another arrival, or a message already in f with the same Message-ID
// and size), nothing is merged: the ops become conflicts, the local rows waiting
// are deleted and every server copy is imported as a message of its own.
func claim(ctx context.Context, db *store.DB, accountID int64, f *folder, uid uint32, messageID string, size int64, twins int, r *result) (int64, error) {
	if messageID == "" {
		return 0, nil
	}
	rows, err := db.Query(ctx, `SELECT o.id, m.id FROM ops o JOIN messages m ON m.id = o.message_id
		WHERE o.account_id = ? AND o.state = 'awaiting_claim' AND o.dst_folder_id = ? AND m.message_id = ? AND m.size = ?`,
		accountID, f.id, messageID, size)
	if err != nil || len(rows.Values) == 0 {
		return 0, err
	}
	present, err := db.Query(ctx, "SELECT count(*) FROM messages WHERE server_folder_id = ? AND message_id = ? AND size = ?", f.id, messageID, size)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 1 && twins <= 1 && store.Int(present.Values[0][0]) == 0 {
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
	msg := fmt.Sprintf("a message moved to %q (Message-ID %s) could not be told apart from another copy there; the copies are kept as separate messages", f.name, messageID)
	var ops, msgs []any
	for _, v := range rows.Values {
		ops, msgs = append(ops, v[0]), append(msgs, v[1])
	}
	ids, tids, err := doomed(ctx, db, "id IN ("+marks(len(msgs))+")", msgs...)
	if err != nil {
		return 0, err
	}
	stmts := []sqlite.Statement{
		{SQL: "UPDATE ops SET state = 'conflict', last_error = ? WHERE id IN (" + marks(len(ops)) + ")", Args: append([]any{msg}, ops...)},
		{SQL: "DELETE FROM messages WHERE id IN (" + marks(len(msgs)) + ")", Args: msgs},
	}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return 0, err
	}
	r.gone(ids, tids)
	r.conflicts = append(r.conflicts, msg)
	return 0, nil
}

// unclaimable ends moves still awaiting their claim after a complete Fetch: every
// arrival since the move was imported, so none matched (the message has no
// Message-ID, or the server changed it). The op and the message's other pending
// ops become conflicts and the local row, which has no server location, is
// deleted; the copy in the destination was imported as a message of its own.
func unclaimable(ctx context.Context, db *store.DB, accountID int64, r *result) error {
	rows, err := db.Query(ctx, `SELECT o.message_id, f.name FROM ops o LEFT JOIN folders f ON f.id = o.dst_folder_id
		WHERE o.account_id = ? AND o.state = 'awaiting_claim' AND o.message_id IS NOT NULL`, accountID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	var msgs []any
	var names []string
	for _, v := range rows.Values {
		msgs, names = append(msgs, v[0]), append(names, fmt.Sprintf("%q", store.Str(v[1])))
	}
	msg := fmt.Sprintf("%d message(s) moved to %s could not be matched to the copy there; the copy is kept as a new message and changes queued for the original were not applied",
		len(msgs), strings.Join(slices.Compact(slices.Sorted(slices.Values(names))), ", "))
	ids, tids, err := doomed(ctx, db, "server_folder_id IS NULL AND id IN ("+marks(len(msgs))+")", msgs...)
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{
		{SQL: "UPDATE ops SET state = 'conflict', last_error = ? WHERE state IN ('pending', 'awaiting_claim') AND message_id IN (" + marks(len(msgs)) + ")",
			Args: append([]any{msg}, msgs...)},
		{SQL: "DELETE FROM messages WHERE server_folder_id IS NULL AND id IN (" + marks(len(msgs)) + ")", Args: msgs},
	}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return err
	}
	r.gone(ids, tids)
	r.conflicts = append(r.conflicts, msg)
	return nil
}

// pendingFlags is true for a messages row with a flags op not yet pushed.
const pendingFlags = "EXISTS (SELECT 1 FROM ops o WHERE o.message_id = messages.id AND o.kind = 'flags' AND o.state = 'pending')"

// syncFlags refreshes flags of 1:last_uid (except where a flags op is pending)
// and deletes local rows whose UID the server no longer returns. It changes
// nothing unless the FETCH completed.
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
	// A flags change still queued wins over the server's flags until it is pushed.
	rows, err := db.Query(ctx, "SELECT id, server_uid, flags, thread_id, "+pendingFlags+
		" FROM messages WHERE server_folder_id = ? AND server_uid <= ?", f.id, f.lastUID)
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	var changed, tids []int64
	for _, v := range rows.Values {
		id, uid, flags := store.Int(v[0]), uint32(store.Int(v[1])), store.Str(v[2])
		fl, ok := remote[uid]
		switch {
		case !ok:
			stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM messages WHERE id = ?", Args: []any{id}})
			tids = append(tids, store.Int(v[3]))
		case fl != flags && store.Int(v[4]) == 0:
			// Checked again in the UPDATE: a local edit may commit between this read and the write.
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE messages SET flags = ? WHERE id = ? AND NOT " + pendingFlags, Args: []any{fl, id}})
		default:
			continue
		}
		changed = append(changed, id)
	}
	if len(tids) > 0 {
		stmts = append(stmts, tidyThreads(accountID, tids)...)
	}
	if BeforeFlagsWrite != nil {
		BeforeFlagsWrite()
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return err
	}
	r.gone(changed, tids)
	return nil
}

// BeforeFlagsWrite, when set, runs between syncFlags reading the local flags and
// writing the server's. Tests only.
var BeforeFlagsWrite func()

// labelFP identifies a message across its cached copies, for the labels set
// by hand to outlive them: a SHA-256 of its Message-ID, else of its headers,
// size and whole text. A hash, so the label stash keeps no mail content.
func labelFP(p *mail.Parsed, size int64) string {
	key := "id\x00" + p.MessageID
	if p.MessageID == "" {
		key = strings.Join([]string{"mail", p.From, fmt.Sprint(p.Date.Unix()), p.Subject, fmt.Sprint(size), p.Text, p.HTML}, "\x00")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

package mailsync

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// Local is a message InsertLocal stored, and the threads it merged.
type Local struct {
	ID, ThreadID int64
	Merged       []int64 // threads merged into ThreadID
	Moved        []int64 // their messages
}

// InsertLocal stores p, a local archive's message whose original file hashes
// to hash, in folderID with no server location (ADR 0036): its thread,
// attachment list, full-text index and security verdict, as importMessage
// does. It is old mail: read, with no receive event, and already labeled and
// scanned, so neither the AI labeler nor the security scanner takes it.
func InsertLocal(ctx context.Context, db *store.DB, accountID, folderID int64, p *mail.Parsed, size int64, hash string) (Local, error) {
	a, err := loadAccount(ctx, db, accountID)
	if err != nil {
		return Local{}, err
	}
	a.noSelf = true
	plan, err := planThread(ctx, db, a, p)
	if err != nil {
		return Local{}, err
	}
	ids, _ := mail.ThreadKeys(p)
	secStatus, secReason := mail.SecurityReportToDB(mail.EvaluateSecurity(p.From, p.AuthenticationResults))
	now := time.Now().Unix()
	args := append([]any{accountID}, plan.args...)
	args = append(args, nullable(p.MessageID), nullable(p.InReplyTo), nullable(strings.Join(p.References, " ")), p.Subject, p.From,
		jsonList(p.To), jsonList(p.Cc), nullable(p.ReplyTo), p.Date.Unix(), `\Seen`, size,
		mail.Snippet(p.Text), p.Text, nullable(p.HTML), p.Truncated, len(p.Attachments) > 0, nullable(labelFP(p, size)),
		p.ListUnsubscribe, p.ListUnsubscribePost, p.ListID, p.AutoSubmitted, p.Precedence, p.AuthenticationResults, secStatus, secReason, now, p.RuleSent, now, hash)
	stmts := append(plan.pre, sqlite.Statement{
		SQL: `INSERT INTO messages(account_id, thread_id,
			message_id, in_reply_to, refs, subject, from_addr, to_addrs, cc_addrs, reply_to, date, flags, size,
			snippet, body_text, body_html, body_truncated, has_attachments, label_fp, list_unsubscribe, list_unsubscribe_post, list_id, auto_submitted, precedence, auth_results, security_status, security_reason, security_scanned_at, rule_sent, labeled_at, content_hash)
			VALUES (?, ` + plan.expr + `, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: args,
	}, sqlite.Statement{
		SQL:  `INSERT INTO message_locations(message_id, folder_id, shown, entered_at, date) VALUES (last_insert_rowid(), ?, 1, unixepoch(), ?)`,
		Args: []any{folderID, p.Date.Unix()},
	})
	msgAt := len(stmts) - 2
	const msg = "(SELECT id FROM messages WHERE account_id = ? AND content_hash = ?)"
	for _, at := range p.Attachments {
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO attachments(message_id, part, encoding, charset, filename, content_type, size, content_id, inline)
				VALUES (` + msg + `, ?, ?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{accountID, hash, at.Part, at.Encoding, at.Charset, at.Filename, at.ContentType, at.Size, at.ContentID, at.Inline},
		})
	}
	stmts = append(stmts, threadTailOf(accountID, msg, []any{accountID, hash}, ids)...)
	res, err := db.Transaction(ctx, stmts)
	if err != nil {
		return Local{}, err
	}
	l := Local{ID: res[msgAt].LastInsertID, ThreadID: plan.target, Merged: plan.others, Moved: plan.moved}
	if l.ThreadID == 0 {
		l.ThreadID = res[0].LastInsertID
	}
	return l, nil
}

// DeleteLocal deletes the local archive's messages ids for good (ADR 0036)
// and returns the content hashes of their originals, for the caller to
// remove, and the threads they were in.
func DeleteLocal(ctx context.Context, db *store.DB, accountID int64, ids []int64) (hashes []string, tids []int64, err error) {
	where := "account_id = ? AND content_hash IS NOT NULL AND id IN (" + marks(len(ids)) + ")"
	args := append([]any{accountID}, anys(ids)...)
	rows, err := db.Query(ctx, "SELECT content_hash, thread_id FROM messages WHERE "+where, args...)
	if err != nil || len(rows.Values) == 0 {
		return nil, nil, err
	}
	for _, v := range rows.Values {
		hashes = append(hashes, store.Str(v[0]))
		if t := store.Int(v[1]); t != 0 {
			tids = append(tids, t)
		}
	}
	stmts := []sqlite.Statement{{SQL: "DELETE FROM messages WHERE " + where, Args: args}}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return nil, nil, err
	}
	return hashes, tids, nil
}

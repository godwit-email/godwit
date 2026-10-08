package mailsync

import (
	"context"
	"encoding/json"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
)

const subjectWindow = 7 * 24 * time.Hour

// threadPlan places a new message in a conversation: pre runs before the message
// INSERT, whose thread_id is the SQL expression expr with args.
type threadPlan struct {
	pre    []sqlite.Statement
	expr   string
	args   []any
	target int64 // existing thread, 0 when pre creates one
}

func planThread(ctx context.Context, db *store.DB, acc *account, p *mail.Parsed) (threadPlan, error) {
	ids, key := mail.ThreadKeys(p)
	tids, err := refThreads(ctx, db, acc.id, ids)
	if err != nil {
		return threadPlan{}, err
	}
	if len(tids) == 0 && p.InReplyTo == "" && len(p.References) == 0 {
		tid, err := subjectThread(ctx, db, acc, p, key)
		if err != nil {
			return threadPlan{}, err
		}
		if tid != 0 {
			tids = []int64{tid}
		}
	}
	if len(tids) == 0 {
		return threadPlan{
			pre: []sqlite.Statement{{SQL: "INSERT INTO threads(account_id, subject, last_date) VALUES (?, ?, ?)",
				Args: []any{acc.id, p.Subject, p.Date.Unix()}}},
			expr: "last_insert_rowid()",
		}, nil
	}
	target, others := tids[0], anys(tids[1:]) // tids are sorted: merge into the smallest
	plan := threadPlan{expr: "?", args: []any{target}, target: target}
	if len(others) > 0 {
		in := "(" + marks(len(others)) + ")"
		args := append([]any{target}, others...)
		plan.pre = []sqlite.Statement{
			{SQL: "UPDATE messages SET thread_id = ? WHERE thread_id IN " + in, Args: args},
			{SQL: "UPDATE thread_refs SET thread_id = ? WHERE thread_id IN " + in, Args: args},
			{SQL: "DELETE FROM threads WHERE id IN " + in, Args: others}, // after the updates: it cascades
		}
	}
	return plan, nil
}

func refThreads(ctx context.Context, db *store.DB, accountID int64, ids []string) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, "SELECT DISTINCT thread_id FROM thread_refs WHERE account_id = ? AND message_id_hdr IN ("+
		marks(len(ids))+") ORDER BY thread_id", append([]any{accountID}, anys(ids)...)...)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, r := range rows.Values {
		out = append(out, store.Int(r[0]))
	}
	return out, nil
}

// subjectThread finds the most recent conversation within subjectWindow with the
// same subject key and a shared participant other than the account itself.
// ponytail: scans the account's messages in the time window and compares in Go;
// store a subject_key column with an index if this shows up in profiles.
func subjectThread(ctx context.Context, db *store.DB, a *account, p *mail.Parsed, key string) (int64, error) {
	if key == "" {
		return 0, nil
	}
	mine := participants(a.email, append([]string{p.From}, append(p.To, p.Cc...)...))
	if len(mine) == 0 {
		return 0, nil
	}
	d := p.Date.Unix()
	w := int64(subjectWindow / time.Second)
	rows, err := db.Query(ctx, `SELECT thread_id, subject, from_addr, to_addrs, cc_addrs FROM messages
		WHERE account_id = ? AND thread_id IS NOT NULL AND date BETWEEN ? AND ? ORDER BY date DESC`, a.id, d-w, d+w)
	if err != nil {
		return 0, err
	}
	for _, r := range rows.Values {
		if _, k := mail.ThreadKeys(&mail.Parsed{Subject: store.Str(r[1])}); k != key {
			continue
		}
		var to, cc []string
		json.Unmarshal([]byte(store.Str(r[3])), &to)
		json.Unmarshal([]byte(store.Str(r[4])), &cc)
		for k := range participants(a.email, append([]string{store.Str(r[2])}, append(to, cc...)...)) {
			if mine[k] {
				return store.Int(r[0]), nil
			}
		}
	}
	return 0, nil
}

func participants(self string, addrs []string) map[string]bool {
	out := map[string]bool{}
	self = mail.AddrKey(self)
	for _, s := range addrs {
		if k := mail.AddrKey(s); k != "" && k != self {
			out[k] = true
		}
	}
	return out
}

// threadTail records every Message-ID of the message at (folderID, uid) under its
// thread (unreceived ones become placeholders) and refreshes the thread's last_date.
func threadTail(accountID, folderID int64, uid uint32, ids []string) []sqlite.Statement {
	const tid = "(SELECT thread_id FROM messages WHERE server_folder_id = ? AND server_uid = ?)"
	var out []sqlite.Statement
	for _, id := range ids {
		out = append(out, sqlite.Statement{
			SQL: "INSERT INTO thread_refs(account_id, message_id_hdr, thread_id) VALUES (?, ?, " + tid + `)
				ON CONFLICT(account_id, message_id_hdr) DO UPDATE SET thread_id = excluded.thread_id`,
			Args: []any{accountID, id, folderID, uid},
		})
	}
	return append(out, sqlite.Statement{
		SQL:  "UPDATE threads SET last_date = (SELECT max(date) FROM messages WHERE thread_id = threads.id) WHERE id = " + tid,
		Args: []any{folderID, uid},
	})
}

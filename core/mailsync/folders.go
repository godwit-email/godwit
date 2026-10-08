package mailsync

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/store"
)

type folder struct {
	id                   int64
	name                 string
	uidValidity, lastUID uint32
}

var specialUse = map[imap.MailboxAttr]string{
	imap.MailboxAttrSent: "sent", imap.MailboxAttrTrash: "trash", imap.MailboxAttrDrafts: "drafts",
	imap.MailboxAttrJunk: "junk", imap.MailboxAttrArchive: "archive",
}

var roleNames = map[string]string{
	"sent": "sent", "sent items": "sent", "已发送": "sent",
	"trash": "trash", "deleted items": "trash", "已删除": "trash",
	"drafts": "drafts", "草稿": "drafts",
	"junk": "junk", "spam": "junk", "垃圾邮件": "junk",
	"archive": "archive", "归档": "archive",
}

// role is the folder's SPECIAL-USE role, else one guessed from its (last) name segment.
func role(d *imap.ListData) string {
	for _, a := range d.Attrs {
		if r, ok := specialUse[a]; ok {
			return r
		}
	}
	if strings.EqualFold(d.Mailbox, "INBOX") {
		return "inbox"
	}
	name := d.Mailbox
	if d.Delim != 0 {
		name = name[strings.LastIndexByte(name, byte(d.Delim))+1:]
	}
	return roleNames[strings.ToLower(name)]
}

// syncFolders mirrors the server's selectable folders into the folders table and
// returns them in LIST order. Folders gone from the server are deleted (cascading
// to their messages).
func syncFolders(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64) ([]*folder, error) {
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		return nil, err
	}
	var names []string
	var stmts []sqlite.Statement
	for _, d := range list {
		if slices.Contains(d.Attrs, imap.MailboxAttrNoSelect) || slices.Contains(d.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		names = append(names, d.Mailbox)
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO folders(account_id, name, role) VALUES (?, ?, ?)
				ON CONFLICT(account_id, name) DO UPDATE SET role = excluded.role`,
			Args: []any{accountID, d.Mailbox, role(d)},
		})
	}
	stmts = append(stmts, sqlite.Statement{
		SQL:  "DELETE FROM folders WHERE account_id = ? AND name NOT IN (" + marks(len(names)) + ")",
		Args: append([]any{accountID}, anys(names)...),
	}, pruneThreads(accountID))
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, "SELECT id, name, uidvalidity, last_uid FROM folders WHERE account_id = ?", accountID)
	if err != nil {
		return nil, err
	}
	byName := map[string]*folder{}
	for _, r := range rows.Values {
		byName[store.Str(r[1])] = &folder{id: store.Int(r[0]), name: store.Str(r[1]),
			uidValidity: uint32(store.Int(r[2])), lastUID: uint32(store.Int(r[3]))}
	}
	out := make([]*folder, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// pruneThreads deletes conversations left without messages (their thread_refs cascade).
func pruneThreads(accountID int64) sqlite.Statement {
	return sqlite.Statement{
		SQL:  "DELETE FROM threads WHERE account_id = ? AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = threads.id)",
		Args: []any{accountID},
	}
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys[T any](s []T) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

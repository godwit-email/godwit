package mailsync

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/store"
)

type folder struct {
	id                   int64
	name                 string
	uidValidity, lastUID uint32
	synced               bool   // a first sync at uidValidity completed
	filled               *int64 // the cutoff a range backfill completed down to
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

func selectable(d *imap.ListData) bool {
	return !slices.Contains(d.Attrs, imap.MailboxAttrNoSelect) && !slices.Contains(d.Attrs, imap.MailboxAttrNonExistent)
}

// CountFolders is how many selectable folders LIST returns: those a sync mirrors.
func CountFolders(c *imapclient.Client) (int, error) {
	list, err := c.List("", "*", nil).Collect()
	n := 0
	for _, d := range list {
		if selectable(d) {
			n++
		}
	}
	return n, err
}

// syncFolders mirrors the server's selectable folders into the folders table and
// returns them in LIST order. Folders gone from the server are deleted with the
// messages in them; a message only moved there locally returns to its server folder.
func syncFolders(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, r *result) ([]*folder, error) {
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		return nil, err
	}
	var names []string
	var stmts []sqlite.Statement
	for _, d := range list {
		if !selectable(d) {
			continue
		}
		names = append(names, d.Mailbox)
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO folders(account_id, name, role) VALUES (?, ?, ?)
				ON CONFLICT(account_id, name) DO UPDATE SET role = excluded.role`,
			Args: []any{accountID, d.Mailbox, role(d)},
		})
	}
	stale := "account_id = ? AND name NOT IN (" + marks(len(names)) + ")"
	staleArgs := append([]any{accountID}, anys(names)...)
	gone := "(SELECT id FROM folders WHERE " + stale + ")"
	twice := append(append([]any{}, staleArgs...), staleArgs...)
	ids, tids, err := doomed(ctx, db, "folder_id IN "+gone+" OR server_folder_id IN "+gone, twice...)
	if err != nil {
		return nil, err
	}
	stmts = append(stmts,
		// Moved locally into a vanished folder but still elsewhere on the server: back to where it is.
		sqlite.Statement{SQL: "UPDATE messages SET folder_id = server_folder_id WHERE folder_id IN " + gone +
			" AND server_folder_id NOT IN " + gone, Args: twice},
		// On the server only in a vanished folder: gone, wherever it is shown locally.
		sqlite.Statement{SQL: "DELETE FROM messages WHERE server_folder_id IN " + gone, Args: staleArgs},
		sqlite.Statement{SQL: "DELETE FROM folders WHERE " + stale, Args: staleArgs}) // cascades to the rest
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return nil, err
	}
	r.gone(ids, tids)
	rows, err := db.Query(ctx, "SELECT id, name, uidvalidity, last_uid, synced, filled_since FROM folders WHERE account_id = ?", accountID)
	if err != nil {
		return nil, err
	}
	byName := map[string]*folder{}
	for _, r := range rows.Values {
		byName[store.Str(r[1])] = &folder{id: store.Int(r[0]), name: store.Str(r[1]),
			uidValidity: uint32(store.Int(r[2])), lastUID: uint32(store.Int(r[3])), synced: store.Int(r[4]) != 0}
		if r[5] != nil {
			v := store.Int(r[5])
			byName[store.Str(r[1])].filled = &v
		}
	}
	out := make([]*folder, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// tidyThreads deletes conversations left without messages (their thread_refs
// cascade) and recomputes last_date of the touched ones that survive.
func tidyThreads(accountID int64, tids []int64) []sqlite.Statement {
	out := []sqlite.Statement{{
		SQL:  "DELETE FROM threads WHERE account_id = ? AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = threads.id)",
		Args: []any{accountID},
	}}
	for _, t := range slices.Compact(slices.Sorted(slices.Values(tids))) {
		out = append(out, sqlite.Statement{
			SQL:  "UPDATE threads SET last_date = (SELECT max(date) FROM messages WHERE thread_id = threads.id) WHERE id = ?",
			Args: []any{t},
		})
	}
	return out
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys[T any](s []T) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// SentFolder returns the name of the server's Sent folder, found the way sync
// assigns roles, or "" when the server has none.
func SentFolder(c *imapclient.Client) (string, error) {
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		return "", err
	}
	for _, d := range list {
		if role(d) == "sent" && !slices.Contains(d.Attrs, imap.MailboxAttrNoSelect) && !slices.Contains(d.Attrs, imap.MailboxAttrNonExistent) {
			return d.Mailbox, nil
		}
	}
	return "", nil
}

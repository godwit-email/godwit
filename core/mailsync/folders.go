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
	// Gmail's All Mail stands in for a missing archive (RoleFolder); its
	// Starred is the unified 已标星 view, so the sidebar shows it no row.
	imap.MailboxAttrAll: "all", imap.MailboxAttrFlagged: "flagged",
}

var roleNames = map[string]string{
	"sent": "sent", "sent items": "sent", "sent messages": "sent", "已发送": "sent",
	"trash": "trash", "deleted items": "trash", "deleted messages": "trash", "已删除": "trash",
	"drafts": "drafts", "草稿": "drafts",
	"junk": "junk", "junk email": "junk", "spam": "junk", "垃圾邮件": "junk",
	"archive": "archive", "归档": "archive",
}

// roles are the roles of LIST's folders, in its order: each folder's
// SPECIAL-USE role, else one guessed from its (last) name segment, unless
// another folder carries that role as SPECIAL-USE (Gmail's [Gmail]/Sent Mail
// beats a folder named Sent Messages).
func roles(list []*imap.ListData) []string {
	out := make([]string, len(list))
	marked := map[string]bool{}
	for i, d := range list {
		if out[i] = markedRole(d); out[i] != "" {
			marked[out[i]] = true
		}
	}
	for i, d := range list {
		if g := guessedRole(d); out[i] == "" && !marked[g] {
			out[i] = g
		}
	}
	return out
}

// markedRole is the folder's SPECIAL-USE role, or inbox for INBOX.
func markedRole(d *imap.ListData) string {
	for _, a := range d.Attrs {
		if r, ok := specialUse[a]; ok {
			return r
		}
	}
	if strings.EqualFold(d.Mailbox, "INBOX") {
		return "inbox"
	}
	return ""
}

// guessedRole is the role its (last) name segment suggests.
func guessedRole(d *imap.ListData) string {
	name := d.Mailbox
	if d.Delim != 0 {
		name = name[strings.LastIndexByte(name, byte(d.Delim))+1:]
	}
	return roleNames[strings.ToLower(name)]
}

// delim is the folder's hierarchy delimiter, "" when the server has none.
func delim(d *imap.ListData) string {
	if d.Delim == 0 {
		return ""
	}
	return string(d.Delim)
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
	rs := roles(list)
	for i, d := range list {
		if !selectable(d) {
			continue
		}
		names = append(names, d.Mailbox)
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO folders(account_id, name, role, delim, role_since) VALUES (?, ?, ?, ?, unixepoch())
				ON CONFLICT(account_id, name) DO UPDATE SET role = excluded.role, delim = excluded.delim`,
			Args: []any{accountID, d.Mailbox, rs[i], delim(d)},
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
		sqlite.Statement{SQL: "UPDATE messages SET folder_id = server_folder_id, entered_at = unixepoch() WHERE folder_id IN " + gone +
			" AND server_folder_id NOT IN " + gone, Args: twice},
		// On the server only in a vanished folder: gone, wherever it is shown locally.
		sqlite.Statement{SQL: "DELETE FROM messages WHERE server_folder_id IN " + gone, Args: staleArgs},
		sqlite.Statement{SQL: "DELETE FROM folders WHERE " + stale, Args: staleArgs}) // cascades to the rest
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return nil, err
	}
	r.gone(ids, tids)
	rows, err := db.Query(ctx, "SELECT "+folderCols+" FROM folders WHERE account_id = ?", accountID)
	if err != nil {
		return nil, err
	}
	byName := map[string]*folder{}
	for _, r := range rows.Values {
		byName[store.Str(r[1])] = scanFolder(r)
	}
	out := make([]*folder, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

const folderCols = "id, name, uidvalidity, last_uid, synced, filled_since"

func scanFolder(r []any) *folder {
	f := &folder{id: store.Int(r[0]), name: store.Str(r[1]),
		uidValidity: uint32(store.Int(r[2])), lastUID: uint32(store.Int(r[3])), synced: store.Int(r[4]) != 0}
	if r[5] != nil {
		v := store.Int(r[5])
		f.filled = &v
	}
	return f
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

// Roles are the roles a user can give a folder (folders.role_override).
var Roles = []string{"drafts", "sent", "junk", "trash", "archive"}

// AltRole is the detected role that stands in for role when no folder is
// detected with it: Gmail's All Mail for archive. Else role itself.
func AltRole(role string) string {
	if role == "archive" {
		return "all"
	}
	return role
}

// RoleFolder is the account's folder of role, the one place roles resolve: the
// folder the user gave the role, else the lowest id detected with it, unless the
// user gave that folder another role or none. Without an archive detected,
// Gmail's All Mail is it: archiving there takes mail out of the inbox. 0 when
// there is none.
func RoleFolder(ctx context.Context, db *store.DB, accountID int64, role string) (id int64, name string, err error) {
	alt := AltRole(role)
	rows, err := db.Query(ctx, `SELECT id, name FROM folders WHERE account_id = ?
		AND (role_override = ? OR (role_override = '' AND role IN (?, ?))) ORDER BY role_override = ? DESC, role = ? DESC, id LIMIT 1`,
		accountID, role, role, alt, role, role)
	if err != nil || len(rows.Values) == 0 {
		return 0, "", err
	}
	return store.Int(rows.Values[0][0]), store.Str(rows.Values[0][1]), nil
}

// SentFolder returns the name of the account's Sent folder (RoleFolder), or ""
// when it has none. Before the first sync stored any folder, it is found from
// LIST the way sync assigns roles.
func SentFolder(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64) (string, error) {
	rows, err := db.Query(ctx, "SELECT count(*) FROM folders WHERE account_id = ?", accountID)
	if err != nil {
		return "", err
	}
	if store.Int(rows.Values[0][0]) > 0 {
		_, name, err := RoleFolder(ctx, db, accountID, "sent")
		return name, err
	}
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		return "", err
	}
	rs := roles(list)
	for i, d := range list {
		if rs[i] == "sent" && selectable(d) {
			return d.Mailbox, nil
		}
	}
	return "", nil
}

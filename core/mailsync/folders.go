package mailsync

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/godwit-email/godwit/core/store"
)

// folder is a stored folder and its backend's sync state.
type folder struct {
	id         int64
	ref        backend.FolderRef // server_ref
	name, role string
	cursor     string
	synced     bool   // a first sync at cursor completed
	filled     *int64 // the cutoff a range backfill completed down to
}

func (f *folder) state() backend.FolderState {
	st := backend.FolderState{Cursor: backend.Cursor(f.cursor), Synced: f.synced}
	if f.filled != nil {
		st.Filled = time.Unix(*f.filled, 0)
	}
	return st
}

func (f *folder) setState(st backend.FolderState) {
	f.cursor, f.synced, f.filled = string(st.Cursor), st.Synced, nil
	if !st.Filled.IsZero() {
		v := st.Filled.Unix()
		f.filled = &v
	}
}

const folderCols = "id, server_ref, name, role, cursor, synced, filled_since"

func scanFolder(r []any) *folder {
	f := &folder{id: store.Int(r[0]), ref: backend.FolderRef(store.Str(r[1])), name: store.Str(r[2]), role: store.Str(r[3]),
		cursor: store.Str(r[4]), synced: store.Int(r[5]) != 0}
	if r[6] != nil {
		v := store.Int(r[6])
		f.filled = &v
	}
	return f
}

// loadFolders reads the account's folders that are on the server.
func (s *syncRun) loadFolders(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT "+folderCols+" FROM folders WHERE account_id = ? AND server_ref IS NOT NULL", s.a.id)
	if err != nil {
		return err
	}
	s.folders = map[backend.FolderRef]*folder{}
	for _, r := range rows.Values {
		f := scanFolder(r)
		s.folders[f.ref] = f
	}
	return nil
}

// CountFolders is how many selectable folders LIST returns: those a sync mirrors.
func CountFolders(c *imapclient.Client) (int, error) {
	list, err := c.List("", "*", nil).Collect()
	n := 0
	for _, d := range list {
		if imapbackend.Selectable(d) {
			n++
		}
	}
	return n, err
}

// applyFolders mirrors the server's folders into the folders table: updated
// ones are added or renamed, gone ones deleted with the messages in them; a
// message only moved there locally returns to its server folder.
func (s *syncRun) applyFolders(ctx context.Context, updated []backend.Folder, gone []backend.FolderRef) error {
	db, accountID, r := s.db, s.a.id, s.r
	var stmts []sqlite.Statement
	for _, f := range updated {
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO folders(account_id, server_ref, name, role, delim, role_since) VALUES (?, ?, ?, ?, ?, unixepoch())
				ON CONFLICT(account_id, server_ref) DO UPDATE SET name = excluded.name, role = excluded.role, delim = excluded.delim`,
			Args: []any{accountID, string(f.Ref), f.Name, string(f.Role), f.Delim},
		})
	}
	var ids, tids, held []int64
	if len(gone) > 0 {
		stale := "account_id = ? AND server_ref IN (" + marks(len(gone)) + ")"
		staleArgs := append([]any{accountID}, anys(gone)...)
		goneIDs := "(SELECT id FROM folders WHERE " + stale + ")"
		twice := append(append([]any{}, staleArgs...), staleArgs...)
		in := "EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id AND folder_id IN " + goneIDs + " AND "
		var err error
		if ids, tids, err = doomed(ctx, db, in+"true)", staleArgs...); err != nil {
			return err
		}
		if held, _, err = doomed(ctx, db, in+"server_ref IS NOT NULL)", staleArgs...); err != nil {
			return err
		}
		shownElsewhere := "EXISTS (SELECT 1 FROM message_locations o WHERE o.message_id = %s AND o.shown AND o.folder_id NOT IN " + goneIDs + ")"
		keepPending := ""
		if s.stable {
			keepPending = " AND server_id IS NULL AND NOT EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND state IN ('pending', 'awaiting_claim'))"
		}
		stmts = append(stmts,
			// Shown only in a vanished folder but on the server elsewhere: back to where it is.
			sqlite.Statement{SQL: "UPDATE message_locations SET shown = 1, entered_at = unixepoch() WHERE server_ref IS NOT NULL AND folder_id NOT IN " + goneIDs +
				" AND message_id IN (SELECT message_id FROM message_locations WHERE shown AND folder_id IN " + goneIDs + ") AND NOT " +
				fmt.Sprintf(shownElsewhere, "message_locations.message_id"), Args: append(slices.Clone(twice), staleArgs...)},
			// Shown only in a vanished folder and on the server nowhere: gone too.
			sqlite.Statement{SQL: "DELETE FROM messages WHERE " + in + "shown) AND " + unlocated + keepPending + " AND NOT " + fmt.Sprintf(shownElsewhere, "messages.id"), Args: twice},
			// With the folders go their locations; a message the server then holds nowhere is orphaned.
			sqlite.Statement{SQL: "DELETE FROM folders WHERE " + stale, Args: staleArgs})
	}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return err
	}
	r.gone(ids, tids)
	if err := r.orphan(ctx, db, s.stable, accountID, held); err != nil {
		return err
	}
	if err := s.loadFolders(ctx); err != nil {
		return err
	}
	if s.stable && !s.merged { // rows stored per folder before a sync fetched their identity (ADR 0034)
		s.merged = true
		return mergeShared(ctx, db, accountID, nil, r)
	}
	return nil
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
	rs := imapbackend.Roles(list)
	for i, d := range list {
		if rs[i] == backend.RoleSent && imapbackend.Selectable(d) {
			return d.Mailbox, nil
		}
	}
	return "", nil
}

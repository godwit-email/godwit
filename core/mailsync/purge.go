package mailsync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/store"
)

// PurgeRoles are the roles a purge can empty.
var PurgeRoles = []string{"junk", "trash"}

// purgePayload is a purge op's payload: the role whose folder (dst_folder_id) it empties.
type purgePayload struct {
	Role string `json:"role"`
}

// purgeable is the condition on message m (with policy p and its folder f joined)
// that a purge may expunge it: shown and on the server in the role's folder at
// the folder's UIDVALIDITY, no other change of it waiting, and in that folder
// longer than the policy's days, counted from the latest of when Godwit first saw
// it there, when the folder got the role and when the purge was turned on. The
// argument is an op id the check ignores (the purge being pushed), else 0.
const purgeable = `m.folder_id = p.folder_id AND m.server_folder_id = p.folder_id AND m.server_uidvalidity = f.uidvalidity
	AND p.days > 0 AND m.entered_at > 0 AND max(m.entered_at, f.role_since, p.since) <= unixepoch() - p.days * 86400
	AND NOT EXISTS (SELECT 1 FROM ops o WHERE o.message_id = m.id AND o.state IN ('pending', 'awaiting_claim') AND o.id != ?)`

// QueuePurge queues the account's due junk and trash purges as purge ops, which
// PushOps expunges after checking again. A folder is purged at most once a day,
// and only on a server with UIDPLUS: a plain EXPUNGE would also remove mail other
// clients marked \Deleted. When the role's folder is not the one the policy's
// clock ran for, the clock restarts there and nothing is purged.
func QueuePurge(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64) error {
	rows, err := db.Query(ctx, "SELECT role, folder_id FROM purge_policies WHERE account_id = ? AND days > 0", accountID)
	if err != nil {
		return err
	}
	blocked := !c.Caps().Has(imap.CapUIDPlus)
	for _, r := range rows.Values {
		role, clock := store.Str(r[0]), store.Int(r[1])
		if _, err := db.Execute(ctx, "UPDATE purge_policies SET blocked = ? WHERE account_id = ? AND role = ?", blocked, accountID, role); err != nil {
			return err
		}
		folder, _, err := RoleFolder(ctx, db, accountID, role)
		if err != nil {
			return err
		}
		if folder != clock {
			if _, err := db.Execute(ctx, "UPDATE purge_policies SET folder_id = ?, since = unixepoch() WHERE account_id = ? AND role = ?",
				nullID(folder), accountID, role); err != nil {
				return err
			}
			continue
		}
		if blocked || folder == 0 {
			continue
		}
		due, err := db.Query(ctx, "SELECT purged_at <= unixepoch() - 86400 FROM folders WHERE id = ?", folder)
		if err != nil || len(due.Values) == 0 || store.Int(due.Values[0][0]) == 0 {
			if err != nil {
				return err
			}
			continue
		}
		payload, _ := json.Marshal(purgePayload{Role: role})
		from := ` FROM messages m JOIN purge_policies p ON p.account_id = m.account_id AND p.role = ?
			JOIN folders f ON f.id = p.folder_id WHERE m.account_id = ? AND ` + purgeable
		if _, err := db.Transaction(ctx, []sqlite.Statement{
			{SQL: `INSERT INTO ops(account_id, message_id, kind, dst_folder_id, payload, created_at)
				SELECT m.account_id, m.id, 'purge', p.folder_id, ?, unixepoch()` + from, Args: []any{string(payload), role, accountID, 0}},
			{SQL: "UPDATE folders SET purged_at = unixepoch() WHERE id = ?", Args: []any{folder}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// purgeStillDue checks a purge op again as it is pushed: the message must still
// be purgeable and its folder still the role's folder, on a server with UIDPLUS.
func purgeStillDue(ctx context.Context, db *store.DB, c *imapclient.Client, o op) (bool, error) {
	var p purgePayload
	if json.Unmarshal([]byte(o.payload), &p) != nil || !c.Caps().Has(imap.CapUIDPlus) {
		return false, nil
	}
	r, err := db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", o.msg)
	if err != nil || len(r.Values) == 0 {
		return false, err
	}
	acc := store.Int(r.Values[0][0])
	folder, _, err := RoleFolder(ctx, db, acc, p.Role)
	if err != nil || folder != o.dst {
		return false, err
	}
	r, err = db.Query(ctx, `SELECT 1 FROM messages m JOIN purge_policies p ON p.account_id = m.account_id AND p.role = ?
		JOIN folders f ON f.id = p.folder_id WHERE m.id = ? AND p.folder_id = ? AND `+purgeable, p.Role, o.msg, o.dst, o.id)
	return err == nil && len(r.Values) > 0, err
}

// expunge removes the message with uid from the selected folder: \Deleted, then
// UID EXPUNGE of that UID only.
func expunge(c *imapclient.Client, uid imap.UID) error {
	set := imap.UIDSetNum(uid)
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return err
	}
	return c.UIDExpunge(set).Close()
}

// auditPurges writes one audit_log row per folder a push expunged mail from:
// the account, folder, count and the policy's period, never subjects.
func auditPurges(ctx context.Context, db *store.DB, accountID int64, counts map[int64]int) error {
	var stmts []sqlite.Statement
	for folder, n := range counts {
		days := int64(0)
		if r, err := db.Query(ctx, "SELECT days FROM purge_policies WHERE account_id = ? AND folder_id = ?", accountID, folder); err == nil && len(r.Values) > 0 {
			days = store.Int(r.Values[0][0])
		}
		now := time.Now().Unix()
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO audit_log(at, source, tool, target_kind, target_id, decision, reason)
			VALUES (?, 'purge', 'purge', 'folder', ?, 'expunged', ?)`,
			Args: []any{now, folder, fmt.Sprintf("account %d: %d message(s) in the folder over %d day(s), cutoff %d", accountID, n, days, now-days*86400)}})
	}
	if len(stmts) == 0 {
		return nil
	}
	_, err := db.Transaction(ctx, stmts)
	return err
}

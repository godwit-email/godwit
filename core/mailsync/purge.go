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

// purgePayload is a purge op's payload: the role whose folder (dst_folder_id) it
// empties. Now marks one the user asked for, which skips the policy's checks.
type purgePayload struct {
	Role string `json:"role"`
	Now  bool   `json:"now,omitempty"`
}

// PurgeNowRole is the payload of a purge op the user asked for: it expunges the
// message from the specified role folder (trash, junk) at once, whatever the policy.
func PurgeNowRole(role string) string {
	b, _ := json.Marshal(purgePayload{Role: role, Now: true})
	return string(b)
}

// PurgeNow is the payload of a purge op the user asked for: it expunges the
// message from Trash at once, whatever the policy.
var PurgeNow = PurgeNowRole("trash")

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

// PurgeStep, when set (tests only), runs at each step of a purge's push:
// "selected", then "stored" (\Deleted set, UID EXPUNGE not sent yet).
var PurgeStep func(step string)

// claimable is the condition on the ops row of a purge that it may go on: still
// pending (a rescue cancels it) and its message still purgeable. Its arguments
// are the role and the op's id.
const claimable = `state = 'pending' AND EXISTS (SELECT 1 FROM messages m JOIN purge_policies p ON p.account_id = m.account_id
	AND p.role = ? JOIN folders f ON f.id = p.folder_id WHERE m.id = ops.message_id AND p.folder_id = ops.dst_folder_id AND ` + purgeable + `)`

// claimableNow is claimable for a purge the user asked for: still pending, its
// message shown and on the server in the op's folder at the folder's UIDVALIDITY,
// and no other change of it waiting.
const claimableNow = `state = 'pending' AND EXISTS (SELECT 1 FROM messages m JOIN folders f ON f.id = ops.dst_folder_id
	WHERE m.id = ops.message_id AND m.folder_id = f.id AND m.server_folder_id = f.id AND m.server_uidvalidity = f.uidvalidity
	AND NOT EXISTS (SELECT 1 FROM ops o WHERE o.message_id = m.id AND o.state IN ('pending', 'awaiting_claim') AND o.id != ops.id))`

// purgeClaim is the claimable condition of a purge op with payload p and id, and its arguments.
func purgeClaim(p purgePayload, id int64) (string, []any) {
	if p.Now {
		return claimableNow, nil
	}
	return claimable, []any{p.Role, id}
}

// purgeStillDue checks a purge op as it is pushed: claimable, its folder still
// the role's folder, on a server with UIDPLUS.
func purgeStillDue(ctx context.Context, db *store.DB, c *imapclient.Client, o op) (bool, error) {
	var p purgePayload
	if json.Unmarshal([]byte(o.payload), &p) != nil || !c.Caps().Has(imap.CapUIDPlus) {
		return false, nil
	}
	r, err := db.Query(ctx, "SELECT account_id FROM ops WHERE id = ?", o.id)
	if err != nil || len(r.Values) == 0 {
		return false, err
	}
	folder, _, err := RoleFolder(ctx, db, store.Int(r.Values[0][0]), p.Role)
	if err != nil || folder != o.dst {
		return false, err
	}
	cond, args := purgeClaim(p, o.id)
	r, err = db.Query(ctx, "SELECT 1 FROM ops WHERE id = ? AND "+cond, append([]any{o.id}, args...)...)
	return err == nil && len(r.Values) > 0, err
}

// pushPurge expunges the message of purge op o, which is at uid in folder
// srcName. It claims the op in one statement before sending anything and checks
// it again after SELECT and between STORE \Deleted and UID EXPUNGE; when it no
// longer holds (a rescue, the role or the policy changed) the op is cancelled
// and any \Deleted a purge of it set is removed again. The one window left is
// the UID EXPUNGE's own round trip.
func pushPurge(ctx context.Context, db *store.DB, c *imapclient.Client, o op, srcName string, srcValidity uint32, uid imap.UID) (string, error) {
	flagged := o.attempts > 0 // an earlier attempt may have set \Deleted
	selected := false
	cancel := func() (string, error) {
		if flagged {
			if !selected {
				if _, err := c.Select(srcName, nil).Wait(); err != nil {
					return fail(ctx, db, o, err)
				}
			}
			if err := undelete(c, uid); err != nil {
				return fail(ctx, db, o, err)
			}
		}
		return "", finish(ctx, db, o, "cancelled", "")
	}
	var p purgePayload
	_ = json.Unmarshal([]byte(o.payload), &p)
	if p.Now && !c.Caps().Has(imap.CapUIDPlus) {
		msg := "the server does not support UIDPLUS, so mail cannot be deleted permanently without risking other mail"
		return msg, finish(ctx, db, o, "failed", msg)
	}
	due, err := purgeStillDue(ctx, db, c, o)
	if err != nil {
		return "", err
	}
	if !due {
		return cancel()
	}
	cond, args := purgeClaim(p, o.id)
	res, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ? AND "+cond, append([]any{o.id}, args...)...)
	if err != nil {
		return "", err
	}
	if res.Changes != 1 {
		return cancel()
	}
	o.attempts++
	sel, err := c.Select(srcName, nil).Wait()
	if err != nil {
		return fail(ctx, db, o, err)
	}
	selected = true
	if sel.UIDValidity != srcValidity { // the folder was recreated: nothing of ours to purge
		return "", finish(ctx, db, o, "cancelled", "")
	}
	if !stillDue(ctx, db, c, o, "selected") {
		return cancel()
	}
	set := imap.UIDSetNum(uid)
	flagged = true
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		undelete(c, uid) // best effort; a rescue's move removes it too
		return fail(ctx, db, o, err)
	}
	if !stillDue(ctx, db, c, o, "stored") {
		return cancel()
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		undelete(c, uid) // best effort, as above
		return fail(ctx, db, o, err)
	}
	// Recorded even if a rescue cancelled it meanwhile: the message is gone.
	_, err = db.Execute(ctx, "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", o.id)
	return "", err
}

// stillDue runs the PurgeStep hook for step, then checks the purge again; a
// database error counts as no.
func stillDue(ctx context.Context, db *store.DB, c *imapclient.Client, o op, step string) bool {
	if PurgeStep != nil {
		PurgeStep(step)
	}
	due, err := purgeStillDue(ctx, db, c, o)
	return err == nil && due
}

// undelete removes \Deleted from uid in the selected folder.
func undelete(c *imapclient.Client, uid imap.UID) error {
	return c.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
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

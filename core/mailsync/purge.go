package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
)

// PurgeRoles are the roles a purge can empty.
var PurgeRoles = []string{"junk", "trash"}

// purgePayload is a purge op's payload: the role whose folder (dst_folder_id) it
// empties. Now marks one the user asked for, which skips the policy's checks.
// DueAt (unix milliseconds) ends the undo window of one the user asked for: the
// push leaves it until then.
type purgePayload struct {
	Role  string `json:"role"`
	Now   bool   `json:"now,omitempty"`
	DueAt int64  `json:"due_at,omitempty"`
}

// PurgeNowRole is the payload of a purge op the user asked for: it expunges the
// message from the role's folder (trash, junk) at due, whatever the policy.
func PurgeNowRole(role string, due time.Time) string {
	b, _ := json.Marshal(purgePayload{Role: role, Now: true, DueAt: due.UnixMilli()})
	return string(b)
}

// opDue is the durable undo deadline of a purge or a last-location archive.
const opDue = "CASE WHEN kind IN ('purge', 'move') THEN IFNULL(json_extract(payload, '$.due_at'), 0) ELSE 0 END"

// opReady also releases a deferred archive once fetch finds another server location.
const opReady = "(" + opDue + ` <= ? OR (kind = 'move' AND dst_folder_id IS NULL AND EXISTS
	(SELECT 1 FROM message_locations l WHERE l.message_id = ops.message_id AND l.folder_id != ops.src_folder_id AND l.server_ref IS NOT NULL)))`

// NextOpDue is the account's first undo deadline after t; zero for none.
func NextOpDue(ctx context.Context, db *store.DB, accountID int64, t time.Time) (time.Time, error) {
	rows, err := db.Query(ctx, `SELECT MIN(`+opDue+`) FROM ops
		WHERE account_id = ? AND state = 'pending' AND `+opDue+` > ?`, accountID, t.UnixMilli())
	if err != nil || len(rows.Values) == 0 || rows.Values[0][0] == nil {
		return time.Time{}, err
	}
	return time.UnixMilli(store.Int(rows.Values[0][0])), nil
}

// onServer is the condition that location l is a server location. One of a
// generation the backend voided is never left: a FolderReset deletes them.
const onServer = "l.server_ref IS NOT NULL"

// purgeable is the condition on message m (with policy p and its folder f joined)
// that a purge may expunge it: shown and on the server in the role's folder, no other change of it waiting, and in that folder
// longer than the policy's days, counted from the latest of when Godwit first saw
// it there, when the folder got the role and when the purge was turned on. The
// argument is an op id the check ignores (the purge being pushed), else 0.
var purgeable = `EXISTS (SELECT 1 FROM message_locations l WHERE l.message_id = m.id AND l.folder_id = p.folder_id AND l.shown
		AND ` + onServer + ` AND l.entered_at > 0 AND max(l.entered_at, f.role_since, p.since) <= unixepoch() - p.days * 86400)
	AND p.days > 0 AND NOT EXISTS (SELECT 1 FROM ops o WHERE o.message_id = m.id AND o.state IN ('pending', 'awaiting_claim') AND o.id != ?)`

// QueuePurge queues the account's due junk and trash purges as purge ops, which
// PushOps expunges after checking again. A folder is purged at most once a day,
// and only on a server whose backend declares the purge feature (IMAP's needs
// UIDPLUS: a plain EXPUNGE would also remove mail other clients marked
// \Deleted). When the role's folder is not the one the policy's clock ran for,
// the clock restarts there and nothing is purged.
func QueuePurge(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64) error {
	rows, err := db.Query(ctx, "SELECT role, folder_id FROM purge_policies WHERE account_id = ? AND days > 0", accountID)
	if err != nil {
		return err
	}
	d, err := recv.Describe(ctx)
	if err != nil {
		return err
	}
	blocked := d.Features[backend.FeaturePurge].Level == backend.LevelUnsupported
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
			{SQL: `INSERT INTO ops(account_id, message_id, kind, src_folder_id, dst_folder_id, payload, created_at)
				SELECT m.account_id, m.id, 'purge', p.folder_id, p.folder_id, ?, unixepoch()` + from, Args: []any{string(payload), role, accountID, 0}},
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

// PurgeStep, when set (tests only), runs at each Confirm of a purge's push:
// "selected", then "stored" (on IMAP: \Deleted set, UID EXPUNGE not sent yet).
var PurgeStep func(step string)

// claimable is the condition on the ops row of a purge that it may go on: still
// pending (a rescue cancels it) and its message still purgeable. Its arguments
// are the role and the op's id.
var claimable = `state = 'pending' AND EXISTS (SELECT 1 FROM messages m JOIN purge_policies p ON p.account_id = m.account_id
	AND p.role = ? JOIN folders f ON f.id = p.folder_id WHERE m.id = ops.message_id AND p.folder_id = ops.dst_folder_id AND ` + purgeable + `)`

// claimableNow is claimable for a purge the user asked for: still pending, its
// message shown and on the server in the op's folder,
// and no other change of it waiting.
var claimableNow = `state = 'pending' AND EXISTS (SELECT 1 FROM messages m JOIN folders f ON f.id = ops.dst_folder_id
	WHERE m.id = ops.message_id AND EXISTS (SELECT 1 FROM message_locations l WHERE l.message_id = m.id AND l.folder_id = f.id AND l.shown
		AND ` + onServer + `)
	AND NOT EXISTS (SELECT 1 FROM ops o WHERE o.message_id = m.id AND o.state IN ('pending', 'awaiting_claim') AND o.id != ops.id))`

// purgeClaim is the claimable condition of a purge op with payload p and id, and its arguments.
func purgeClaim(p purgePayload, id int64) (string, []any) {
	if p.Now {
		return claimableNow, nil
	}
	return claimable, []any{p.Role, id}
}

// purgeStillDue checks a purge op as it is pushed: claimable, its folder still
// the role's folder, on a server that can purge.
func (pu *pusher) purgeStillDue(ctx context.Context, o op) (bool, error) {
	db := pu.db
	var p purgePayload
	if json.Unmarshal([]byte(o.payload), &p) != nil || !pu.canPurge {
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

// pushPurge deletes the message of purge op o, at at, for good. It claims the
// op in one statement before sending anything, and the backend's Confirm
// checks it again before each step; when it no longer holds (a rescue, the
// role or the policy changed) the op is cancelled, and the backend undoes
// what a purge of it left on the server.
func (pu *pusher) pushPurge(ctx context.Context, o op, at backend.Location) (string, error) {
	db := pu.db
	apply := func(confirm func() bool) (string, error) {
		res, err := pu.recv.Apply(ctx, backend.Purge{At: at, Confirm: confirm})
		switch {
		case errors.Is(err, backend.ErrNotFound): // the folder was recreated: nothing of ours to purge
			return "", finish(ctx, db, o, "cancelled", "")
		case err != nil:
			return fail(ctx, db, o, err, true)
		case res.Outcome == backend.Failed && res.Reason == backend.Cancelled:
			return "", finish(ctx, db, o, "cancelled", "")
		case res.Outcome == backend.Failed:
			return fail(ctx, db, o, errors.New(res.Reason), false)
		case res.Outcome == backend.Unsupported:
			msg := unsupported(res.Reason)
			return msg, finish(ctx, db, o, "failed", msg)
		}
		// Recorded even if a rescue cancelled it meanwhile: the message is gone.
		_, err = db.Execute(ctx, "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", o.id)
		return "", err
	}
	cancel := func() (string, error) {
		if o.attempts > 0 { // an earlier attempt may have set \Deleted
			return apply(func() bool { return false })
		}
		return "", finish(ctx, db, o, "cancelled", "")
	}
	var p purgePayload
	_ = json.Unmarshal([]byte(o.payload), &p)
	if p.Now && !pu.canPurge {
		msg := "the server cannot delete mail permanently without risking other mail"
		return msg, finish(ctx, db, o, "failed", msg)
	}
	due, err := pu.purgeStillDue(ctx, o)
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
	steps := []string{"selected", "stored"}
	return apply(func() bool {
		step := steps[0]
		if len(steps) > 1 {
			steps = steps[1:]
		}
		return pu.stillDue(ctx, o, step)
	})
}

// stillDue runs the PurgeStep hook for step, then checks the purge again; a
// database error counts as no.
func (pu *pusher) stillDue(ctx context.Context, o op, step string) bool {
	if PurgeStep != nil {
		PurgeStep(step)
	}
	due, err := pu.purgeStillDue(ctx, o)
	return err == nil && due
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

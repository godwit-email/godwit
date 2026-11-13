package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
)

// The op queue (GLOSSARY 操作队列): changes made locally that wait to be
// pushed to the server. This file is the only writer of the ops table. The
// push (ops.go, purge.go, gmail_ops.go) and fetch record what became of an op
// through it, every change of state through opsTo.

// MaxAttempts is how many server refusals an op takes before it is marked failed.
const MaxAttempts = 5

// The states of an op.
const (
	opPending   = "pending"        // waits for the push
	opAwaiting  = "awaiting_claim" // a move the server did without saying where: fetch claims the arrival
	opDone      = "done"           // the server has it
	opConflict  = "conflict"       // the server's mail no longer fits it
	opFailed    = "failed"         // refused MaxAttempts times, or the server cannot do it
	opCancelled = "cancelled"      // taken back (a rescue, an undo), or nothing was left to do
)

// opFrom is the op queue's state machine, the states each state is entered
// from; opsTo makes no other change:
//
//	→ pending (queued)
//	pending → pending (an attempt's error recorded) | awaiting_claim (moved, no location given) | done | conflict | failed | cancelled
//	awaiting_claim → done (claimed) | conflict (unclaimable; its message, folder or account gone)
//	cancelled → done (a purge whose expunge was under way when a rescue cancelled it: the message is gone)
var opFrom = map[string][]string{
	opPending:   {opPending},
	opAwaiting:  {opPending},
	opDone:      {opPending, opAwaiting, opCancelled},
	opConflict:  {opPending, opAwaiting},
	opFailed:    {opPending},
	opCancelled: {opPending},
}

// unfinished is the states, as an SQL list, of an op still to do: a later op
// of its message waits for it.
const unfinished = "('" + opPending + "', '" + opAwaiting + "')"

var (
	fromPending    = []string{opPending}
	fromAwaiting   = []string{opAwaiting}
	fromUnfinished = []string{opPending, opAwaiting}
)

// opsTo is the statement that moves the ops matching where from one of from
// to state to, also setting set ("column = ?, ..."); args are set's, then
// where's. An op in another state is left alone: it changed since it was
// read. An edge opFrom lacks panics: callers name constant states, so a test
// that runs the path finds it.
func opsTo(to string, from []string, set, where string, args ...any) sqlite.Statement {
	if _, ok := opFrom[to]; !ok {
		panic("op queue: no state " + to)
	}
	for _, f := range from {
		if !slices.Contains(opFrom[to], f) {
			panic(fmt.Sprintf("op queue: no edge from %s to %s", f, to))
		}
	}
	if set != "" {
		set = ", " + set
	}
	return sqlite.Statement{SQL: "UPDATE ops SET state = '" + to + "'" + set + " WHERE (" + where + ") AND state IN ('" + strings.Join(from, "', '") + "')",
		Args: args}
}

// opDoneStmt is the statement ending pending op id as done.
func opDoneStmt(id int64) sqlite.Statement {
	return opsTo(opDone, fromPending, "last_error = NULL", "id = ?", id)
}

type op struct {
	id, msg, src, dst, dep, accountID int64
	kind, payload                     string
	attempts, refusals                int
}

// finish moves pending op o to state with lastError and its refusals. A move
// that ends failed or conflict puts the message back in the folders the
// server has it in (settle); a move queued after it from its destination then
// moves it from its source.
func finish(ctx context.Context, db *store.DB, o op, state, lastError string) error {
	stmts := []sqlite.Statement{opsTo(state, fromPending, "refusals = ?, last_error = ?", "id = ?", o.refusals, nullable(lastError), o.id)}
	if o.kind == "move" && (state == opFailed || state == opConflict) {
		stmts = append(append(stmts,
			sqlite.Statement{SQL: `UPDATE ops SET src_folder_id = ? WHERE message_id = ? AND kind IN ('move', 'purge') AND state = 'pending'
				AND src_folder_id = ? AND id > ?`, Args: []any{o.src, o.msg, o.dst, o.id}}),
			settle(o.msg)...)
	}
	_, err := db.Transaction(ctx, stmts)
	return err
}

// fail records a failed attempt. Only server refusals (!stop) count toward
// MaxAttempts (a8e885f4); an uncertain or refused read error (stop) leaves
// the op pending, and anything other than a *backend.FolderError stops the
// push.
func fail(ctx context.Context, db *store.DB, o op, cause error, stop bool) (string, error) {
	if stop {
		err := finish(ctx, db, o, opPending, cause.Error())
		var refused *backend.FolderError
		if errors.As(cause, &refused) {
			return "", err // a refused read: the push goes on
		}
		return "", errors.Join(cause, err)
	}
	o.refusals++
	state, problem := opPending, ""
	if o.refusals >= MaxAttempts {
		state, problem = opFailed, fmt.Sprintf("a change was given up after %d attempts: %v", o.refusals, cause)
	}
	if err := finish(ctx, db, o, state, cause.Error()); err != nil {
		return problem, errors.Join(cause, err)
	}
	return problem, nil
}

// count adds n to o's attempts, in the row and in o. An attempt is counted
// before anything is sent, so after a dropped connection the next round knows
// it may have reached the server (ADR 0006); one that wrote nothing is taken
// back.
func count(ctx context.Context, db *store.DB, o *op, n int) error {
	if _, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + ? WHERE id = ?", n, o.id); err != nil {
		return err
	}
	o.attempts += n
	return nil
}

// noteError records cause on pending op id, whose state stays.
func noteError(ctx context.Context, db *store.DB, id int64, cause string) error {
	_, err := db.Execute(ctx, "UPDATE ops SET last_error = ? WHERE id = ?", cause, id)
	return err
}

// waiting reports whether o waits for the op it depends on, still unfinished.
// Once that one ended otherwise (failed, conflict) o no longer depends on it
// and runs from the message's current server location.
func waiting(ctx context.Context, db *store.DB, o op) (bool, error) {
	if o.dep == 0 {
		return false, nil
	}
	r, err := db.Query(ctx, "SELECT 1 FROM ops WHERE id = ? AND state IN "+unfinished, o.dep)
	if err != nil || len(r.Values) > 0 {
		return err == nil, err
	}
	_, err = db.Execute(ctx, "UPDATE ops SET depends_on = NULL WHERE id = ?", o.id)
	return false, err
}

// saveBaseline keeps the destination's baseline a move's backend took before
// sending, for a retry to tell its copy from mail already there.
func saveBaseline(ctx context.Context, db *store.DB, id int64, b backend.MoveBaseline) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = db.Execute(ctx, "UPDATE ops SET payload = json_set(coalesce(nullif(payload, ''), '{}'), '$.before', json(?)) WHERE id = ?", string(raw), id)
	return err
}

// mergeOps are the statements that give keep the ops of the messages in in
// (an SQL list, with args), re-chained by id, each keeping its source folder
// (merge, ADR 0034).
func mergeOps(keep int64, in string, args []any) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: "UPDATE ops SET message_id = ? WHERE message_id IN " + in, Args: append([]any{keep}, args...)},
		{SQL: `UPDATE ops SET depends_on = (SELECT max(p.id) FROM ops p WHERE p.message_id = ops.message_id AND p.state IN ` + unfinished + ` AND p.id < ops.id)
			WHERE message_id = ? AND state = 'pending'`, Args: []any{keep}},
	}
}

// claimPurge counts an attempt of purge op o, in the row and in o, if cond
// (with args) still holds for its row: the purge is claimed in one statement
// before anything is sent.
func claimPurge(ctx context.Context, db *store.DB, o *op, cond string, args []any) (bool, error) {
	res, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ? AND "+cond, append([]any{o.id}, args...)...)
	if err != nil || res.Changes != 1 {
		return false, err
	}
	o.attempts++
	return true, nil
}

// latestOp is message ?'s newest unfinished op, which a new op of it waits for.
const latestOp = "(SELECT max(id) FROM ops WHERE message_id = ? AND state IN " + unfinished + ")"

// flagsPayload is a flags op's payload: flags to add, then flags to remove.
type flagsPayload struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// movePayload is a move op's payload. DueAt (unix milliseconds, as opDue
// reads it) ends the undo window of an archive from the message's last
// visible Gmail location: the push leaves it until then. Before is the
// destination's baseline its backend took before sending (saveBaseline).
type movePayload struct {
	DueAt  int64                 `json:"due_at,omitempty"`
	Before *backend.MoveBaseline `json:"before,omitempty"`
}

// purgePayload is a purge op's payload: the role whose folder (dst_folder_id) it
// empties. Now marks one the user asked for, which skips the policy's checks.
// DueAt (unix milliseconds, as opDue reads it) ends the undo window of one the
// user asked for: the push leaves it until then.
type purgePayload struct {
	Role  string `json:"role"`
	Now   bool   `json:"now,omitempty"`
	DueAt int64  `json:"due_at,omitempty"`
}

func payload(v any) string {
	b, _ := json.Marshal(v) // the payloads are strings, numbers and bools
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

// ready is the condition that a pending op is due at ? (and, newOnly, never attempted).
func ready(newOnly bool) string {
	if newOnly {
		return "state = 'pending' AND " + opReady + " AND attempts = 0"
	}
	return "state = 'pending' AND " + opReady
}

// readyOps are the ids of the account's ops ready to push at now, oldest first.
func readyOps(ctx context.Context, db *store.DB, accountID int64, now time.Time, newOnly bool) ([]int64, error) {
	rows, err := db.Query(ctx, "SELECT id FROM ops WHERE account_id = ? AND "+ready(newOnly)+" ORDER BY id", accountID, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(rows.Values))
	for i, v := range rows.Values {
		ids[i] = store.Int(v[0])
	}
	return ids, nil
}

// readyOp reads op id of the account, if it is still ready at now.
func readyOp(ctx context.Context, db *store.DB, accountID, id int64, now time.Time, newOnly bool) (op, bool, error) {
	r, err := db.Query(ctx, `SELECT kind, message_id, src_folder_id, dst_folder_id, payload, depends_on, attempts, refusals FROM ops
		WHERE id = ? AND `+ready(newOnly), id, now.UnixMilli())
	if err != nil || len(r.Values) == 0 {
		return op{}, false, err
	}
	v := r.Values[0]
	return op{id: id, kind: store.Str(v[0]), msg: store.Int(v[1]), src: store.Int(v[2]), dst: store.Int(v[3]), accountID: accountID,
		payload: store.Str(v[4]), dep: store.Int(v[5]), attempts: int(store.Int(v[6])), refusals: int(store.Int(v[7]))}, true, nil
}

// queuePolicyPurges queues a purge by the policy of role of every message
// from (a FROM clause over messages m and purge_policies p, with args)
// selects.
func queuePolicyPurges(role, from string, args ...any) sqlite.Statement {
	return sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, src_folder_id, dst_folder_id, payload, created_at)
		SELECT m.account_id, m.id, 'purge', p.folder_id, p.folder_id, ?, unixepoch()` + from, Args: append([]any{payload(purgePayload{Role: role})}, args...)}
}

// The queue as core changes it: each Queue* statement queues one change the
// user made locally, after the message's unfinished ops. Core runs them in
// the transaction that makes the change locally.

// QueueFlags queues adding then removing flags (canonical names) on message msg.
func QueueFlags(accountID, msg int64, add, remove []string) sqlite.Statement {
	return sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, payload, depends_on, created_at)
		VALUES (?, ?, 'flags', ?, ` + latestOp + `, unixepoch())`, Args: []any{accountID, msg, payload(flagsPayload{add, remove}), msg}}
}

// QueueMove queues moving message msg out of folder src into dst; dst 0
// only takes it out of src (Gmail's archive with All Mail hidden). A non-zero
// due opens an undo window: the push leaves the move until then. Nothing is
// queued on an inbox-only account, where a move is local (InboxOnly).
func QueueMove(accountID, msg, src, dst int64, due time.Time) sqlite.Statement {
	var p movePayload
	if !due.IsZero() {
		p.DueAt = due.UnixMilli()
	}
	return sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, src_folder_id, dst_folder_id, payload, depends_on, created_at)
		SELECT ?, ?, 'move', ?, ?, ?, ` + latestOp + `, unixepoch() WHERE NOT ` + InboxOnly, Args: []any{accountID, msg, src, nullID(dst), payload(p), msg, accountID}}
}

// QueueRestore queues adding message msg to folder again (Gmail: its label
// back), for one transaction. With takeBack, its archive out of folder that
// nothing has sent yet is cancelled instead.
func QueueRestore(ctx context.Context, db *store.DB, accountID, msg, folder int64, takeBack bool) ([]sqlite.Statement, error) {
	var cancel int64
	if takeBack {
		r, err := db.Query(ctx, `SELECT id FROM ops WHERE message_id = ? AND kind = 'move' AND src_folder_id = ?
			AND dst_folder_id IS NULL AND state = 'pending' AND attempts = 0 ORDER BY id DESC LIMIT 1`, msg, folder)
		if err != nil {
			return nil, err
		}
		if len(r.Values) > 0 {
			cancel = store.Int(r.Values[0][0])
		}
	}
	return []sqlite.Statement{
		opsTo(opCancelled, fromPending, "", "id = ? AND attempts = 0", cancel),
		{SQL: `INSERT INTO ops(account_id, message_id, kind, dst_folder_id, depends_on, created_at)
			SELECT ?, ?, 'move', ?, ` + latestOp + `, unixepoch() WHERE NOT EXISTS (SELECT 1 FROM ops WHERE id = ? AND state = 'cancelled')`,
			Args: []any{accountID, msg, folder, msg, cancel}},
	}, nil
}

// QueuePurgeNow queues deleting message msg for good from folder, the
// account's folder of role (trash, junk), at due, whatever the policy: until
// then CancelPurges takes it back.
func QueuePurgeNow(accountID, msg, folder int64, role string, due time.Time) sqlite.Statement {
	return sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, src_folder_id, dst_folder_id, payload, depends_on, created_at)
		VALUES (?, ?, 'purge', ?, ?, ?, ` + latestOp + `, unixepoch())`,
		Args: []any{accountID, msg, folder, folder, payload(purgePayload{Role: role, Now: true, DueAt: due.UnixMilli()}), msg}}
}

// CancelPurges cancels the purges of the messages that wait to be pushed (a
// rescue, an undo); its Changes count them. One being pushed still completes.
func CancelPurges(msgs ...int64) sqlite.Statement {
	return opsTo(opCancelled, fromPending, "", "kind = 'purge' AND message_id IN ("+marks(len(msgs))+")", anys(msgs)...)
}

// ConflictAll ends every unfinished op of the account as a conflict, with
// reason: the server they were queued for is gone.
func ConflictAll(accountID int64, reason string) sqlite.Statement {
	return opsTo(opConflict, fromUnfinished, "last_error = ?", "account_id = ?", reason, accountID)
}

// MarkSent leaves op id as a push cut off after sending does: an attempt
// counted that may have reached the server, with the destination's baseline
// before it when before is set. Tests reproduce a lost reply with it.
func MarkSent(ctx context.Context, db *store.DB, id int64, before *backend.MoveBaseline) error {
	if err := count(ctx, db, &op{id: id}, 1); err != nil || before == nil {
		return err
	}
	return saveBaseline(ctx, db, id, *before)
}

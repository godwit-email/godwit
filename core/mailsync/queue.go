package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

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

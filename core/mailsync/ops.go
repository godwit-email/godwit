package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/i18n"
)

// MaxAttempts is how many times an op is sent before it is marked failed.
const MaxAttempts = 5

// FlagsPayload is the payload of a flags op: flags to add, then flags to remove.
type FlagsPayload struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// OpsError lists ops that PushOps marked conflict or failed; the push went on.
// Changed holds messages shown in a different folder because a move ended so.
type OpsError struct {
	Problems []string
	Changed  []int64
}

func (e *OpsError) Error() string { return strings.Join(e.Problems, "; ") }

var errNoDestination = errors.New("the destination folder no longer exists")

type op struct {
	id, msg, src, dst, dep, accountID int64
	kind, payload                     string
	attempts                          int
}

// pusher is one push: the account's backend and what it declares.
type pusher struct {
	db        *store.DB
	recv      backend.Receiver
	stable    bool // IdentityStable: a message may have other locations
	inboxOnly bool // IdentityInboxOnly: a purge DELEs at the inbox location, wherever the message shows
	canPurge  bool
}

// PushOps sends the account's pending ops to the server in id order. Ops whose
// message has no server location yet, or whose dependency is still pending or
// awaiting its claim, wait for a later round, as do purges whose undo window is
// still open at now; once the dependency failed or conflicted the op is re-based
// (depends_on cleared) and runs from the message's current server location.
// The backend is asked only when an op is ready. Problems with single ops come
// back as *OpsError; any other error means the connection is unusable and the
// push stopped.
func PushOps(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, now time.Time) error {
	return push(ctx, db, recv, accountID, now, "")
}

// PushNewOps is PushOps limited to ops never attempted, for a second round after
// a fetch: ops that waited for a claim (an Unlocated move), for another server
// location (a deferred archive) or that were queued meanwhile run, ones that
// failed are not retried. Without such an op it sends nothing to the server.
func PushNewOps(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, now time.Time) error {
	return push(ctx, db, recv, accountID, now, " AND attempts = 0")
}

func push(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, now time.Time, filter string) (rerr error) {
	var problems []string
	var changed []int64
	// A purge of a message already gone has nothing left to do.
	if _, err := db.Execute(ctx, `UPDATE ops SET state = 'cancelled'
		WHERE account_id = ? AND message_id IS NULL AND kind = 'purge' AND state = 'pending'`, accountID); err != nil {
		return err
	}
	res, err := db.Execute(ctx, `UPDATE ops SET state = 'conflict', last_error = 'the message no longer exists'
		WHERE account_id = ? AND message_id IS NULL AND state IN ('pending', 'awaiting_claim')`, accountID)
	if err != nil {
		return err
	}
	purged := map[int64]int{} // expunged per folder, audited when the push ends
	defer func() {
		if aerr := auditPurges(context.WithoutCancel(ctx), db, accountID, purged); aerr != nil && rerr == nil {
			rerr = aerr
		}
	}()
	if res.Changes > 0 {
		problems = append(problems, fmt.Sprintf("%d change(s) not applied: the message no longer exists", res.Changes))
	}
	rows, err := db.Query(ctx, `SELECT id FROM ops
		WHERE account_id = ? AND state = 'pending' AND `+opReady+filter+` ORDER BY id`, accountID, now.UnixMilli())
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		if len(problems) > 0 {
			return &OpsError{problems, changed}
		}
		return nil
	}
	d, err := recv.Describe(ctx)
	if err != nil {
		return err
	}
	p := &pusher{db: db, recv: recv, stable: d.Identity == backend.IdentityStable, inboxOnly: d.Identity == backend.IdentityInboxOnly, canPurge: d.Features[backend.FeaturePurge].Level != backend.LevelUnsupported}
	for _, row := range rows.Values {
		// A preceding move can merge queues or rebase the next move's source.
		current, err := db.Query(ctx, `SELECT id, kind, message_id, src_folder_id, dst_folder_id, payload, depends_on, attempts FROM ops
			WHERE id = ? AND state = 'pending' AND `+opReady+filter, row[0], now.UnixMilli())
		if err != nil {
			return err
		}
		if len(current.Values) == 0 {
			continue
		}
		v := current.Values[0]
		o := op{id: store.Int(v[0]), kind: store.Str(v[1]), msg: store.Int(v[2]), src: store.Int(v[3]), dst: store.Int(v[4]), accountID: accountID,
			payload: store.Str(v[5]), dep: store.Int(v[6]), attempts: int(store.Int(v[7]))}
		problem, err := p.pushOp(ctx, o)
		if o.kind == "purge" && err == nil && problem == "" {
			if r, qerr := db.Query(ctx, "SELECT 1 FROM ops WHERE id = ? AND state = 'done'", o.id); qerr == nil && len(r.Values) > 0 {
				purged[o.dst]++
			}
		}
		if problem != "" {
			problems = append(problems, problem)
			if o.kind == "move" {
				changed = append(changed, o.msg)
			}
		}
		if err != nil {
			if len(problems) > 0 {
				err = errors.Join(err, &OpsError{problems, changed})
			}
			return err
		}
	}
	if len(problems) > 0 {
		return &OpsError{problems, changed}
	}
	return nil
}

// pushOp applies o. problem is set when o became conflict or failed; err when the
// connection or database is unusable.
func (p *pusher) pushOp(ctx context.Context, o op) (problem string, err error) {
	db := p.db
	if o.dep != 0 {
		r, err := db.Query(ctx, "SELECT state FROM ops WHERE id = ?", o.dep)
		if err != nil {
			return "", err
		}
		if len(r.Values) > 0 && (store.Str(r.Values[0][0]) == "pending" || store.Str(r.Values[0][0]) == "awaiting_claim") {
			return "", nil
		}
		if _, err := db.Execute(ctx, "UPDATE ops SET depends_on = NULL WHERE id = ?", o.id); err != nil {
			return "", err
		}
	}
	if o.kind == "move" && p.stable {
		return p.pushStableMove(ctx, o)
	}
	// A flags change goes wherever the server has the message; a move or purge
	// takes it from the folder it was queued from.
	at, args := "", []any{o.msg}
	local := o.kind == "purge" && p.inboxOnly // from a local folder: the server has it in the inbox only
	if o.kind != "flags" && !local {
		at, args = " AND l.folder_id = ?", append(args, o.src)
	}
	r, err := db.Query(ctx, `SELECT l.folder_id, f.server_ref, l.server_ref, f.name FROM message_locations l
		JOIN folders f ON f.id = l.folder_id WHERE l.message_id = ? AND l.server_ref IS NOT NULL`+at+" LIMIT 1", args...)
	if err != nil {
		return "", err
	}
	if len(r.Values) == 0 {
		if p.inboxOnly { // no longer on the server: kept here (a purged one the fetch deletes)
			if o.kind == "purge" { // nothing to send, but the purge is checked as one that is
				if due, err := p.purgeStillDue(ctx, o); err != nil || !due {
					if err != nil {
						return "", err
					}
					return "", finish(ctx, db, o, "cancelled", "")
				}
			}
			return "", finish(ctx, db, o, "done", "")
		}
		if p.stable {
			msg := "the message no longer has a server location, so the change could not be applied"
			return msg, finish(ctx, db, o, "conflict", msg)
		}
		return "", nil // no server location: the move before it awaits its claim
	}
	src, srcName := store.Int(r.Values[0][0]), store.Str(r.Values[0][3])
	loc := backend.Location{Folder: backend.FolderRef(store.Str(r.Values[0][1])), Ref: backend.MessageRef(store.Str(r.Values[0][2]))}
	if o.kind == "move" && o.dst == src {
		return "", finish(ctx, db, o, "done", "")
	}
	if o.kind == "purge" {
		return p.pushPurge(ctx, o, loc)
	}
	// Counted before anything is sent: after a dropped connection the next round
	// knows the command may have reached the server.
	if _, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ?", o.id); err != nil {
		return "", err
	}
	o.attempts++

	var w backend.Op
	var dst destination
	switch o.kind {
	case "flags":
		var f FlagsPayload
		if err := json.Unmarshal([]byte(o.payload), &f); o.payload != "" && err != nil {
			msg := "unreadable flags change: " + err.Error()
			return msg, finish(ctx, db, o, "failed", msg)
		}
		w = backend.SetFlags{At: loc, Add: flagsOf(f.Add), Remove: flagsOf(f.Remove)}
	case "move":
		if dst, err = p.destination(ctx, o.dst); err != nil || dst.ref == "" {
			if err != nil {
				return "", err
			}
			return fail(ctx, db, o, errNoDestination, false)
		}
		var mp struct {
			Before *backend.MoveBaseline `json:"before,omitempty"`
		}
		if o.payload != "" {
			_ = json.Unmarshal([]byte(o.payload), &mp)
		}
		w = backend.Move{
			At: loc, To: dst.ref, Cursor: dst.cursor, Before: mp.Before,
			BeforeCopy: func(b backend.MoveBaseline) error {
				raw, err := json.Marshal(b)
				if err != nil {
					return err
				}
				_, err = db.Execute(ctx, "UPDATE ops SET payload = json_set(coalesce(nullif(payload, ''), '{}'), '$.before', json(?)) WHERE id = ?", string(raw), o.id)
				return err
			},
		}
	default:
		msg := fmt.Sprintf("unknown change %q", o.kind)
		return msg, finish(ctx, db, o, "failed", msg)
	}
	res, err := p.clearPurge(ctx, o, loc)
	if err == nil && res.Outcome == backend.Done {
		res, err = p.recv.Apply(ctx, w)
	}
	switch {
	case errors.Is(err, backend.ErrNotFound) && !errors.Is(err, backend.ErrFolderReset) && o.kind == "flags" && p.stable:
		// Another client took the message out of that folder: try its next location.
		if _, err := db.Transaction(ctx, unlocate(o.msg, src)); err != nil {
			return "", err
		}
		return p.pushOp(ctx, o)
	case errors.Is(err, backend.ErrNotFound):
		msg := fmt.Sprintf("folder %q was recreated on the server, so a change to a message in it was not applied", srcName)
		return msg, finish(ctx, db, o, "conflict", msg)
	case err != nil:
		return fail(ctx, db, o, err, true)
	}
	switch res.Outcome {
	case backend.Unsupported:
		msg := unsupported(res.Reason)
		return msg, finish(ctx, db, o, "failed", msg)
	case backend.Failed:
		return fail(ctx, db, o, errors.New(res.Reason), false)
	case backend.Unlocated:
		return "", moved(ctx, db, o, "")
	}
	if o.kind == "flags" {
		return "", finish(ctx, db, o, "done", "")
	}
	return "", moved(ctx, db, o, string(res.Ref))
}

// clearPurge removes the \Deleted that a purge of the message, when one
// reached the server, may have left at at: a rescued message must not carry
// it. Without such a purge, or for a flags change, it does nothing (Done).
func (p *pusher) clearPurge(ctx context.Context, o op, at backend.Location) (backend.Result, error) {
	if o.kind != "move" {
		return backend.Result{Outcome: backend.Done}, nil
	}
	r, err := p.db.Query(ctx, "SELECT 1 FROM ops WHERE message_id = ? AND kind = 'purge' AND attempts > 0", o.msg)
	if err != nil || len(r.Values) == 0 {
		return backend.Result{Outcome: backend.Done}, err
	}
	// The backends report \Deleted among the keywords, as it is spelled.
	return p.recv.Apply(ctx, backend.SetFlags{At: at, Remove: backend.Flags{Keywords: []string{`\Deleted`}}})
}

// destination is a move's target folder: "" ref when it no longer exists.
type destination struct {
	ref    backend.FolderRef
	cursor backend.Cursor
}

func (p *pusher) destination(ctx context.Context, id int64) (destination, error) {
	d, err := p.db.Query(ctx, "SELECT server_ref, cursor FROM folders WHERE id = ? AND server_ref IS NOT NULL", id)
	if err != nil || len(d.Values) == 0 {
		return destination{}, err
	}
	return destination{backend.FolderRef(store.Str(d.Values[0][0])), backend.Cursor(store.Str(d.Values[0][1]))}, nil
}

// unsupported is the last_error of an op the server cannot do: the feature's
// reason described in English, as the user reads it, never the reason's key.
func unsupported(reason string) string {
	switch reason {
	case "imap.no_move":
		return "the server supports neither MOVE nor UIDPLUS, so messages cannot be moved safely"
	case "imap.no_uidplus":
		return "the server does not support UIDPLUS, so mail cannot be deleted permanently without risking other mail"
	}
	return "the server does not support this change: " + i18n.TIn("en", "feature.reason."+reason)
}

// flagsOf is local storage's flag names written neutrally.
func flagsOf(names []string) backend.Flags {
	var f backend.Flags
	for _, n := range names {
		switch n {
		case `\Seen`:
			f.Seen = true
		case `\Flagged`:
			f.Flagged = true
		case `\Answered`:
			f.Answered = true
		case `\Draft`:
			f.Draft = true
		default:
			f.Keywords = append(f.Keywords, n)
		}
	}
	return f
}

// lostSource ends a move on an account of stable identity whose source
// folder no longer has the message, or whose move gave no usable location,
// from what Godwit knows of the server rather than by sending it again. Such
// a server keeps a message in a folder at one location, so a known location
// in the destination means the move is done. Else, when a move of it may
// have reached the server (sent), it awaits its claim there by ServerID;
// when none can have, another client took it out of the source first, and
// the move is a conflict. A message the server has nowhere else waits for
// the claim of the move before it.
func (p *pusher) lostSource(ctx context.Context, o op, sent bool) (string, error) {
	r, err := p.db.Query(ctx, `SELECT count(*), coalesce(sum(folder_id = ?), 0) FROM message_locations
		WHERE message_id = ? AND folder_id != ? AND server_ref IS NOT NULL`, o.dst, o.msg, o.src)
	if err != nil {
		return "", err
	}
	switch {
	case store.Int(r.Values[0][1]) > 0:
		_, err := p.db.Transaction(ctx, append(unlocate(o.msg, o.src),
			sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}}))
		return "", err
	case sent:
		return "", moved(ctx, p.db, o, "")
	}
	msg := "the message left the folder it was moved from on the server before the move was sent"
	return msg, finish(ctx, p.db, o, "conflict", msg)
}

// moved records a move the server accepted: with its new location ref the
// message's server location in the source folder becomes the destination;
// without one it is cleared until fetch claims the arrival (ServerID, else
// Message-ID and size) in the destination. Its other folders keep their
// server locations. Another row already at ref is the same server message
// (Gmail's All Mail copy of mail archived there, synced before X-GM-MSGID
// was, #506): it is merged into the moved one (ADR 0034), its queued ops with it.
func moved(ctx context.Context, db *store.DB, o op, ref string) error {
	stmts := append(unlocate(o.msg, o.src),
		sqlite.Statement{SQL: "UPDATE ops SET state = 'awaiting_claim', last_error = NULL WHERE id = ?", Args: []any{o.id}})
	if ref != "" {
		ids, _, err := doomed(ctx, db, "EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id AND folder_id = ? AND server_ref = ?) AND id != ?", o.dst, ref, o.msg)
		if err != nil {
			return err
		}
		stmts = append(unlocate(o.msg, o.src), sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}})
		if len(ids) > 0 { // at most one: UNIQUE(folder_id, server_ref)
			m, tids, err := merge(ctx, db, o.msg, ids)
			if err != nil {
				return err
			}
			stmts = append(append(stmts, m...), tidyThreads(o.accountID, tids)...)
		}
		// Trash/Spam removes every other Gmail membership before a queued flag runs.
		stmts = append(stmts, sqlite.Statement{SQL: `UPDATE message_locations SET server_ref = NULL
			WHERE message_id = ? AND folder_id != ? AND EXISTS (SELECT 1 FROM folders WHERE id = ? AND role IN ('trash', 'junk'))`, Args: []any{o.msg, o.dst, o.dst}}, dropUnseen(o.msg))
		stmts = append(stmts, locate(o.msg, o.dst, ref)...)
		if o.src == 0 {
			stmts = append(stmts, settle(o.msg)...)
		}
	}
	_, err := db.Transaction(ctx, stmts)
	return err
}

// finish sets o's state. A move that ends failed or conflict puts the message
// back in the folders the server has it in (settle); a move queued after it
// from its destination then moves it from its source.
func finish(ctx context.Context, db *store.DB, o op, state, lastError string) error {
	where := "id = ?"
	if o.kind == "purge" {
		where += " AND state = 'pending'" // a rescue's cancel stands
	}
	stmts := []sqlite.Statement{{SQL: "UPDATE ops SET state = ?, last_error = ? WHERE " + where, Args: []any{state, nullable(lastError), o.id}}}
	if o.kind == "move" && (state == "failed" || state == "conflict") {
		stmts = append(append(stmts,
			sqlite.Statement{SQL: `UPDATE ops SET src_folder_id = ? WHERE message_id = ? AND kind IN ('move', 'purge') AND state = 'pending'
				AND src_folder_id = ? AND id > ?`, Args: []any{o.src, o.msg, o.dst, o.id}}),
			settle(o.msg)...)
	}
	_, err := db.Transaction(ctx, stmts)
	return err
}

// fail records a failed attempt; the op is failed once it used MaxAttempts. A
// server refusal is the op's problem; anything else (stop) also stops the push.
func fail(ctx context.Context, db *store.DB, o op, cause error, stop bool) (string, error) {
	state, problem := "pending", ""
	if o.attempts >= MaxAttempts {
		state, problem = "failed", fmt.Sprintf("a change was given up after %d attempts: %v", o.attempts, cause)
	}
	if err := finish(ctx, db, o, state, cause.Error()); err != nil {
		return problem, errors.Join(cause, err)
	}
	if stop {
		return problem, cause
	}
	return problem, nil
}

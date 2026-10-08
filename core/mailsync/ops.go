package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/store"
)

// MaxAttempts is how many times an op is sent before it is marked failed.
const MaxAttempts = 5

// FlagsPayload is the payload of a flags op: flags to add, then flags to remove.
type FlagsPayload struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// OpsError lists ops that PushOps marked conflict or failed; the push went on.
type OpsError struct{ Problems []string }

func (e *OpsError) Error() string { return strings.Join(e.Problems, "; ") }

var errNoDestination = errors.New("the destination folder no longer exists")

type op struct {
	id, msg, dst, dep int64
	kind, payload     string
	attempts          int
}

// PushOps sends the account's pending ops to the server in id order. Ops whose
// message has no server location yet, or whose dependency is still pending or
// awaiting its claim, wait for a later round (a conflict or failed dependency no
// longer holds them back). Problems with single ops come back as *OpsError; any other error
// means the connection is unusable and the push stopped.
func PushOps(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64) error {
	var problems []string
	res, err := db.Execute(ctx, `UPDATE ops SET state = 'conflict', last_error = 'the message no longer exists'
		WHERE account_id = ? AND message_id IS NULL AND state IN ('pending', 'awaiting_claim')`, accountID)
	if err != nil {
		return err
	}
	if res.Changes > 0 {
		problems = append(problems, fmt.Sprintf("%d change(s) not applied: the message no longer exists", res.Changes))
	}
	rows, err := db.Query(ctx, `SELECT id, kind, message_id, dst_folder_id, payload, depends_on, attempts FROM ops
		WHERE account_id = ? AND state = 'pending' ORDER BY id`, accountID)
	if err != nil {
		return err
	}
	for _, v := range rows.Values {
		o := op{id: store.Int(v[0]), kind: store.Str(v[1]), msg: store.Int(v[2]), dst: store.Int(v[3]),
			payload: store.Str(v[4]), dep: store.Int(v[5]), attempts: int(store.Int(v[6]))}
		problem, err := pushOp(ctx, db, c, o)
		if problem != "" {
			problems = append(problems, problem)
		}
		if err != nil {
			if len(problems) > 0 {
				err = errors.Join(err, &OpsError{problems})
			}
			return err
		}
	}
	if len(problems) > 0 {
		return &OpsError{problems}
	}
	return nil
}

// pushOp applies o. problem is set when o became conflict or failed; err when the
// connection or database is unusable.
func pushOp(ctx context.Context, db *store.DB, c *imapclient.Client, o op) (problem string, err error) {
	if o.dep != 0 {
		r, err := db.Query(ctx, "SELECT state FROM ops WHERE id = ?", o.dep)
		if err != nil {
			return "", err
		}
		if len(r.Values) > 0 && (store.Str(r.Values[0][0]) == "pending" || store.Str(r.Values[0][0]) == "awaiting_claim") {
			return "", nil
		}
	}
	r, err := db.Query(ctx, `SELECT m.server_folder_id, m.server_uidvalidity, m.server_uid, f.name FROM messages m
		JOIN folders f ON f.id = m.server_folder_id WHERE m.id = ?`, o.msg)
	if err != nil || len(r.Values) == 0 {
		return "", err // no server location: the move before it awaits its claim
	}
	src, srcValidity, uid, srcName := store.Int(r.Values[0][0]), uint32(store.Int(r.Values[0][1])),
		imap.UID(store.Int(r.Values[0][2])), store.Str(r.Values[0][3])
	if o.kind == "move" && o.dst == src {
		return "", finish(ctx, db, o.id, "done", "")
	}
	// Counted before anything is sent: after a dropped connection the next round
	// knows the command may have reached the server.
	if _, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ?", o.id); err != nil {
		return "", err
	}
	sent := o.attempts > 0
	o.attempts++

	var dstName string
	var dstValidity uint32
	switch o.kind {
	case "flags":
	case "move":
		if !c.Caps().Has(imap.CapMove) && !c.Caps().Has(imap.CapUIDPlus) {
			msg := "the server supports neither MOVE nor UIDPLUS, so messages cannot be moved safely"
			return msg, finish(ctx, db, o.id, "failed", msg)
		}
		d, err := db.Query(ctx, "SELECT name, uidvalidity FROM folders WHERE id = ?", o.dst)
		if err != nil {
			return "", err
		}
		if len(d.Values) == 0 {
			return fail(ctx, db, o, errNoDestination)
		}
		dstName, dstValidity = store.Str(d.Values[0][0]), uint32(store.Int(d.Values[0][1]))
	default:
		msg := fmt.Sprintf("unknown change %q", o.kind)
		return msg, finish(ctx, db, o.id, "failed", msg)
	}

	sel, err := c.Select(srcName, nil).Wait()
	if err != nil {
		return fail(ctx, db, o, err)
	}
	if sel.UIDValidity != srcValidity {
		msg := fmt.Sprintf("folder %q was recreated on the server, so a change to a message in it was not applied", srcName)
		return msg, finish(ctx, db, o.id, "conflict", msg)
	}
	set := imap.UIDSetNum(uid)
	if o.kind == "flags" {
		var p FlagsPayload
		if err := json.Unmarshal([]byte(o.payload), &p); err != nil {
			msg := "unreadable flags change: " + err.Error()
			return msg, finish(ctx, db, o.id, "failed", msg)
		}
		if err := storeFlags(c, set, p); err != nil {
			return fail(ctx, db, o, err)
		}
		return "", finish(ctx, db, o.id, "done", "")
	}
	if sent {
		// The last attempt may have moved it before the connection dropped.
		got, err := c.Fetch(set, &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return fail(ctx, db, o, err)
		}
		if len(got) == 0 {
			return "", moved(ctx, db, o, 0, 0)
		}
	}
	validity, dstUID, err := move(c, set, dstName)
	if err != nil {
		return fail(ctx, db, o, err)
	}
	if validity != dstValidity { // a COPYUID from a folder generation we have not synced is no use
		dstUID = 0
	}
	return "", moved(ctx, db, o, validity, dstUID)
}

func storeFlags(c *imapclient.Client, set imap.UIDSet, p FlagsPayload) error {
	for _, s := range []struct {
		op    imap.StoreFlagsOp
		flags []string
	}{{imap.StoreFlagsAdd, p.Add}, {imap.StoreFlagsDel, p.Remove}} {
		if len(s.flags) == 0 {
			continue
		}
		fl := make([]imap.Flag, len(s.flags))
		for i, f := range s.flags {
			fl[i] = imap.Flag(f)
		}
		if err := c.Store(set, &imap.StoreFlags{Op: s.op, Silent: true, Flags: fl}, nil).Close(); err != nil {
			return err
		}
	}
	return nil
}

// move moves the message in the selected folder to dst and returns the COPYUID
// destination, or zeros when the server gave none. Without MOVE it runs COPY,
// STORE \Deleted and UID EXPUNGE one after another, each waiting for the previous
// OK; not Client.Move's fallback, which pipelines them and sends a plain EXPUNGE
// (removing every \Deleted message in the folder) when UIDPLUS is missing.
func move(c *imapclient.Client, set imap.UIDSet, dst string) (validity, uid uint32, err error) {
	if c.Caps().Has(imap.CapMove) {
		d, err := c.Move(set, dst).Wait()
		if err != nil {
			return 0, 0, err
		}
		return d.UIDValidity, single(d.DestUIDs), nil
	}
	d, err := c.Copy(set, dst).Wait()
	if err != nil {
		return 0, 0, err
	}
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return 0, 0, err
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		return 0, 0, err
	}
	return d.UIDValidity, single(d.DestUIDs), nil
}

// single returns the only UID of s, or 0.
func single(s imap.NumSet) uint32 {
	u, _ := s.(imap.UIDSet)
	if n, ok := u.Nums(); ok && len(n) == 1 {
		return uint32(n[0])
	}
	return 0
}

// moved records a move the server accepted: with a COPYUID the message's server
// location becomes the destination; without one it is cleared until fetch claims
// the arrival (Message-ID and size) in the destination.
func moved(ctx context.Context, db *store.DB, o op, validity, uid uint32) error {
	stmts := []sqlite.Statement{
		{SQL: "UPDATE messages SET server_folder_id = NULL, server_uidvalidity = NULL, server_uid = NULL WHERE id = ?", Args: []any{o.msg}},
		{SQL: "UPDATE ops SET state = 'awaiting_claim', last_error = NULL WHERE id = ?", Args: []any{o.id}},
	}
	if uid != 0 {
		stmts = []sqlite.Statement{
			{SQL: "UPDATE messages SET server_folder_id = ?, server_uidvalidity = ?, server_uid = ? WHERE id = ?", Args: []any{o.dst, validity, uid, o.msg}},
			{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}},
		}
	}
	_, err := db.Transaction(ctx, stmts)
	return err
}

func finish(ctx context.Context, db *store.DB, id int64, state, lastError string) error {
	_, err := db.Execute(ctx, "UPDATE ops SET state = ?, last_error = ? WHERE id = ?", state, nullable(lastError), id)
	return err
}

// fail records a failed attempt; the op is failed once it used MaxAttempts. A
// server refusal is the op's problem; anything else also stops the push.
func fail(ctx context.Context, db *store.DB, o op, cause error) (string, error) {
	state, problem := "pending", ""
	if o.attempts >= MaxAttempts {
		state, problem = "failed", fmt.Sprintf("a change was given up after %d attempts: %v", o.attempts, cause)
	}
	if err := finish(ctx, db, o.id, state, cause.Error()); err != nil {
		return problem, errors.Join(cause, err)
	}
	var refused *imap.Error
	if errors.As(cause, &refused) || cause == errNoDestination {
		return problem, nil
	}
	return problem, cause
}

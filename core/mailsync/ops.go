package mailsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/store"
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

// PushOps sends the account's pending ops to the server in id order. Ops whose
// message has no server location yet, or whose dependency is still pending or
// awaiting its claim, wait for a later round, as do purges whose undo window is
// still open at now; once the dependency failed or conflicted the op is re-based
// (depends_on cleared) and runs from the message's current server location.
// Problems with single ops come back as *OpsError; any other error means the
// connection is unusable and the push stopped.
func PushOps(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, now time.Time) error {
	return push(ctx, db, c, accountID, now, "")
}

// PushNewOps is PushOps limited to ops never attempted, for a second round after
// a fetch claimed moves: ops that were waiting run, ones that failed are not retried.
func PushNewOps(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, now time.Time) error {
	return push(ctx, db, c, accountID, now, " AND attempts = 0")
}

func push(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, now time.Time, filter string) (rerr error) {
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
		problem, err := pushOp(ctx, db, c, o)
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
func pushOp(ctx context.Context, db *store.DB, c *imapclient.Client, o op) (problem string, err error) {
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
	if o.kind == "move" && c.Caps().Has(capGmail) && (!c.Caps().Has(imap.CapMove) || o.dst == 0 || o.src == 0) {
		return pushGmailLabels(ctx, db, c, o)
	}
	// A flags change goes wherever the server has the message; a move or purge
	// takes it from the folder it was queued from.
	at, args := "", []any{o.msg}
	if o.kind != "flags" {
		at, args = " AND l.folder_id = ?", append(args, o.src)
	}
	r, err := db.Query(ctx, `SELECT l.folder_id, l.uidvalidity, l.uid, f.name FROM message_locations l
		JOIN folders f ON f.id = l.folder_id WHERE l.message_id = ? AND l.uid IS NOT NULL`+at+" LIMIT 1", args...)
	if err != nil {
		return "", err
	}
	gmail := c.Caps().Has(capGmail)
	if len(r.Values) == 0 {
		if o.kind == "move" && gmail {
			return lostSource(ctx, db, o, o.attempts > 0)
		}
		if gmail {
			msg := "the message no longer has a server location, so the change could not be applied"
			return msg, finish(ctx, db, o, "conflict", msg)
		}
		return "", nil // no server location: the move before it awaits its claim
	}
	src, srcValidity, uid, srcName := store.Int(r.Values[0][0]), uint32(store.Int(r.Values[0][1])),
		imap.UID(store.Int(r.Values[0][2])), store.Str(r.Values[0][3])
	if o.kind == "move" && o.dst == src {
		return "", finish(ctx, db, o, "done", "")
	}
	if o.kind == "purge" {
		return pushPurge(ctx, db, c, o, srcName, srcValidity, uid)
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
			return msg, finish(ctx, db, o, "failed", msg)
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
		return msg, finish(ctx, db, o, "failed", msg)
	}

	sel, err := c.Select(srcName, nil).Wait()
	if err != nil {
		return fail(ctx, db, o, err)
	}
	if sel.UIDValidity != srcValidity {
		msg := fmt.Sprintf("folder %q was recreated on the server, so a change to a message in it was not applied", srcName)
		return msg, finish(ctx, db, o, "conflict", msg)
	}
	set := imap.UIDSetNum(uid)
	if o.kind == "flags" {
		var p FlagsPayload
		if err := json.Unmarshal([]byte(o.payload), &p); err != nil {
			msg := "unreadable flags change: " + err.Error()
			return msg, finish(ctx, db, o, "failed", msg)
		}
		if err := storeFlags(c, set, p); err != nil {
			return fail(ctx, db, o, err)
		}
		if gmail { // a silent STORE to a label another client removed matches nothing
			got, err := c.Fetch(set, &imap.FetchOptions{UID: true}).Collect()
			if err != nil {
				return fail(ctx, db, o, err)
			}
			if len(got) == 0 {
				if _, err := db.Transaction(ctx, unlocate(o.msg, src)); err != nil {
					return "", err
				}
				return pushOp(ctx, db, c, o) // try the next known location
			}
		}
		return "", finish(ctx, db, o, "done", "")
	}
	// A purge of it that reached the server may have left \Deleted.
	pr, err := db.Query(ctx, "SELECT 1 FROM ops WHERE message_id = ? AND kind = 'purge' AND attempts > 0", o.msg)
	if err != nil {
		return "", err
	}
	purged := len(pr.Values) > 0
	if sent {
		// The last attempt may have moved it before the connection dropped.
		got, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
		if err != nil {
			return fail(ctx, db, o, err)
		}
		if len(got) == 0 {
			if gmail {
				return lostSource(ctx, db, o, true)
			}
			return "", moved(ctx, db, o, 0, 0)
		}
		// Without MOVE, the last attempt may have got through COPY and STORE but
		// not EXPUNGE: copying again would leave two in the destination. \Deleted
		// alone is no proof (another client may have set it), so the copy must
		// be in the destination too; fetch claims it there.
		if !c.Caps().Has(imap.CapMove) && !purged && slices.Contains(got[0].Flags, imap.FlagDeleted) {
			copied, err := copyIn(ctx, db, c, o.msg, dstName)
			if err == nil {
				err = reselect(c, srcName, srcValidity)
			}
			if err != nil {
				return fail(ctx, db, o, err)
			}
			if copied {
				if err := c.UIDExpunge(set).Close(); err != nil {
					return fail(ctx, db, o, err)
				}
				return "", moved(ctx, db, o, 0, 0)
			}
		}
	}
	if purged { // the rescued copy must not carry \Deleted
		if err := undelete(c, uid); err != nil {
			return fail(ctx, db, o, err)
		}
	}
	validity, dstUID, err := move(c, set, dstName)
	if err != nil {
		return fail(ctx, db, o, err)
	}
	if validity != dstValidity { // a COPYUID from a folder generation we have not synced is no use
		dstUID = 0
	}
	if dstUID == 0 && gmail {
		return lostSource(ctx, db, o, true)
	}
	return "", moved(ctx, db, o, validity, dstUID)
}

// lostSource ends a Gmail move whose source folder no longer has the message,
// or whose MOVE got no usable COPYUID, from what Godwit knows of the server
// rather than by sending it again. Gmail keeps a message in a folder under
// one UID, so a known location in the destination means the move is done.
// Else, when a MOVE of it may have reached the server (sent), it awaits its
// claim there by X-GM-MSGID; when none can have, another client took it out
// of the source first, and the move is a conflict. A message the server has
// nowhere else waits for the claim of the move before it.
func lostSource(ctx context.Context, db *store.DB, o op, sent bool) (string, error) {
	r, err := db.Query(ctx, `SELECT count(*), coalesce(sum(folder_id = ?), 0) FROM message_locations
		WHERE message_id = ? AND folder_id != ? AND uid IS NOT NULL`, o.dst, o.msg, o.src)
	if err != nil {
		return "", err
	}
	switch {
	case store.Int(r.Values[0][1]) > 0:
		_, err := db.Transaction(ctx, append(unlocate(o.msg, o.src),
			sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}}))
		return "", err
	case sent:
		return "", moved(ctx, db, o, 0, 0)
	}
	msg := "the message left the folder it was moved from on the server before the move was sent"
	return msg, finish(ctx, db, o, "conflict", msg)
}

// copyIn reports whether folder holds a message with msg's Message-ID and
// size, as a claim matches it. A message without a Message-ID is never found.
func copyIn(ctx context.Context, db *store.DB, c *imapclient.Client, msg int64, folder string) (bool, error) {
	r, err := db.Query(ctx, "SELECT message_id, size FROM messages WHERE id = ?", msg)
	if err != nil || len(r.Values) == 0 || store.Str(r.Values[0][0]) == "" || store.Int(r.Values[0][1]) <= 0 {
		return false, err
	}
	return InFolder(c, folder, store.Str(r.Values[0][0]), store.Int(r.Values[0][1]))
}

// InFolder reports whether folder, examined, holds a message whose Message-ID
// is exactly messageID (without its angle brackets) and, when size > 0, of that
// size. SEARCH HEADER matches substrings and ignores case, so each hit is checked.
func InFolder(c *imapclient.Client, folder, messageID string, size int64) (bool, error) {
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return false, err
	}
	crit := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}}
	if size > 0 {
		crit.Larger, crit.Smaller = size-1, size+1
	}
	res, err := c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return false, err
	}
	uids := res.AllUIDs()
	if len(uids) == 0 {
		return false, nil
	}
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		h, err := netmail.ReadMessage(bytes.NewReader(append(m.FindBodySection(sec), "\r\n\r\n"...)))
		if err != nil {
			continue
		}
		if strings.Trim(strings.TrimSpace(h.Header.Get("Message-Id")), "<>") == messageID {
			return true, nil
		}
	}
	return false, nil
}

// reselect selects folder again, read-write, as long as it is the generation validity.
func reselect(c *imapclient.Client, folder string, validity uint32) error {
	sel, err := c.Select(folder, nil).Wait()
	if err == nil && sel.UIDValidity != validity {
		err = fmt.Errorf("folder %q was recreated on the server", folder)
	}
	return err
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
// location in the source folder becomes the destination; without one it is
// cleared until fetch claims the arrival (X-GM-MSGID, else Message-ID and size)
// in the destination. Its other folders keep their server locations. Another
// row already at the COPYUID is the same server message (Gmail's All Mail copy
// of mail archived there, synced before X-GM-MSGID was, #506): it is merged
// into the moved one (ADR 0034), its queued ops with it.
func moved(ctx context.Context, db *store.DB, o op, validity, uid uint32) error {
	stmts := append(unlocate(o.msg, o.src),
		sqlite.Statement{SQL: "UPDATE ops SET state = 'awaiting_claim', last_error = NULL WHERE id = ?", Args: []any{o.id}})
	if uid != 0 {
		ids, _, err := doomed(ctx, db, "EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id AND folder_id = ? AND uid = ?) AND id != ?", o.dst, uid, o.msg)
		if err != nil {
			return err
		}
		stmts = append(unlocate(o.msg, o.src), sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}})
		if len(ids) > 0 { // at most one: UNIQUE(folder_id, uid)
			m, tids, err := merge(ctx, db, o.msg, ids)
			if err != nil {
				return err
			}
			stmts = append(append(stmts, m...), tidyThreads(o.accountID, tids)...)
		}
		// Trash/Spam removes every other Gmail membership before a queued flag runs.
		stmts = append(stmts, sqlite.Statement{SQL: `UPDATE message_locations SET uidvalidity = NULL, uid = NULL
			WHERE message_id = ? AND folder_id != ? AND EXISTS (SELECT 1 FROM folders WHERE id = ? AND role IN ('trash', 'junk'))`, Args: []any{o.msg, o.dst, o.dst}}, dropUnseen(o.msg))
		stmts = append(stmts, locate(o.msg, o.dst, validity, uid)...)
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
// server refusal is the op's problem; anything else also stops the push.
func fail(ctx context.Context, db *store.DB, o op, cause error) (string, error) {
	state, problem := "pending", ""
	if o.attempts >= MaxAttempts {
		state, problem = "failed", fmt.Sprintf("a change was given up after %d attempts: %v", o.attempts, cause)
	}
	if err := finish(ctx, db, o, state, cause.Error()); err != nil {
		return problem, errors.Join(cause, err)
	}
	var refused *imap.Error
	if errors.As(cause, &refused) || cause == errNoDestination {
		return problem, nil
	}
	return problem, cause
}

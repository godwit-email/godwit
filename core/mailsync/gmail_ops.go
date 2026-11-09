package mailsync

import (
	"context"
	"errors"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
)

// pushStableMove applies a move on an account of stable identity (Gmail),
// where a message may be in several folders: from no folder it adds one
// (AddToFolder, from any location the server still has), to no folder it
// takes one away (RemoveFromFolder), else it moves. The backend reads where
// the message is by its ServerID on every attempt, so a lost reply is
// uncertain rather than a failure: it stays pending past the retry budget
// until an attempt resolves it. Only a refusal counts against MaxAttempts.
func (p *pusher) pushStableMove(ctx context.Context, o op) (string, error) {
	db := p.db
	r, err := db.Query(ctx, `SELECT m.server_id, f.server_ref, f.role, l.server_ref FROM messages m
		LEFT JOIN folders f ON f.id = ? LEFT JOIN message_locations l ON l.message_id = m.id AND l.folder_id = f.id WHERE m.id = ?`, o.src, o.msg)
	if err != nil {
		return "", err
	}
	if len(r.Values) == 0 {
		return "the message no longer exists", finish(ctx, db, o, "conflict", "the message no longer exists")
	}
	v := r.Values[0]
	at := backend.Location{Folder: backend.FolderRef(store.Str(v[1])), Ref: backend.MessageRef(store.Str(v[3])), ServerID: store.Str(v[0])}
	completed := o
	if store.Str(v[2]) == "all" {
		completed.src = 0 // All Mail is implicit membership, never a removable label.
	}
	var to backend.FolderRef
	if o.dst != 0 {
		dst, err := p.destination(ctx, o.dst)
		if err != nil {
			return "", err
		}
		if dst.ref == "" {
			return p.refused(ctx, o, errNoDestination)
		}
		to = dst.ref
	}
	if o.src == o.dst {
		return "", finish(ctx, db, o, "done", "")
	}
	if o.src == 0 {
		return p.addToFolder(ctx, o, at.ServerID, to)
	}
	if at.ServerID == "" && at.Ref == "" {
		return p.lostSource(ctx, o, o.attempts > 0)
	}
	var w backend.Op = backend.Move{At: at, To: to}
	if o.dst == 0 {
		w = backend.RemoveFromFolder{At: at}
	}
	res, err := p.clearPurge(ctx, o, at)
	if errors.Is(err, backend.ErrNotFound) || (err == nil && res.Outcome == backend.Done) {
		res, err = p.recv.Apply(ctx, w) // a source already gone is the move's to find
	}
	if errors.Is(err, backend.ErrNotFound) {
		return p.lostSource(ctx, o, o.attempts > 0)
	}
	return p.stableResult(ctx, o, completed, res, err)
}

// addToFolder adds folder to to the message, from the first of its known
// locations where the server still has it.
func (p *pusher) addToFolder(ctx context.Context, o op, serverID string, to backend.FolderRef) (string, error) {
	anchors, err := p.db.Query(ctx, `SELECT f.server_ref, l.server_ref FROM message_locations l JOIN folders f ON f.id = l.folder_id
		WHERE l.message_id = ? AND l.server_ref IS NOT NULL AND f.server_ref IS NOT NULL ORDER BY l.folder_id`, o.msg)
	if err != nil {
		return "", err
	}
	for _, a := range anchors.Values {
		at := backend.Location{Folder: backend.FolderRef(store.Str(a[0])), Ref: backend.MessageRef(store.Str(a[1])), ServerID: serverID}
		res, err := p.recv.Apply(ctx, backend.AddToFolder{At: at, To: to})
		if !errors.Is(err, backend.ErrNotFound) {
			return p.stableResult(ctx, o, o, res, err)
		}
	}
	msg := "Gmail message has no addressable location in visible IMAP folders; expose All Mail or restore a label to apply this change"
	_, err = p.db.Execute(ctx, "UPDATE ops SET last_error = ? WHERE id = ?", msg, o.id)
	return msg, err // report the blocked intent, but let fetch discover a new location
}

// stableResult records how a stable-identity move ended. completed is o with
// the source it takes the message out of locally.
func (p *pusher) stableResult(ctx context.Context, o, completed op, res backend.Result, err error) (string, error) {
	if err != nil { // a lost reply: uncertain, not counted
		_, derr := p.db.Execute(ctx, "UPDATE ops SET last_error = ? WHERE id = ?", err.Error(), o.id)
		return "", errors.Join(err, derr)
	}
	switch res.Outcome {
	case backend.Unsupported:
		msg := unsupported(res.Reason)
		return msg, finish(ctx, p.db, o, "failed", msg)
	case backend.Failed:
		return p.refused(ctx, o, errors.New(res.Reason))
	case backend.Unlocated:
		return p.lostSource(ctx, o, true)
	}
	if o.dst != 0 && res.Ref != "" {
		return "", moved(ctx, p.db, completed, string(res.Ref))
	}
	// All Mail can be hidden: removing INBOX is complete without a target location.
	_, err = p.db.Transaction(ctx, append(unlocate(o.msg, completed.src), sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}}))
	return "", err
}

// refused counts a refused attempt and fails the op once it used MaxAttempts.
func (p *pusher) refused(ctx context.Context, o op, cause error) (string, error) {
	if _, err := p.db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ?", o.id); err != nil {
		return "", err
	}
	o.attempts++
	return fail(ctx, p.db, o, cause, false)
}

package mailsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
)

// pushStableMove applies a move on an account of stable identity (Gmail),
// where a message may be in several folders: from no folder it adds one
// (AddToFolder, from any location the server still has), to no folder it
// takes one away (RemoveFromFolder), else it moves. The backend reads where
// the message is by its ServerID on every attempt, so a lost reply is
// uncertain rather than a failure: it stays pending past the retry budget
// until an attempt resolves it. Every attempt that may have written counts
// (a lost reply too), so a later one knows the move may have reached the
// server; only a refusal fails the op at MaxAttempts.
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
	var dst destination
	if o.dst != 0 {
		var err error
		dst, err = p.destination(ctx, o.dst)
		if err != nil {
			return "", err
		}
		if dst.ref == "" {
			return p.refused(ctx, o, errNoDestination)
		}
	}
	if o.src == o.dst {
		return "", finish(ctx, db, o, "done", "")
	}
	if o.src == 0 {
		return p.addToFolder(ctx, o, at.ServerID, dst.ref)
	}
	sent := o.attempts > 0
	if at.ServerID == "" && at.Ref == "" {
		return p.lostSource(ctx, o, sent)
	}
	var w backend.Op = backend.Move{At: at, To: dst.ref, Cursor: dst.cursor}
	if o.dst == 0 {
		w = backend.RemoveFromFolder{At: at}
	}
	if err := count(ctx, p.db, &o, 1); err != nil {
		return "", err
	}
	completed.attempts = o.attempts
	res, err := p.clearPurge(ctx, o, at)
	if errors.Is(err, backend.ErrNotFound) || (err == nil && res.Outcome == backend.Done) {
		res, err = p.recv.Apply(ctx, w) // a source already gone is the move's to find
	}
	if nothingDone(res, err) {
		if err := count(ctx, p.db, &o, -1); err != nil {
			return "", err
		}
	}
	if errors.Is(err, backend.ErrNotFound) {
		switch {
		case errors.Is(err, backend.ErrFolderReset):
			msg := fmt.Sprintf("folder %q was recreated on the server, so a change to a message in it was not applied", at.Folder)
			return msg, finish(ctx, db, o, "conflict", msg)
		case at.ServerID != "" && o.dst != 0 && store.Str(v[2]) != "all":
			// The backend found it by identity in neither folder.
			msg := "the message left the folder it was moved from and is absent from the destination"
			return msg, finish(ctx, db, o, "conflict", msg)
		}
		return p.lostSource(ctx, o, sent)
	}
	return p.stableResult(ctx, o, completed, res, err)
}

// nothingDone reports an Apply that wrote nothing, so its attempt does not count.
func nothingDone(res backend.Result, err error) bool {
	var refused *backend.FolderError
	return errors.Is(err, backend.ErrNotFound) || errors.As(err, &refused) || (err == nil && res.Outcome == backend.Unsupported)
}

// addToFolder adds folder to to the message, from the first of its known
// locations where the server still has it.
func (p *pusher) addToFolder(ctx context.Context, o op, serverID string, to backend.FolderRef) (string, error) {
	anchors, err := p.db.Query(ctx, `SELECT f.server_ref, l.server_ref FROM message_locations l JOIN folders f ON f.id = l.folder_id
		WHERE l.message_id = ? AND l.server_ref IS NOT NULL AND f.server_ref IS NOT NULL ORDER BY l.folder_id`, o.msg)
	if err != nil {
		return "", err
	}
	if err := count(ctx, p.db, &o, 1); err != nil {
		return "", err
	}
	for _, a := range anchors.Values {
		at := backend.Location{Folder: backend.FolderRef(store.Str(a[0])), Ref: backend.MessageRef(store.Str(a[1])), ServerID: serverID}
		res, err := p.recv.Apply(ctx, backend.AddToFolder{At: at, To: to})
		if !errors.Is(err, backend.ErrNotFound) {
			if nothingDone(res, err) {
				if err := count(ctx, p.db, &o, -1); err != nil {
					return "", err
				}
			}
			return p.stableResult(ctx, o, o, res, err)
		}
	}
	if err := count(ctx, p.db, &o, -1); err != nil { // nothing was done
		return "", err
	}
	msg := "Gmail message has no addressable location in visible IMAP folders; expose All Mail or restore a label to apply this change"
	return msg, noteError(ctx, p.db, o.id, msg) // report the blocked intent, but let fetch discover a new location
}

// stableResult records how a stable-identity move ended, its attempt already
// counted. completed is o with the source it takes the message out of locally.
func (p *pusher) stableResult(ctx context.Context, o, completed op, res backend.Result, err error) (string, error) {
	if err != nil { // uncertain: tried again, failed only by a refusal
		derr := noteError(ctx, p.db, o.id, err.Error())
		var refused *backend.FolderError
		if errors.As(err, &refused) {
			return "", derr // a refused read: the push goes on
		}
		return "", errors.Join(err, derr)
	}
	switch res.Outcome {
	case backend.Unsupported:
		msg := unsupported(res.Reason)
		return msg, finish(ctx, p.db, o, "failed", msg)
	case backend.Failed:
		return fail(ctx, p.db, o, errors.New(res.Reason), false)
	case backend.Unlocated:
		return p.lostSource(ctx, o, true)
	}
	if o.dst != 0 && res.Ref != "" {
		return "", moved(ctx, p.db, completed, string(res.Ref))
	}
	// All Mail can be hidden: removing INBOX is complete without a target location.
	_, err = p.db.Transaction(ctx, append(unlocate(o.msg, completed.src), opDoneStmt(o.id)))
	return "", err
}

// refused counts an attempt refused before anything was sent and fails the
// op once it used MaxAttempts.
func (p *pusher) refused(ctx context.Context, o op, cause error) (string, error) {
	if err := count(ctx, p.db, &o, 1); err != nil {
		return "", err
	}
	return fail(ctx, p.db, o, cause, false)
}

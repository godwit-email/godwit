package ews

import (
	"context"
	"errors"
	"fmt"

	"github.com/godwit-email/godwit/backend"
)

// Write-back: Apply's ops but SetFlags, and folder create, rename and delete.

// oneResponse is an operation's response with one response message, of
// whatever name, carrying the ids of the items or folders it made.
type oneResponse struct {
	Messages struct {
		Message []idsMessage `xml:",any"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

type idsMessage struct {
	responseMessage
	Items struct {
		Item []struct {
			ID id `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		} `xml:",any"` // Message, ...
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Folders struct {
		Folder []struct {
			ID id `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		} `xml:",any"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
}

// one sends op, a request about one item or folder, and returns the id of
// the item or folder its answer names, "" for none. The server's refusal is
// a *ResponseError.
func one(ctx context.Context, c *client, op any) (string, error) {
	resp, err := call[oneResponse](ctx, c, op)
	if err != nil {
		return "", err
	}
	ms := resp.Messages.Message
	if len(ms) != 1 {
		return "", fmt.Errorf("ews: %T: %d responses to one request", op, len(ms))
	}
	if err := ms[0].err(); err != nil {
		return "", err
	}
	switch m := ms[0]; {
	case len(m.Items.Item) > 0:
		return m.Items.Item[0].ID.ID, nil
	case len(m.Folders.Folder) > 0:
		return m.Folders.Folder[0].ID.ID, nil
	}
	return "", nil
}

// written is a write-back's result from the item's new ItemId, "" when the
// server did not say, and the error writing it: ErrNotFound when the item
// at is gone, Failed when the server refused it.
func written(at backend.Location, newID string, err error) (backend.Result, error) {
	var re *ResponseError
	switch {
	case isCode(err, "ErrorItemNotFound"):
		return backend.Result{}, fmt.Errorf("ews: item %s: %w", at.Ref, backend.ErrNotFound)
	case errors.As(err, &re):
		return backend.Result{Outcome: backend.Failed, Reason: re.Code + ": " + re.Text}, nil
	case err != nil:
		return backend.Result{}, err
	case newID == "":
		return backend.Result{Outcome: backend.Unlocated}, nil
	}
	return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(newID)}, nil
}

// remove deletes the item at the way how names: a deletion is Done with no ref.
func (r *Receiver) remove(ctx context.Context, at backend.Location, how string) (backend.Result, error) {
	res, err := written(at, "-", r.c.deleteItem(ctx, string(at.Ref), how))
	if res.Outcome == backend.Done {
		res.Ref = ""
	}
	return res, err
}

// append saves op.Raw into op.To with its flags, unless op.To holds a
// message of that Message-ID and size already.
func (r *Receiver) append(ctx context.Context, op backend.Append) (backend.Result, error) {
	if op.MessageID != "" {
		have, err := r.Find(ctx, op.To, op.MessageID, int64(len(op.Raw)))
		var re *ResponseError
		if errors.As(err, &re) {
			return backend.Result{}, &backend.FolderError{Folder: op.To, Err: err}
		}
		if err != nil {
			return backend.Result{}, err
		}
		if len(have) > 0 {
			return backend.Result{Outcome: backend.Done, Ref: have[0]}, nil
		}
	}
	id, err := r.c.createItem(ctx, string(op.To), op.Raw, op.Flags, op.Date)
	return written(backend.Location{Folder: op.To}, id, err)
}

// CreateFolder creates a mail folder named name under parent, "" for the
// top of the mailbox, and returns its ref.
func (r *Receiver) CreateFolder(ctx context.Context, parent backend.FolderRef, name string) (backend.FolderRef, error) {
	p := distinguished("msgfolderroot")
	if parent != "" {
		p = folderRef(string(parent))
	}
	id, err := r.c.createFolder(ctx, p, name)
	if err == nil && id == "" {
		err = errors.New("ews: CreateFolder answered without the FolderId")
	}
	return backend.FolderRef(id), err
}

// RenameFolder gives the folder ref the name name; its ref stays.
func (r *Receiver) RenameFolder(ctx context.Context, ref backend.FolderRef, name string) error {
	return r.c.renameFolder(ctx, string(ref), name)
}

// DeleteFolder deletes the folder ref with its mail and subfolders, as
// Outlook does: into Deleted Items, where they can still be recovered.
func (r *Receiver) DeleteFolder(ctx context.Context, ref backend.FolderRef) error {
	return r.c.deleteFolder(ctx, string(ref), "MoveToDeletedItems")
}

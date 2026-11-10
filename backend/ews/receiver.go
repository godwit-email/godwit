package ews

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// Receiver is an EWS account's receiving backend. A folder's FolderRef is
// its FolderId, a message's MessageRef its ItemId (no ChangeKey), and a
// folder's cursor its SyncFolderItems SyncState.
type Receiver struct{ c *client }

var _ backend.Receiver = (*Receiver)(nil)

// New is a Receiver for the account cfg names. It connects on first use.
func New(cfg Config) *Receiver { return &Receiver{newClient(cfg)} }

// Close implements backend.Receiver.
func (r *Receiver) Close() error {
	r.c.http.CloseIdleConnections()
	return nil
}

// ReasonServerTooOld is the add-account check's refusal of a server older
// than Exchange 2016.
const ReasonServerTooOld = "ews.server_too_old"

// ErrServerTooOld is Check on a server older than Exchange 2016. Its message
// is ReasonServerTooOld.
var ErrServerTooOld = errors.New(ReasonServerTooOld)

// reasonPending: the backend does not do this yet.
const reasonPending = "ews.pending"

// shortcut: write-back, push and the flags are still to come, so every
// feature but the ones only stated here is Unsupported for now; the
// write-back and push tickets fill in the spec's table.
var description = func() backend.Description {
	d := backend.Description{Features: map[backend.Feature]backend.Support{}, Identity: backend.IdentityLocation, PollInterval: time.Minute}
	for _, f := range backend.AllFeatures {
		d.Features[f] = backend.Support{Level: backend.LevelUnsupported, Reason: reasonPending}
	}
	return d
}()

// Describe implements backend.Receiver. Its first call signs in, so refused
// credentials are ErrAuthFailed here.
func (r *Receiver) Describe(ctx context.Context) (backend.Description, error) {
	if r.c.serverVersion() == nil {
		if _, err := r.c.getFolders(ctx, []string{"msgfolderroot"}); err != nil {
			return backend.Description{}, err
		}
	}
	d := description
	d.Features = maps.Clone(d.Features)
	return d, nil
}

// Check implements backend.Receiver: signing in, Exchange 2016 or later, and
// listing the folders.
func (r *Receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	fs, err := r.folders(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	if v := r.c.serverVersion(); v == nil || !v.atLeast2016() {
		return backend.CheckResult{}, ErrServerTooOld
	}
	d, err := r.Describe(ctx)
	return backend.CheckResult{Host: r.c.host(), Folders: len(fs), Description: d}, err
}

// roles are the distinguished folders a role is read from.
var roles = []struct {
	id   string
	role backend.Role
}{
	{"inbox", backend.RoleInbox}, {"sentitems", backend.RoleSent}, {"drafts", backend.RoleDrafts},
	{"deleteditems", backend.RoleTrash}, {"junkemail", backend.RoleJunk}, {"archive", backend.RoleArchive},
}

// folders is the mailbox's mail folders, each named by its path from the
// root and given its role by the distinguished folder it is.
// shortcut: lists every folder each round (no hierarchy SyncState kept);
// keep the SyncFolderHierarchy SyncState as the account cursor when a
// mailbox's folders prove too many to list each round.
func (r *Receiver) folders(ctx context.Context) ([]backend.Folder, error) {
	names := []string{"msgfolderroot"}
	for _, ro := range roles {
		names = append(names, ro.id)
	}
	ids, err := r.c.getFolders(ctx, names)
	if err != nil {
		return nil, err
	}
	root, role := ids[0], map[string]backend.Role{}
	for i, ro := range roles {
		if ids[i+1] != "" {
			role[ids[i+1]] = ro.role
		}
	}
	changes, _, err := r.c.syncFolderHierarchy(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := map[string]*folder{}
	var order []*folder
	for _, ch := range changes {
		if f := ch.Folder; f != nil && (f.Class == "" || f.Class == "IPF.Note" || strings.HasPrefix(f.Class, "IPF.Note.")) {
			byID[f.ID.ID] = f
			order = append(order, f)
		}
	}
	var out []backend.Folder
	for _, f := range order {
		name, parent := f.DisplayName, f.Parent.ID
		for p, n := byID[parent], 0; p != nil && n < len(order); p, n = byID[p.Parent.ID], n+1 {
			name = p.DisplayName + "/" + name
		}
		if parent == root || byID[parent] == nil { // a mail folder under a folder that is not one sits at the top
			parent = ""
		}
		out = append(out, backend.Folder{Ref: backend.FolderRef(f.ID.ID), Name: name, Delim: "/",
			Parent: backend.FolderRef(parent), Role: role[f.ID.ID]})
	}
	return out, nil
}

// getBatch is how many items one GetItem fetches, as Microsoft advises for
// requests with MIME content.
const getBatch = 10

// Sync implements backend.Receiver: the folder list, then each folder's
// SyncFolderItems pages from its cursor. A folder's first sync reports its
// mail as backfill; mail created after it as new arrivals.
// shortcut: reports only created items, all of them, whole: no Window, no
// Older, no Update, ReadFlagChange or Delete yet, and no FolderReset on
// ErrorInvalidSyncStateData; the incremental sync and sync range tickets
// add them.
func (r *Receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	if req.Older > 0 {
		return false, fmt.Errorf("ews: older mail: %w", backend.ErrUnsupported)
	}
	fs, err := r.folders(ctx)
	if err != nil {
		return false, err
	}
	var changes []backend.Change
	seen := map[backend.FolderRef]bool{}
	for _, f := range fs {
		changes, seen[f.Ref] = append(changes, backend.FolderUpdated{Folder: f}), true
	}
	for ref := range req.Folders {
		if !seen[ref] {
			changes = append(changes, backend.FolderGone{Ref: ref})
		}
	}
	if err := commit(backend.Page{Changes: changes}); err != nil {
		return false, err
	}
	var errs []error
	for _, f := range fs {
		if req.Only != nil && !slices.Contains(req.Only, f.Ref) {
			continue
		}
		err := r.syncFolder(ctx, req, f.Ref, commit)
		var re *ResponseError
		if errors.As(err, &re) {
			errs = append(errs, &backend.FolderError{Folder: f.Ref, Err: err})
			continue
		}
		if err != nil {
			return false, err
		}
	}
	return false, errors.Join(errs...)
}

// syncFolder takes folder's pages from its cursor to the last, committing each.
func (r *Receiver) syncFolder(ctx context.Context, req backend.SyncRequest, folder backend.FolderRef, commit func(backend.Page) error) error {
	st := req.Folders[folder]
	for {
		m, err := r.c.syncFolderItems(ctx, string(folder), string(st.Cursor))
		if err != nil {
			return err
		}
		var created []string
		for _, ch := range m.Changes.Change {
			if ch.XMLName.Local == "Create" {
				created = append(created, ch.ref())
			}
		}
		var changes []backend.Change
		for len(created) > 0 {
			n := min(getBatch, len(created))
			items, err := r.c.getItems(ctx, created[:n])
			if err != nil {
				return err
			}
			created = created[n:]
			for _, it := range items {
				if isCode(it.err, "ErrorItemNotFound") {
					continue // gone since: a later page or round says so
				}
				if it.err != nil {
					return it.err
				}
				changes = append(changes, backend.MessageAppeared{
					Location: backend.Location{Folder: folder, Ref: backend.MessageRef(it.ID.ID)},
					Flags:    backend.Flags{Seen: it.IsRead},
					Backfill: !st.Synced,
					Content: &backend.Content{Size: int64(len(it.raw)), Date: it.Received,
						Header: backend.HeaderFields(it.raw, req.HeaderFields), Raw: it.raw},
				})
			}
		}
		st = backend.FolderState{Cursor: backend.Cursor(m.SyncState), Synced: st.Synced || m.Last, Filled: st.Filled}
		if err := commit(backend.Page{Changes: changes, Folders: map[backend.FolderRef]backend.FolderState{folder: st}}); err != nil {
			return err
		}
		if m.Last {
			return nil
		}
	}
}

var errNotYet = fmt.Errorf("ews: %w yet", backend.ErrUnsupported)

// Fetch implements backend.Receiver.
// shortcut: not yet; content comes whole with the sync (Content.Raw).
func (r *Receiver) Fetch(context.Context, backend.ContentRequest) (io.ReadCloser, error) {
	return nil, errNotYet
}

// Headers implements backend.Receiver.
// shortcut: not yet.
func (r *Receiver) Headers(context.Context, backend.FolderRef, []backend.MessageRef, []string) (map[backend.MessageRef][]byte, error) {
	return nil, errNotYet
}

// Find implements backend.Receiver.
// shortcut: not yet.
func (r *Receiver) Find(context.Context, backend.FolderRef, string, int64) ([]backend.MessageRef, error) {
	return nil, errNotYet
}

// Apply implements backend.Receiver: nothing is written back yet.
func (r *Receiver) Apply(context.Context, backend.Op) (backend.Result, error) {
	return backend.Result{Outcome: backend.Unsupported, Reason: reasonPending}, nil
}

// Watch implements backend.Receiver: no push yet, so core polls at
// PollInterval.
func (r *Receiver) Watch(context.Context, func([]backend.FolderRef)) error {
	return backend.ErrUnsupported
}

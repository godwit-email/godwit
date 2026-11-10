package ews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
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

// Reason keys, translated by the UI and Wit.
const (
	reasonDraft      = "ews.draft_read_only"  // Exchange sets IsDraft itself; it cannot be changed
	reasonOneFolder  = "ews.one_folder"       // a message is in one folder only
	reasonNoKeywords = "ews.no_keywords"      // custom keywords are not kept on Exchange
	reasonNoLabels   = "ews.folder_not_label" // a folder is not a label: deleting it deletes its mail
	reasonNoAllMail  = "ews.no_all_mail"      // archiving moves to the Archive folder
	reasonFolders    = "ews.folders_pending"  // core manages no server folders yet, for any backend
)

// description is the spec's table: one Description for every EWS account,
// since Exchange 2016 and later all do the same.
var description = func() backend.Description {
	d := backend.Description{Features: map[backend.Feature]backend.Support{}, Identity: backend.IdentityLocation, PollInterval: time.Minute}
	for _, f := range backend.AllFeatures {
		d.Features[f] = backend.Support{Level: backend.LevelSupported}
	}
	set := func(l backend.Level, reason string, fs ...backend.Feature) {
		for _, f := range fs {
			d.Features[f] = backend.Support{Level: l, Reason: reason}
		}
	}
	set(backend.LevelNoted, reasonDraft, backend.FeatureDraft)
	set(backend.LevelUnsupported, reasonOneFolder, backend.FeatureAddToFolder, backend.FeatureRemoveFromFolder)
	set(backend.LevelUnsupported, reasonNoKeywords, backend.FeatureKeywords)
	set(backend.LevelUnsupported, reasonNoLabels, backend.FeatureLabelDelete)
	set(backend.LevelUnsupported, reasonNoAllMail, backend.FeatureAllMail)
	set(backend.LevelUnsupported, reasonFolders, backend.FeatureFolderCreate, backend.FeatureFolderRename, backend.FeatureFolderDelete)
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
// listing the folders. A refused sign-in the server takes over Basic is
// ErrExtendedProtection.
func (r *Receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	fs, _, err := r.folders(ctx)
	if errors.Is(err, backend.ErrAuthFailed) && r.c.takesBasic(ctx) {
		return backend.CheckResult{}, ErrExtendedProtection
	}
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
// root and given its role by the distinguished folder it is, and the
// SyncFolderHierarchy SyncState they are at.
func (r *Receiver) folders(ctx context.Context) ([]backend.Folder, string, error) {
	names := []string{"msgfolderroot"}
	for _, ro := range roles {
		names = append(names, ro.id)
	}
	ids, err := r.c.getFolders(ctx, names)
	if err != nil {
		return nil, "", err
	}
	root, role := ids[0], map[string]backend.Role{}
	for i, ro := range roles {
		if ids[i+1] != "" {
			role[ids[i+1]] = ro.role
		}
	}
	changes, state, err := r.c.syncFolderHierarchy(ctx, "")
	if err != nil {
		return nil, "", err
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
	return out, state, nil
}

// hierarchy is the folder list and the account cursor after it. From a
// cursor, a hierarchy without changes since is nil folders; any change
// lists every folder again, since a folder's name is its path and the
// server reports a rename for the renamed folder alone. A cursor the server
// voided lists them too.
func (r *Receiver) hierarchy(ctx context.Context, account backend.Cursor) ([]backend.Folder, backend.Cursor, error) {
	if account != "" {
		changes, state, err := r.c.syncFolderHierarchy(ctx, string(account))
		if err == nil && len(changes) == 0 {
			return nil, backend.Cursor(state), nil
		}
		if err != nil && !isCode(err, "ErrorInvalidSyncStateData") {
			return nil, "", err
		}
	}
	fs, state, err := r.folders(ctx)
	return fs, backend.Cursor(state), err
}

// getBatch is how many items one GetItem fetches, as Microsoft advises for
// requests with MIME content.
const getBatch = 10

// Sync implements backend.Receiver: the folders, when they changed since the
// account cursor, then each folder's SyncFolderItems pages from its cursor.
// A folder's first sync reports its mail in the window as backfill, mail
// created after it as new arrivals if received in the window (mail moved in
// keeps its older time); an Update or ReadFlagChange is the item's flags, a
// Delete its location gone. A folder whose SyncState the server voided is
// reset and synced from nothing, local storage claiming its mail again.
// Older takes what the window left out instead.
// shortcut: a window grown after the first sync brings in nothing more,
// only 加载更早 does; scan the folder with FindItem down to the new cutoff
// (tracked in Filled) if users miss it.
// shortcut: the sync downloads whole messages; take their structure from
// Attachments and fetch parts with GetAttachment if large mail slows it.
func (r *Receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	if req.Older > 0 {
		return r.older(ctx, req, commit)
	}
	fs, account, err := r.hierarchy(ctx, req.Account)
	if err != nil {
		return false, err
	}
	page := backend.Page{Account: account}
	refs := slices.Sorted(maps.Keys(req.Folders))
	if fs != nil {
		refs = nil
		for _, f := range fs {
			page.Changes, refs = append(page.Changes, backend.FolderUpdated{Folder: f}), append(refs, f.Ref)
		}
		for ref := range req.Folders {
			if !slices.Contains(refs, ref) {
				page.Changes = append(page.Changes, backend.FolderGone{Ref: ref})
			}
		}
	}
	if err := commit(page); err != nil {
		return false, err
	}
	var errs []error
	for _, ref := range refs {
		if req.Only != nil && !slices.Contains(req.Only, ref) {
			continue
		}
		err := r.syncFolder(ctx, req, ref, commit)
		var re *ResponseError
		if errors.As(err, &re) {
			errs = append(errs, &backend.FolderError{Folder: ref, Err: err})
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
	at := func(ref string) backend.Location {
		return backend.Location{Folder: folder, Ref: backend.MessageRef(ref)}
	}
	var cut *time.Time // the window's, found at the first Create
	inWindow := func(received time.Time) (bool, error) {
		if cut == nil {
			c, err := r.cutoff(ctx, folder, req.Window)
			if err != nil {
				return false, err
			}
			cut = &c
		}
		return received.IsZero() || !received.Before(*cut), nil
	}
	for {
		m, err := r.c.syncFolderItems(ctx, string(folder), string(st.Cursor))
		if isCode(err, "ErrorInvalidSyncStateData") && st.Cursor != "" {
			st = backend.FolderState{}
			if err := commit(backend.Page{Changes: []backend.Change{backend.FolderReset{Ref: folder}},
				Folders: map[backend.FolderRef]backend.FolderState{folder: st}}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		var created, updated []string
		var changes []backend.Change
		for _, ch := range m.Changes.Change {
			switch ch.XMLName.Local {
			case "Create":
				in, err := inWindow(ch.Item.Received)
				if err != nil {
					return err
				}
				if in {
					created = append(created, ch.ref())
				}
			case "Update", "ReadFlagChange": // the latter's IsRead alone would lose the other flags
				updated = append(updated, ch.ref())
			case "Delete":
				changes = append(changes, backend.MessageGone{Location: at(ch.ref())})
			}
		}
		if created, err = r.unheld(ctx, req.Local, folder, created); err != nil {
			return err
		}
		err = r.each(ctx, created, true, func(it fetched) {
			changes = append(changes, appeared(folder, it, !st.Synced, req.HeaderFields))
		})
		if err != nil {
			return err
		}
		err = r.each(ctx, updated, false, func(it fetched) {
			changes = append(changes, backend.FlagsChanged{Location: at(it.ID.ID), Flags: it.flags()})
		})
		if err != nil {
			return err
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

// unheld is the ids of folder that local storage does not hold yet: one it
// holds came from Godwit's own move, copy or append, Done with its ItemId.
func (r *Receiver) unheld(ctx context.Context, local backend.Local, folder backend.FolderRef, ids []string) ([]string, error) {
	if local == nil || len(ids) == 0 {
		return ids, nil
	}
	refs := make([]backend.MessageRef, len(ids))
	for i, id := range ids {
		refs[i] = backend.MessageRef(id)
	}
	held, err := local.HeldRefs(ctx, folder, refs)
	return slices.DeleteFunc(ids, func(id string) bool { return held[backend.MessageRef(id)] }), err
}

// appeared is it, fetched with its MIME content, appearing in folder.
func appeared(folder backend.FolderRef, it fetched, backfill bool, fields []string) backend.MessageAppeared {
	return backend.MessageAppeared{Location: backend.Location{Folder: folder, Ref: backend.MessageRef(it.ID.ID)}, Flags: it.flags(), Backfill: backfill,
		Content: &backend.Content{Size: int64(len(it.raw)), Date: it.Received, Header: backend.HeaderFields(it.raw, fields), Raw: it.raw}}
}

// findBatch is how many items a FindItem page of Older asks for.
const findBatch = 512

// older takes up to req.Older of each Only folder's messages local storage
// lacks, newest received first, from FindItem pages from the newest down;
// more reports that a folder holds still more.
func (r *Receiver) older(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (more bool, err error) {
	for _, ref := range req.Only {
		if !req.Folders[ref].Synced {
			return false, backend.ErrNotSynced
		}
	}
	for _, ref := range req.Only {
		var pick []string
		for offset, last := 0, false; !last && len(pick) <= req.Older; {
			var page []found
			if page, last, err = r.c.findPage(ctx, string(ref), offset, findBatch); err != nil {
				return false, err
			}
			offset += len(page)
			refs := make([]backend.MessageRef, len(page))
			for i, it := range page {
				refs[i] = backend.MessageRef(it.ID.ID)
			}
			held := map[backend.MessageRef]bool{}
			if req.Local != nil {
				if held, err = req.Local.HeldRefs(ctx, ref, refs); err != nil {
					return false, err
				}
			}
			for _, it := range refs {
				if !held[it] {
					pick = append(pick, string(it))
				}
			}
		}
		if len(pick) > req.Older {
			pick, more = pick[:req.Older], true
		}
		var changes []backend.Change
		if err := r.each(ctx, pick, true, func(it fetched) {
			changes = append(changes, appeared(ref, it, true, req.HeaderFields))
		}); err != nil {
			return false, err
		}
		if err := commit(backend.Page{Changes: changes, Folders: map[backend.FolderRef]backend.FolderState{ref: req.Folders[ref]}}); err != nil {
			return false, err
		}
	}
	return more, nil
}

// cutoff is when the oldest mail w keeps in folder was received: the
// newest Newest's oldest, else Since; zero for all of it.
func (r *Receiver) cutoff(ctx context.Context, folder backend.FolderRef, w backend.Window) (time.Time, error) {
	if w.Newest <= 0 {
		return w.Since, nil
	}
	nth, _, err := r.c.findPage(ctx, string(folder), w.Newest-1, 1)
	if err != nil || len(nth) == 0 {
		return time.Time{}, err
	}
	return nth[0].Received, nil
}

// each fetches the items ids, in batches, with their MIME content if mime,
// and calls do with each one still there: one gone since is skipped, as a
// later page or round says it is gone.
func (r *Receiver) each(ctx context.Context, ids []string, mime bool, do func(fetched)) error {
	batch := getBatch
	if !mime {
		batch = maxChanges
	}
	for len(ids) > 0 {
		n := min(batch, len(ids))
		items, err := r.c.getItems(ctx, ids[:n], mime)
		if err != nil {
			return err
		}
		ids = ids[n:]
		for _, it := range items {
			if isCode(it.err, "ErrorItemNotFound") {
				continue
			}
			if it.err != nil {
				return it.err
			}
			do(it)
		}
	}
	return nil
}

// Fetch implements backend.Receiver. The sync gives whole messages, which
// local storage parses, so a part is named by its IMAP part path ("1.2")
// and cut from the whole message, still transfer-encoded as IMAP gives it.
func (r *Receiver) Fetch(ctx context.Context, req backend.ContentRequest) (io.ReadCloser, error) {
	items, err := r.c.getItems(ctx, []string{string(req.Ref)}, true)
	if err != nil {
		return nil, err
	}
	if isCode(items[0].err, "ErrorItemNotFound") {
		return nil, fmt.Errorf("ews: item %s: %w", req.Ref, backend.ErrNotFound)
	}
	if items[0].err != nil {
		return nil, items[0].err
	}
	raw := items[0].raw
	switch {
	case req.Part != "":
		var path []int
		for s := range strings.SplitSeq(string(req.Part), ".") {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("ews: part %q: %w", req.Part, backend.ErrNotFound)
			}
			path = append(path, n)
		}
		if raw = imapserver.ExtractBodySection(bytes.NewReader(raw), &imap.FetchItemBodySection{Part: path}); raw == nil {
			return nil, fmt.Errorf("ews: part %q: %w", req.Part, backend.ErrNotFound)
		}
	case req.HeaderFields != nil:
		raw = backend.HeaderFields(raw, req.HeaderFields)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

// Headers implements backend.Receiver: GetItem with the MIME content, the
// fields cut from it.
func (r *Receiver) Headers(ctx context.Context, _ backend.FolderRef, refs []backend.MessageRef, fields []string) (map[backend.MessageRef][]byte, error) {
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = string(ref)
	}
	out := map[backend.MessageRef][]byte{}
	err := r.each(ctx, ids, true, func(it fetched) {
		out[backend.MessageRef(it.ID.ID)] = backend.HeaderFields(it.raw, fields)
	})
	return out, err
}

// Find implements backend.Receiver: FindItem on InternetMessageId; a size
// is the MIME message's, as the sync stores it, so it is compared on the
// MIME content of the items found.
func (r *Receiver) Find(ctx context.Context, folder backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	ids, err := r.c.findByMessageID(ctx, string(folder), "<"+messageID+">")
	if err != nil {
		return nil, err
	}
	var out []backend.MessageRef
	if size <= 0 {
		for _, id := range ids {
			out = append(out, backend.MessageRef(id))
		}
		return out, nil
	}
	err = r.each(ctx, ids, true, func(it fetched) {
		if int64(len(it.raw)) == size {
			out = append(out, backend.MessageRef(it.ID.ID))
		}
	})
	return out, err
}

// Apply implements backend.Receiver. SetFlags updates the item's flags;
// Move and Copy ask for the new ItemId (a move changes it), Done with it or
// Unlocated without; Delete moves to Deleted Items; Purge deletes for good
// once Confirm agrees; Append saves the MIME message with its flags.
func (r *Receiver) Apply(ctx context.Context, op backend.Op) (backend.Result, error) {
	switch op := op.(type) {
	case backend.SetFlags:
		var err error
		if us := flagUpdates(op.Add, op.Remove); len(us) > 0 {
			err = r.c.updateItem(ctx, string(op.At.Ref), us)
		}
		res, err := written(op.At, "-", err)
		res.Ref = ""
		return res, err
	case backend.AddToFolder, backend.RemoveFromFolder:
		return backend.Result{Outcome: backend.Unsupported, Reason: reasonOneFolder}, nil
	case backend.Move:
		if op.To == op.At.Folder {
			return backend.Result{Outcome: backend.Done, Ref: op.At.Ref}, nil
		}
		id, err := r.c.moveItem(ctx, string(op.At.Ref), string(op.To), false)
		return written(op.At, id, err)
	case backend.Copy:
		id, err := r.c.moveItem(ctx, string(op.At.Ref), string(op.To), true)
		return written(op.At, id, err)
	case backend.Delete:
		return r.remove(ctx, op.At, "MoveToDeletedItems")
	case backend.Purge:
		if op.Confirm != nil && !op.Confirm() {
			return backend.Result{Outcome: backend.Failed, Reason: backend.Cancelled}, nil
		}
		return r.remove(ctx, op.At, "HardDelete")
	case backend.Append:
		return r.append(ctx, op)
	}
	return backend.Result{}, fmt.Errorf("ews: unknown op %T", op)
}

// Watch implements backend.Receiver over a streaming subscription to all
// folders, kept for the account across Watch calls: each notification wakes
// for the folders its events name. A connection that reached its 30 minutes
// is opened again on the same subscription; a subscription the server lost
// is made again, with a wake for what it missed. A failed connection ends
// Watch, and core polls.
func (r *Receiver) Watch(ctx context.Context, wake func(folders []backend.FolderRef)) error {
	cr, err := r.c.cfg.Credentials(ctx)
	if err != nil {
		return err
	}
	a := r.c.affinity(r.c.account(cr))
	a.mu.Lock()
	sub := a.subscription
	a.mu.Unlock()
	for {
		if sub == "" {
			if sub, err = r.c.subscribe(ctx); err != nil {
				return err
			}
			a.mu.Lock()
			a.subscription = sub
			a.mu.Unlock()
		}
		wake(nil) // for what changed before watching, or while the subscription was lost
		for err == nil {
			err = r.c.stream(ctx, sub, wake)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !isCode(err, "ErrorSubscriptionNotFound") && !isCode(err, "ErrorInvalidSubscription") {
			return err
		}
		a.lost(sub)
		sub, err = "", nil
	}
}

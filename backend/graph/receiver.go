package graph

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// Receiver is a Microsoft Graph account's receiving backend. A folder's
// FolderRef is its mailFolder id, a message's ServerID and MessageRef its
// ImmutableId, and a folder's cursor its messages/delta deltaLink.
type Receiver struct{ c *client }

var _ backend.Receiver = (*Receiver)(nil)

// NewReceiver is a Receiver for the account cfg names. It connects on first use.
func NewReceiver(cfg Config) *Receiver { return &Receiver{newClient(cfg)} }

// Close implements backend.Receiver.
func (r *Receiver) Close() error {
	r.c.http.CloseIdleConnections()
	return nil
}

// Reason keys, translated by the UI and Wit.
const (
	reasonDraft      = "graph.draft_read_only"  // Graph sets isDraft itself; it cannot be patched
	reasonAnswered   = "graph.no_answered"      // Graph messages carry no answered flag
	reasonCategories = "graph.categories"       // custom keywords map to Outlook categories
	reasonOneFolder  = "graph.one_folder"       // a message is in one folder only
	reasonNoLabels   = "graph.folder_not_label" // a folder is not a label: deleting it deletes its mail
	reasonNoAllMail  = "graph.no_all_mail"      // archiving moves to the Archive folder
	reasonFolders    = "graph.folders_pending"  // core manages no server folders yet
	reasonNoPush     = "graph.no_push"          // Graph webhooks cannot reach a desktop client
)

var description = func() backend.Description {
	d := backend.Description{Features: map[backend.Feature]backend.Support{}, Identity: backend.IdentityStable, PollInterval: time.Minute}
	for _, f := range backend.AllFeatures {
		d.Features[f] = backend.Support{Level: backend.LevelSupported}
	}
	set := func(l backend.Level, reason string, fs ...backend.Feature) {
		for _, f := range fs {
			d.Features[f] = backend.Support{Level: l, Reason: reason}
		}
	}
	set(backend.LevelNoted, reasonDraft, backend.FeatureDraft)
	set(backend.LevelNoted, reasonCategories, backend.FeatureKeywords)
	set(backend.LevelUnsupported, reasonAnswered, backend.FeatureAnswered)
	set(backend.LevelUnsupported, reasonOneFolder, backend.FeatureAddToFolder, backend.FeatureRemoveFromFolder)
	set(backend.LevelUnsupported, reasonNoLabels, backend.FeatureLabelDelete)
	set(backend.LevelUnsupported, reasonNoAllMail, backend.FeatureAllMail)
	set(backend.LevelUnsupported, reasonFolders, backend.FeatureFolderCreate, backend.FeatureFolderRename, backend.FeatureFolderDelete)
	set(backend.LevelUnsupported, reasonNoPush, backend.FeaturePush)
	return d
}()

// Describe implements backend.Receiver. Its first call signs in, so refused
// credentials are ErrAuthFailed here.
func (r *Receiver) Describe(ctx context.Context) (backend.Description, error) {
	r.c.mu.Lock()
	ok := r.c.signedIn
	r.c.mu.Unlock()
	if !ok {
		if _, err := callJSON[struct{ ID string }](ctx, r.c, http.MethodGet, "/me/mailFolders/inbox?$select=id", nil); err != nil {
			return backend.Description{}, err
		}
	}
	d := description
	d.Features = maps.Clone(d.Features)
	return d, nil
}

// Check implements backend.Receiver: verifies /me/mailFolders/inbox with the
// Mail.ReadWrite token and lists the mailbox's folders.
func (r *Receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	if _, err := callJSON[struct{ ID string }](ctx, r.c, http.MethodGet, "/me/mailFolders/inbox?$select=id", nil); err != nil {
		return backend.CheckResult{}, err
	}
	fs, _, err := r.folders(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	d, err := r.Describe(ctx)
	return backend.CheckResult{Host: r.c.host(), Folders: len(fs), Description: d}, err
}

// Watch implements backend.Receiver: desktop clients cannot receive Graph
// webhooks, so core polls every PollInterval (1 minute).
func (r *Receiver) Watch(context.Context, func([]backend.FolderRef)) error {
	return backend.ErrUnsupported
}

var wellKnownRoles = []struct {
	name string
	role backend.Role
}{
	{"inbox", backend.RoleInbox},
	{"sentitems", backend.RoleSent},
	{"drafts", backend.RoleDrafts},
	{"deleteditems", backend.RoleTrash},
	{"junkemail", backend.RoleJunk},
	{"archive", backend.RoleArchive},
}

type graphFolder struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	ParentFolderID   string `json:"parentFolderId"`
	ChildFolderCount int    `json:"childFolderCount"`
	Removed          *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

type folderListResp struct {
	Value     []graphFolder `json:"value"`
	NextLink  string        `json:"@odata.nextLink"`
	DeltaLink string        `json:"@odata.deltaLink"`
}

func (r *Receiver) listChildren(ctx context.Context, path string, out *[]graphFolder, seen map[string]bool) error {
	next := path
	for next != "" {
		resp, err := callJSON[folderListResp](ctx, r.c, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		for _, f := range resp.Value {
			if f.Removed != nil || f.ID == "" || seen[f.ID] {
				continue
			}
			seen[f.ID] = true
			*out = append(*out, f)
			if f.ChildFolderCount > 0 {
				childPath := "/me/mailFolders/" + url.PathEscape(f.ID) + "/childFolders?$top=250&$select=id,displayName,parentFolderId,childFolderCount"
				if err := r.listChildren(ctx, childPath, out, seen); err != nil {
					return err
				}
			}
		}
		next = resp.NextLink
	}
	return nil
}

// folders lists every mail folder recursively, resolving standard roles via
// Graph v1.0's well-known folder paths (/me/mailFolders/{wellKnownName}).
// The hierarchy delta cursor is obtained BEFORE listing folders so any folder
// created after listChildren finishes remains ahead of the cursor and is
// observed on the next hierarchy round.
func (r *Receiver) folders(ctx context.Context) ([]backend.Folder, string, error) {
	var delta string
	if d, err := r.folderDeltaCursor(ctx); err == nil {
		delta = d
	} else if !isStatus(err, http.StatusNotFound) && !isStatus(err, http.StatusNotImplemented) {
		return nil, "", err
	}
	var raw []graphFolder
	seen := map[string]bool{}
	const rootQuery = "/me/mailFolders?$top=250&$select=id,displayName,parentFolderId,childFolderCount"
	if err := r.listChildren(ctx, rootQuery, &raw, seen); err != nil {
		return nil, "", err
	}
	roleByID := map[string]backend.Role{}
	for _, wk := range wellKnownRoles {
		f, err := callJSON[graphFolder](ctx, r.c, http.MethodGet, "/me/mailFolders/"+wk.name+"?$select=id", nil)
		if err == nil && f.ID != "" {
			roleByID[f.ID] = wk.role
		} else if err != nil && !isStatus(err, http.StatusNotFound) {
			return nil, "", err
		}
	}
	byID := map[string]*graphFolder{}
	for i := range raw {
		byID[raw[i].ID] = &raw[i]
	}
	var out []backend.Folder
	for i := range raw {
		f := &raw[i]
		name, parent := f.DisplayName, f.ParentFolderID
		for p, n := byID[parent], 0; p != nil && n < len(raw); p, n = byID[p.ParentFolderID], n+1 {
			name = p.DisplayName + "/" + name
		}
		if byID[parent] == nil {
			parent = ""
		}
		out = append(out, backend.Folder{
			Ref:    backend.FolderRef(f.ID),
			Name:   name,
			Delim:  "/",
			Parent: backend.FolderRef(parent),
			Role:   roleByID[f.ID],
		})
	}
	return out, delta, nil
}

func (r *Receiver) folderDeltaCursor(ctx context.Context) (string, error) {
	next := "/me/mailFolders/delta?$select=id,displayName,parentFolderId"
	for next != "" {
		resp, err := callJSON[folderListResp](ctx, r.c, http.MethodGet, next, nil)
		if err != nil {
			return "", err
		}
		if resp.DeltaLink != "" {
			return resp.DeltaLink, nil
		}
		next = resp.NextLink
	}
	return "", nil
}

func (r *Receiver) hierarchy(ctx context.Context, account backend.Cursor) ([]backend.Folder, backend.Cursor, error) {
	if account != "" {
		next := string(account)
		changed := false
		for next != "" {
			resp, err := callJSON[folderListResp](ctx, r.c, http.MethodGet, next, nil)
			if err != nil {
				if isDeltaReset(err) {
					break
				}
				return nil, "", err
			}
			if len(resp.Value) > 0 {
				changed = true
			}
			if resp.DeltaLink != "" {
				if !changed {
					return nil, backend.Cursor(resp.DeltaLink), nil
				}
				break
			}
			next = resp.NextLink
		}
	}
	fs, state, err := r.folders(ctx)
	return fs, backend.Cursor(state), err
}

type graphMessage struct {
	ID                string    `json:"id"`
	ParentFolderID    string    `json:"parentFolderId"`
	InternetMessageID string    `json:"internetMessageId"`
	ReceivedDateTime  time.Time `json:"receivedDateTime"`
	IsRead            *bool     `json:"isRead"`
	IsDraft           *bool     `json:"isDraft"`
	Categories        []string  `json:"categories"`
	Flag              *struct {
		FlagStatus string `json:"flagStatus"`
	} `json:"flag"`
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

func (m graphMessage) flags() backend.Flags {
	var f backend.Flags
	if m.IsRead != nil && *m.IsRead {
		f.Seen = true
	}
	if m.IsDraft != nil && *m.IsDraft {
		f.Draft = true
	}
	if m.Flag != nil && strings.EqualFold(m.Flag.FlagStatus, "flagged") {
		f.Flagged = true
	}
	if len(m.Categories) > 0 {
		f.Keywords = slices.Clone(m.Categories)
		slices.Sort(f.Keywords)
	}
	return f
}

type msgListResp struct {
	Value     []graphMessage `json:"value"`
	NextLink  string         `json:"@odata.nextLink"`
	DeltaLink string         `json:"@odata.deltaLink"`
}

const msgSelect = "id,parentFolderId,internetMessageId,receivedDateTime,isRead,isDraft,flag,categories"

// Sync implements backend.Receiver: folder hierarchy, then each folder's
// /me/mailFolders/{id}/messages/delta pages from its deltaLink cursor.
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
	if req.Only == nil && len(errs) == 0 && req.Local != nil {
		if err := r.reconcileLost(ctx, req, refs, commit); err != nil {
			return false, err
		}
	}
	return false, errors.Join(errs...)
}

// reconcileLost rechecks stored ImmutableIds that lost every known folder
// location during a full round (for example, a message moved from a later-polled
// folder into an already-polled folder mid-sync) before mailsync drops orphans.
func (r *Receiver) reconcileLost(ctx context.Context, req backend.SyncRequest, refs []backend.FolderRef, commit func(backend.Page) error) error {
	lost, err := req.Local.Lost(ctx)
	if err != nil || len(lost) == 0 {
		return err
	}
	knownFolders := map[backend.FolderRef]bool{}
	for _, ref := range refs {
		knownFolders[ref] = true
	}
	refreshedFolders := false
	var changes []backend.Change
	for _, id := range lost {
		if id == "" {
			continue
		}
		m, err := callJSON[graphMessage](ctx, r.c, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select="+msgSelect, nil)
		if isStatus(err, http.StatusNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if m.ID == "" || m.Removed != nil || m.ParentFolderID == "" {
			continue
		}
		fref := backend.FolderRef(m.ParentFolderID)
		if !knownFolders[fref] && !refreshedFolders {
			refreshedFolders = true
			fs, _, ferr := r.folders(ctx)
			if ferr != nil {
				return ferr
			}
			for _, f := range fs {
				if !knownFolders[f.Ref] {
					knownFolders[f.Ref] = true
					changes = append(changes, backend.FolderUpdated{Folder: f})
				}
			}
		}
		if !knownFolders[fref] {
			continue // e.g. Recoverable Items dumpster outside mailFolders
		}
		changes = append(changes, backend.MessageAppeared{
			Location: backend.Location{Folder: fref, Ref: backend.MessageRef(m.ID), ServerID: m.ID},
			Flags:    m.flags(),
			Backfill: true,
		})
	}
	if len(changes) == 0 {
		return nil
	}
	return commit(backend.Page{Changes: changes})
}

type folderCursor struct {
	Delta  string          `json:"d"`
	Window *backend.Window `json:"w,omitempty"`
}

func decodeFolderCursor(raw backend.Cursor) folderCursor {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return folderCursor{}
	}
	if strings.HasPrefix(s, "{") {
		var fc folderCursor
		if err := json.Unmarshal([]byte(s), &fc); err == nil && fc.Delta != "" {
			return fc
		}
	}
	return folderCursor{Delta: s}
}

func (fc folderCursor) encode() backend.Cursor {
	if fc.Delta == "" {
		return ""
	}
	b, _ := json.Marshal(fc)
	return backend.Cursor(b)
}

func (fc folderCursor) covers(w backend.Window) bool {
	h := fc.Window
	switch {
	case h == nil:
		return false
	case h.Newest == 0 && h.Since.IsZero(): // all mail
		return true
	case w.Newest > 0:
		return h.Newest >= w.Newest
	case !w.Since.IsZero():
		return h.Newest == 0 && !w.Since.Before(h.Since)
	}
	return false // w is all mail, h is not
}

func (r *Receiver) syncFolder(ctx context.Context, req backend.SyncRequest, folder backend.FolderRef, commit func(backend.Page) error) error {
	st := req.Folders[folder]
	var cut *time.Time
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

	cur := decodeFolderCursor(st.Cursor)
	needBackfill := cur.Delta != "" && !cur.covers(req.Window)
	nextURL := cur.Delta
	reset := false
	if nextURL == "" {
		nextURL = "/me/mailFolders/" + url.PathEscape(string(folder)) + "/messages/delta?$select=" + msgSelect
	}
	var finalDelta string
	for {
		resp, err := callJSON[msgListResp](ctx, r.c, http.MethodGet, nextURL, nil)
		if err != nil && isDeltaReset(err) && st.Cursor != "" {
			st = backend.FolderState{}
			cur = folderCursor{}
			needBackfill = false
			reset = true
			if err := commit(backend.Page{
				Changes: []backend.Change{backend.FolderReset{Ref: folder}},
				Folders: map[backend.FolderRef]backend.FolderState{folder: st},
			}); err != nil {
				return err
			}
			nextURL = "/me/mailFolders/" + url.PathEscape(string(folder)) + "/messages/delta?$select=" + msgSelect
			continue
		}
		if err != nil {
			return err
		}

		var live []graphMessage
		var changes []backend.Change
		for _, m := range resp.Value {
			if m.ID == "" {
				continue
			}
			if m.Removed != nil {
				changes = append(changes, backend.MessageGone{
					Location: backend.Location{Folder: folder, Ref: backend.MessageRef(m.ID), ServerID: m.ID},
				})
				continue
			}
			live = append(live, m)
		}

		held := map[backend.MessageRef]bool{}
		known := map[string]bool{}
		if req.Local != nil && len(live) > 0 {
			mrefs := make([]backend.MessageRef, len(live))
			ids := make([]string, len(live))
			for i, m := range live {
				mrefs[i], ids[i] = backend.MessageRef(m.ID), m.ID
			}
			if !reset {
				if held, err = req.Local.HeldRefs(ctx, folder, mrefs); err != nil {
					return err
				}
			}
			if known, err = req.Local.KnownIDs(ctx, ids); err != nil {
				return err
			}
		}

		for _, m := range live {
			loc := backend.Location{Folder: folder, Ref: backend.MessageRef(m.ID), ServerID: m.ID}
			// Delta responses may return only changed properties on an update; fetch
			// the message's full flag set when any flag field was omitted.
			fl, ok, err := r.fullFlags(ctx, m)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if held[loc.Ref] {
				changes = append(changes, backend.FlagsChanged{Location: loc, Flags: fl})
				continue
			}
			in, err := inWindow(m.ReceivedDateTime)
			if err != nil {
				return err
			}
			if !in && !known[m.ID] {
				continue
			}
			var content *backend.Content
			if !known[m.ID] {
				content, err = r.fetchContent(ctx, m.ID, m.ReceivedDateTime, req)
				if errors.Is(err, backend.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				known[m.ID] = true
			}
			changes = append(changes, backend.MessageAppeared{
				Location: loc,
				Flags:    fl,
				Backfill: !st.Synced,
				Content:  content,
			})
		}

		last := resp.DeltaLink != ""
		nextState := st
		if last {
			finalDelta = resp.DeltaLink
			savedWin := &req.Window
			if needBackfill {
				savedWin = cur.Window // keep narrower window until backfill finishes
			} else if cur.covers(req.Window) {
				savedWin = cur.Window
			}
			nextState = backend.FolderState{
				Cursor: folderCursor{Delta: finalDelta, Window: savedWin}.encode(),
				Synced: true,
				Filled: st.Filled,
			}
		}
		if err := commit(backend.Page{
			Changes: changes,
			Folders: map[backend.FolderRef]backend.FolderState{folder: nextState},
		}); err != nil {
			return err
		}
		if last {
			break
		}
		if resp.NextLink == "" {
			return nil
		}
		nextURL = resp.NextLink
	}
	if needBackfill && finalDelta != "" {
		return r.backfillWindow(ctx, req, folder, finalDelta, st.Filled, commit)
	}
	return nil
}

const backfillBatch = 50

// backfillWindow imports older messages newly covered by a widened SyncRequest.Window
// before advancing the folder cursor's recorded window.
func (r *Receiver) backfillWindow(ctx context.Context, req backend.SyncRequest, folder backend.FolderRef, deltaLink string, filled time.Time, commit func(backend.Page) error) error {
	next := fmt.Sprintf("/me/mailFolders/%s/messages?$orderby=receivedDateTime+desc&$top=%d&$select=%s",
		url.PathEscape(string(folder)), backfillBatch, msgSelect)
	seen := 0
	for next != "" {
		resp, err := callJSON[msgListResp](ctx, r.c, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		var inWin []graphMessage
		reachedEnd := false
		for _, m := range resp.Value {
			if m.ID == "" || m.Removed != nil {
				continue
			}
			if req.Window.Newest > 0 && seen >= req.Window.Newest {
				reachedEnd = true
				break
			}
			if !req.Window.Since.IsZero() && !m.ReceivedDateTime.IsZero() && m.ReceivedDateTime.Before(req.Window.Since) {
				reachedEnd = true
				break
			}
			seen++
			inWin = append(inWin, m)
		}
		if len(inWin) > 0 {
			mrefs := make([]backend.MessageRef, len(inWin))
			ids := make([]string, len(inWin))
			for i, m := range inWin {
				mrefs[i], ids[i] = backend.MessageRef(m.ID), m.ID
			}
			held := map[backend.MessageRef]bool{}
			known := map[string]bool{}
			if req.Local != nil {
				if held, err = req.Local.HeldRefs(ctx, folder, mrefs); err != nil {
					return err
				}
				if known, err = req.Local.KnownIDs(ctx, ids); err != nil {
					return err
				}
			}
			var changes []backend.Change
			for _, m := range inWin {
				loc := backend.Location{Folder: folder, Ref: backend.MessageRef(m.ID), ServerID: m.ID}
				if held[loc.Ref] {
					continue
				}
				var content *backend.Content
				if !known[m.ID] {
					content, err = r.fetchContent(ctx, m.ID, m.ReceivedDateTime, req)
					if errors.Is(err, backend.ErrNotFound) {
						continue
					}
					if err != nil {
						return err
					}
					known[m.ID] = true
				}
				changes = append(changes, backend.MessageAppeared{
					Location: loc,
					Flags:    m.flags(),
					Backfill: true,
					Content:  content,
				})
			}
			if len(changes) > 0 {
				if err := commit(backend.Page{Changes: changes}); err != nil {
					return err
				}
			}
		}
		if reachedEnd || resp.NextLink == "" {
			break
		}
		next = resp.NextLink
	}
	win := req.Window
	return commit(backend.Page{
		Folders: map[backend.FolderRef]backend.FolderState{
			folder: {
				Cursor: folderCursor{Delta: deltaLink, Window: &win}.encode(),
				Synced: true,
				Filled: filled,
			},
		},
	})
}

func (r *Receiver) fullFlags(ctx context.Context, m graphMessage) (backend.Flags, bool, error) {
	if m.IsRead != nil && m.IsDraft != nil && m.Flag != nil && m.Categories != nil {
		return m.flags(), true, nil
	}
	full, err := callJSON[graphMessage](ctx, r.c, http.MethodGet, "/me/messages/"+url.PathEscape(m.ID)+"?$select="+msgSelect, nil)
	if isStatus(err, http.StatusNotFound) {
		return backend.Flags{}, false, nil
	}
	if err != nil {
		return backend.Flags{}, false, err
	}
	return full.flags(), true, nil
}

func (r *Receiver) fetchMIME(ctx context.Context, id string) ([]byte, error) {
	raw, err := r.c.do(ctx, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"/$value", "", nil)
	if isStatus(err, http.StatusNotFound) {
		return nil, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
	}
	return raw, err
}

func (r *Receiver) fetchContent(ctx context.Context, id string, received time.Time, req backend.SyncRequest) (*backend.Content, error) {
	raw, err := r.fetchMIME(ctx, id)
	if err != nil {
		return nil, err
	}
	mp := parseMIMEStructure(raw)
	st := mp.toBackend("")
	texts := map[backend.PartRef][]byte{}
	if req.TextParts != nil {
		limit := req.TextBytes
		for _, pref := range req.TextParts(&st) {
			if body, ok := mp.findLeaf(pref); ok {
				if limit > 0 && int64(len(body)) > limit {
					body = body[:limit]
				}
				texts[pref] = body
			}
		}
	}
	return &backend.Content{
		Size:      int64(len(raw)),
		Date:      received,
		Header:    backend.HeaderFields(raw, req.HeaderFields),
		Structure: &st,
		Text:      texts,
	}, nil
}

func (r *Receiver) cutoff(ctx context.Context, folder backend.FolderRef, w backend.Window) (time.Time, error) {
	if w.Newest <= 0 {
		return w.Since, nil
	}
	q := fmt.Sprintf("/me/mailFolders/%s/messages?$orderby=receivedDateTime+desc&$skip=%d&$top=1&$select=id,receivedDateTime",
		url.PathEscape(string(folder)), w.Newest-1)
	resp, err := callJSON[msgListResp](ctx, r.c, http.MethodGet, q, nil)
	if err != nil || len(resp.Value) == 0 {
		return time.Time{}, err
	}
	return resp.Value[0].ReceivedDateTime, nil
}

const olderBatch = 200

func (r *Receiver) older(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (more bool, err error) {
	for _, ref := range req.Only {
		if !req.Folders[ref].Synced {
			return false, backend.ErrNotSynced
		}
	}
	for _, ref := range req.Only {
		var pick []graphMessage
		next := fmt.Sprintf("/me/mailFolders/%s/messages?$orderby=receivedDateTime+desc&$top=%d&$select=%s",
			url.PathEscape(string(ref)), olderBatch, msgSelect)
		for next != "" && len(pick) <= req.Older {
			resp, err := callJSON[msgListResp](ctx, r.c, http.MethodGet, next, nil)
			if err != nil {
				return false, err
			}
			refs := make([]backend.MessageRef, len(resp.Value))
			for i, m := range resp.Value {
				refs[i] = backend.MessageRef(m.ID)
			}
			held := map[backend.MessageRef]bool{}
			if req.Local != nil && len(refs) > 0 {
				if held, err = req.Local.HeldRefs(ctx, ref, refs); err != nil {
					return false, err
				}
			}
			for _, m := range resp.Value {
				if m.ID != "" && !held[backend.MessageRef(m.ID)] {
					pick = append(pick, m)
				}
			}
			next = resp.NextLink
		}
		if len(pick) > req.Older {
			pick, more = pick[:req.Older], true
		}
		known := map[string]bool{}
		if req.Local != nil && len(pick) > 0 {
			ids := make([]string, len(pick))
			for i, m := range pick {
				ids[i] = m.ID
			}
			if known, err = req.Local.KnownIDs(ctx, ids); err != nil {
				return false, err
			}
		}
		var changes []backend.Change
		for _, m := range pick {
			var content *backend.Content
			if !known[m.ID] {
				content, err = r.fetchContent(ctx, m.ID, m.ReceivedDateTime, req)
				if errors.Is(err, backend.ErrNotFound) {
					continue
				}
				if err != nil {
					return false, err
				}
				known[m.ID] = true
			}
			changes = append(changes, backend.MessageAppeared{
				Location: backend.Location{Folder: ref, Ref: backend.MessageRef(m.ID), ServerID: m.ID},
				Flags:    m.flags(),
				Backfill: true,
				Content:  content,
			})
		}
		if err := commit(backend.Page{Changes: changes, Folders: map[backend.FolderRef]backend.FolderState{ref: req.Folders[ref]}}); err != nil {
			return false, err
		}
	}
	return more, nil
}

// Fetch implements backend.Receiver.
func (r *Receiver) Fetch(ctx context.Context, req backend.ContentRequest) (io.ReadCloser, error) {
	raw, err := r.fetchMIME(ctx, string(req.Ref))
	if err != nil {
		return nil, err
	}
	switch {
	case req.Part != "":
		mp := parseMIMEStructure(raw)
		body, ok := mp.findLeaf(req.Part)
		if !ok {
			return nil, fmt.Errorf("graph: part %q: %w", req.Part, backend.ErrNotFound)
		}
		raw = body
	case req.HeaderFields != nil:
		raw = backend.HeaderFields(raw, req.HeaderFields)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

// Headers implements backend.Receiver.
func (r *Receiver) Headers(ctx context.Context, _ backend.FolderRef, refs []backend.MessageRef, fields []string) (map[backend.MessageRef][]byte, error) {
	out := map[backend.MessageRef][]byte{}
	for _, ref := range refs {
		raw, err := r.fetchMIME(ctx, string(ref))
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[ref] = backend.HeaderFields(raw, fields)
	}
	return out, nil
}

// Find implements backend.Receiver.
func (r *Receiver) Find(ctx context.Context, folder backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	want := "<" + strings.Trim(messageID, "<>") + ">"
	filter := "internetMessageId eq '" + strings.ReplaceAll(want, "'", "''") + "'"
	q := "/me/mailFolders/" + url.PathEscape(string(folder)) + "/messages?$filter=" + url.QueryEscape(filter) + "&$select=id,internetMessageId"
	var candidates []string
	for q != "" {
		resp, err := callJSON[msgListResp](ctx, r.c, http.MethodGet, q, nil)
		if err != nil {
			return nil, err
		}
		for _, m := range resp.Value {
			if strings.Trim(strings.TrimSpace(m.InternetMessageID), "<>") == strings.Trim(messageID, "<>") {
				candidates = append(candidates, m.ID)
			}
		}
		q = resp.NextLink
	}
	var out []backend.MessageRef
	if size <= 0 {
		for _, id := range candidates {
			out = append(out, backend.MessageRef(id))
		}
		return out, nil
	}
	for _, id := range candidates {
		raw, err := r.fetchMIME(ctx, id)
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) == size {
			out = append(out, backend.MessageRef(id))
		}
	}
	return out, nil
}

// Apply implements backend.Receiver.
func (r *Receiver) Apply(ctx context.Context, op backend.Op) (backend.Result, error) {
	switch op := op.(type) {
	case backend.SetFlags:
		return r.setFlags(ctx, op)
	case backend.AddToFolder, backend.RemoveFromFolder:
		return backend.Result{Outcome: backend.Unsupported, Reason: reasonOneFolder}, nil
	case backend.Move:
		return r.move(ctx, op.At, op.To, false)
	case backend.Copy:
		return r.move(ctx, op.At, op.To, true)
	case backend.Delete:
		return r.deleteToTrash(ctx, op.At)
	case backend.Purge:
		return r.purge(ctx, op)
	case backend.Append:
		return r.append(ctx, op)
	}
	return backend.Result{}, fmt.Errorf("graph: unknown op %T", op)
}

func written(newID string, wantRef bool, err error) (backend.Result, error) {
	var re *ResponseError
	switch {
	case errors.As(err, &re):
		return backend.Result{Outcome: backend.Failed, Reason: re.Error()}, nil
	case err != nil:
		return backend.Result{}, err
	case wantRef && newID == "":
		return backend.Result{Outcome: backend.Unlocated}, nil
	case wantRef:
		return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(newID)}, nil
	}
	return backend.Result{Outcome: backend.Done}, nil
}

func (r *Receiver) setFlags(ctx context.Context, op backend.SetFlags) (backend.Result, error) {
	id := cmp.Or(string(op.At.Ref), op.At.ServerID)
	patch := map[string]any{}
	switch {
	case op.Add.Seen:
		patch["isRead"] = true
	case op.Remove.Seen:
		patch["isRead"] = false
	}
	switch {
	case op.Add.Flagged:
		patch["flag"] = map[string]string{"flagStatus": "flagged"}
	case op.Remove.Flagged:
		patch["flag"] = map[string]string{"flagStatus": "notFlagged"}
	}

	kwAdd := filterKeywords(op.Add.Keywords)
	kwRem := filterKeywords(op.Remove.Keywords)
	if len(kwAdd) > 0 || len(kwRem) > 0 {
		cur, err := callJSON[graphMessage](ctx, r.c, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select=id,categories", nil)
		if isStatus(err, http.StatusNotFound) {
			return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
		}
		if err != nil {
			return backend.Result{}, err
		}
		cats := slices.Clone(cur.Categories)
		for _, k := range kwRem {
			cats = slices.DeleteFunc(cats, func(c string) bool { return strings.EqualFold(c, k) })
		}
		for _, k := range kwAdd {
			if !slices.ContainsFunc(cats, func(c string) bool { return strings.EqualFold(c, k) }) {
				cats = append(cats, k)
			}
		}
		if cats == nil {
			cats = []string{}
		}
		patch["categories"] = cats
	}
	if len(patch) == 0 {
		return backend.Result{Outcome: backend.Done}, nil
	}
	_, err := callJSON[graphMessage](ctx, r.c, http.MethodPatch, "/me/messages/"+url.PathEscape(id), patch)
	if isStatus(err, http.StatusNotFound) {
		return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
	}
	return written("", false, err)
}

func filterKeywords(kws []string) []string {
	var out []string
	for _, k := range kws {
		if strings.HasPrefix(k, `\`) { // IMAP session/purge flags such as \Deleted
			continue
		}
		out = append(out, k)
	}
	return out
}

func (r *Receiver) move(ctx context.Context, at backend.Location, to backend.FolderRef, copyOp bool) (backend.Result, error) {
	id := cmp.Or(string(at.Ref), at.ServerID)
	if !copyOp {
		if to == at.Folder {
			return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(id)}, nil
		}
		// With IdentityStable, check current folder first so a retried move after a
		// dropped response sees the message already in destination (or gone from both).
		cur, err := callJSON[graphMessage](ctx, r.c, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select=id,parentFolderId", nil)
		if isStatus(err, http.StatusNotFound) {
			return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
		}
		if err != nil {
			return backend.Result{}, err
		}
		if cur.ParentFolderID == string(to) {
			return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(cur.ID)}, nil
		}
		if at.Folder != "" && cur.ParentFolderID != "" && cur.ParentFolderID != string(at.Folder) {
			return backend.Result{}, fmt.Errorf("graph: message %s left folder %s: %w", id, at.Folder, backend.ErrNotFound)
		}
	}
	verb := "move"
	if copyOp {
		verb = "copy"
	}
	moved, err := callJSON[graphMessage](ctx, r.c, http.MethodPost,
		"/me/messages/"+url.PathEscape(id)+"/"+verb,
		map[string]string{"destinationId": string(to)})
	if isStatus(err, http.StatusNotFound) {
		// Verify whether the source message itself is missing vs the destination folder.
		if _, gerr := callJSON[graphMessage](ctx, r.c, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select=id", nil); isStatus(gerr, http.StatusNotFound) {
			return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
		}
	}
	return written(moved.ID, true, err)
}

func (r *Receiver) purge(ctx context.Context, op backend.Purge) (backend.Result, error) {
	id := cmp.Or(string(op.At.Ref), op.At.ServerID)
	if op.At.Folder != "" {
		cur, err := callJSON[graphMessage](ctx, r.c, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select=id,parentFolderId", nil)
		if isStatus(err, http.StatusNotFound) {
			return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
		}
		if err != nil {
			return backend.Result{}, err
		}
		if cur.ParentFolderID != "" && cur.ParentFolderID != string(op.At.Folder) {
			return backend.Result{}, fmt.Errorf("graph: message %s left folder %s: %w", id, op.At.Folder, backend.ErrNotFound)
		}
	}
	if op.Confirm != nil && !op.Confirm() {
		return backend.Result{Outcome: backend.Failed, Reason: backend.Cancelled}, nil
	}
	_, err := r.c.do(ctx, http.MethodPost, "/me/messages/"+url.PathEscape(id)+"/permanentDelete", "", nil)
	if isStatus(err, http.StatusNotFound) {
		return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
	}
	return written("", false, err)
}

func (r *Receiver) deleteToTrash(ctx context.Context, at backend.Location) (backend.Result, error) {
	id := cmp.Or(string(at.Ref), at.ServerID)
	trash, err := callJSON[graphFolder](ctx, r.c, http.MethodGet, "/me/mailFolders/deleteditems?$select=id", nil)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return backend.Result{}, err
	}
	if trash.ID == "" || backend.FolderRef(trash.ID) == at.Folder {
		_, err := r.c.do(ctx, http.MethodPost, "/me/messages/"+url.PathEscape(id)+"/permanentDelete", "", nil)
		if isStatus(err, http.StatusNotFound) {
			return backend.Result{}, fmt.Errorf("graph: message %s: %w", id, backend.ErrNotFound)
		}
		return written("", false, err)
	}
	res, err := r.move(ctx, at, backend.FolderRef(trash.ID), false)
	res.Ref = ""
	return res, err
}

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
	b64 := []byte(base64.StdEncoding.EncodeToString(op.Raw))
	raw, err := r.c.do(ctx, http.MethodPost, "/me/messages", "text/plain", b64)
	if err != nil {
		return written("", true, err)
	}
	var created graphMessage
	if err := json.Unmarshal(raw, &created); err != nil {
		return backend.Result{}, fmt.Errorf("graph: decode created message: %w", err)
	}
	patch := map[string]any{"isRead": op.Flags.Seen}
	if op.Flags.Flagged {
		patch["flag"] = map[string]string{"flagStatus": "flagged"}
	}
	if kws := filterKeywords(op.Flags.Keywords); len(kws) > 0 {
		patch["categories"] = kws
	}
	if _, err := callJSON[graphMessage](ctx, r.c, http.MethodPatch, "/me/messages/"+url.PathEscape(created.ID), patch); err != nil {
		return written("", true, err)
	}
	if op.To != "" && created.ParentFolderID != string(op.To) {
		moved, err := callJSON[graphMessage](ctx, r.c, http.MethodPost,
			"/me/messages/"+url.PathEscape(created.ID)+"/move",
			map[string]string{"destinationId": string(op.To)})
		return written(moved.ID, true, err)
	}
	return written(created.ID, true, nil)
}

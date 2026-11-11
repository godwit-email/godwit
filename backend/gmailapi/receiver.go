package gmailapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// AllMailRef is the synthetic All Mail folder: Gmail's labels.list has no
// All Mail entry, while messages.list (without spam/trash) is All Mail.
// Keeping it as RoleAll aligns archiving, sidebar 归档 and multi-location
// behavior with Gmail IMAP (backend/imapbackend).
const AllMailRef backend.FolderRef = "[Gmail]/All Mail"

// Receiver is a Gmail account's receiving backend over the Gmail REST API v1.
type Receiver struct {
	c *client

	mu       sync.Mutex
	signedIn bool
}

var _ backend.Receiver = (*Receiver)(nil)

// New returns a Receiver for the Gmail account cfg reaches.
func New(cfg Config) *Receiver { return &Receiver{c: newClient(cfg, 0)} }

// Close implements backend.Receiver.
func (r *Receiver) Close() error {
	r.c.http.CloseIdleConnections()
	return nil
}

var description = func() backend.Description {
	d := backend.Description{
		Features:     map[backend.Feature]backend.Support{},
		Identity:     backend.IdentityStable,
		PollInterval: time.Minute,
	}
	for _, f := range backend.AllFeatures {
		d.Features[f] = backend.Support{Level: backend.LevelSupported}
	}
	set := func(l backend.Level, reason string, fs ...backend.Feature) {
		for _, f := range fs {
			d.Features[f] = backend.Support{Level: l, Reason: reason}
		}
	}
	set(backend.LevelNoted, ReasonSystemLabelsFixed, backend.FeatureMove, backend.FeatureAddToFolder, backend.FeatureRemoveFromFolder)
	set(backend.LevelUnsupported, ReasonNoCopy, backend.FeatureCopy)
	set(backend.LevelUnsupported, ReasonNoAnswered, backend.FeatureAnswered)
	set(backend.LevelUnsupported, ReasonNoDraft, backend.FeatureDraft)
	set(backend.LevelUnsupported, ReasonNoKeywords, backend.FeatureKeywords)
	set(backend.LevelUnsupported, ReasonNoPush, backend.FeaturePush)
	set(backend.LevelUnsupported, ReasonFoldersPending, backend.FeatureFolderCreate, backend.FeatureFolderRename, backend.FeatureFolderDelete, backend.FeatureLabelDelete)
	return d
}()

func (r *Receiver) ensureSignedIn(ctx context.Context) error {
	r.mu.Lock()
	ok := r.signedIn
	r.mu.Unlock()
	if ok {
		return nil
	}
	if _, err := r.c.getProfile(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	r.signedIn = true
	r.mu.Unlock()
	return nil
}

// Describe implements backend.Receiver. Its first call signs in so refused
// credentials return ErrAuthFailed and a disabled API returns ErrAPIDisabled.
func (r *Receiver) Describe(ctx context.Context) (backend.Description, error) {
	if err := r.ensureSignedIn(ctx); err != nil {
		return backend.Description{}, err
	}
	d := description
	d.Features = maps.Clone(d.Features)
	return d, nil
}

// Check implements backend.Receiver.
func (r *Receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	labels, err := r.c.listLabels(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	r.mu.Lock()
	r.signedIn = true
	r.mu.Unlock()
	d, err := r.Describe(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	return backend.CheckResult{Host: r.c.host(), Folders: len(folders(labels)), Description: d}, nil
}

// folders maps Gmail labels to backend.Folder entries, aligned with
// backend/imapbackend's Gmail handling: UNREAD is a flag (Seen), STARRED is
// both RoleFlagged and Flagged, IMPORTANT is a regular folder (Role ""),
// CHAT and CATEGORY_* are skipped, and All Mail is RoleAll.
func folders(labels []apiLabel) []backend.Folder {
	byName := map[string]backend.FolderRef{}
	for _, l := range labels {
		if isFolderLabel(l) {
			byName[l.Name] = backend.FolderRef(l.ID)
		}
	}
	var out []backend.Folder
	for _, l := range labels {
		if !isFolderLabel(l) {
			continue
		}
		f := backend.Folder{
			Ref:   backend.FolderRef(l.ID),
			Name:  l.Name,
			Delim: "/",
			Role:  labelRole(l.ID),
		}
		if f.Role == "" {
			if i := strings.LastIndex(l.Name, "/"); i > 0 {
				f.Parent = byName[l.Name[:i]]
			}
		}
		out = append(out, f)
	}
	out = append(out, backend.Folder{
		Ref:   AllMailRef,
		Name:  string(AllMailRef),
		Delim: "/",
		Role:  backend.RoleAll,
	})
	return out
}

func isFolderLabel(l apiLabel) bool {
	if l.ID == "" || l.ID == string(AllMailRef) || l.ID == "UNREAD" || l.ID == "CHAT" || strings.HasPrefix(l.ID, "CATEGORY_") {
		return false
	}
	return true
}

func labelRole(id string) backend.Role {
	switch id {
	case "INBOX":
		return backend.RoleInbox
	case "SENT":
		return backend.RoleSent
	case "DRAFT":
		return backend.RoleDrafts
	case "SPAM":
		return backend.RoleJunk
	case "TRASH":
		return backend.RoleTrash
	case "STARRED":
		return backend.RoleFlagged
	}
	return ""
}

func flagsOf(labelIDs []string) backend.Flags {
	return backend.Flags{
		Seen:    !slices.Contains(labelIDs, "UNREAD"),
		Flagged: slices.Contains(labelIDs, "STARRED"),
		Draft:   slices.Contains(labelIDs, "DRAFT"),
	}
}

func inFolder(labelIDs []string, folder backend.FolderRef) bool {
	switch {
	case folder == "":
		return false
	case slices.Contains(labelIDs, "TRASH"):
		return folder == "TRASH"
	case slices.Contains(labelIDs, "SPAM"):
		return folder == "SPAM"
	case folder == AllMailRef:
		return !slices.Contains(labelIDs, "DRAFT")
	default:
		return slices.Contains(labelIDs, string(folder))
	}
}

// messageFolders returns the folders a message with labelIDs belongs to, with
// AllMailRef last so a primary folder (such as INBOX) carries Content on first sync.
// Trash and Spam are exclusive binned folders, matching Gmail IMAP.
func messageFolders(labelIDs []string, valid map[backend.FolderRef]bool) []backend.FolderRef {
	if slices.Contains(labelIDs, "TRASH") {
		if valid["TRASH"] {
			return []backend.FolderRef{"TRASH"}
		}
		return nil
	}
	if slices.Contains(labelIDs, "SPAM") {
		if valid["SPAM"] {
			return []backend.FolderRef{"SPAM"}
		}
		return nil
	}
	var out []backend.FolderRef
	seen := map[backend.FolderRef]bool{}
	for _, id := range labelIDs {
		ref := backend.FolderRef(id)
		if ref != AllMailRef && valid[ref] && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	slices.Sort(out)
	if valid[AllMailRef] && !slices.Contains(labelIDs, "DRAFT") {
		out = append(out, AllMailRef)
	}
	return out
}

type cursor struct {
	HistoryID string          `json:"h"`
	Window    *backend.Window `json:"w,omitempty"`
}

func (c cursor) covers(w backend.Window) bool {
	h := c.Window
	switch {
	case h == nil:
		return false
	case h.Newest == 0 && h.Since.IsZero():
		return true
	case w.Newest > 0:
		return h.Newest >= w.Newest
	case !w.Since.IsZero():
		return h.Newest == 0 && !w.Since.Before(h.Since)
	}
	return false
}

func (c cursor) encode() backend.Cursor {
	b, _ := json.Marshal(c)
	return backend.Cursor(b)
}

const pageSize = 100

// Sync implements backend.Receiver.
func (r *Receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	if req.Older > 0 {
		return r.older(ctx, req, commit)
	}
	if req.Account != "" {
		var cur cursor
		if err := json.Unmarshal([]byte(req.Account), &cur); err != nil {
			return false, fmt.Errorf("gmailapi: cursor %q: %w", req.Account, err)
		}
		err := r.incremental(ctx, req, cur, commit)
		if !errors.Is(err, backend.ErrNotFound) {
			return false, err
		}
		// historyId expired (HTTP 404): reconcile every folder by FolderSnapshot.
		return false, r.reconcile(ctx, req, commit)
	}
	return false, r.initial(ctx, req, commit)
}

type round struct {
	r      *Receiver
	req    backend.SyncRequest
	commit func(backend.Page) error
	page   []backend.Change
}

func (s *round) add(c ...backend.Change) { s.page = append(s.page, c...) }

func (s *round) flush(p backend.Page) error {
	p.Changes, s.page = s.page, nil
	if len(p.Changes) == 0 && p.Account == "" && len(p.Folders) == 0 {
		return nil
	}
	return s.commit(p)
}

func (r *Receiver) syncFolders(ctx context.Context, req backend.SyncRequest, s *round, markAll bool) ([]backend.Folder, map[backend.FolderRef]bool, map[backend.FolderRef]backend.FolderState, error) {
	labels, err := r.c.listLabels(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	r.mu.Lock()
	r.signedIn = true
	r.mu.Unlock()
	fs := folders(labels)
	valid := map[backend.FolderRef]bool{}
	states := map[backend.FolderRef]backend.FolderState{}
	for _, f := range fs {
		valid[f.Ref] = true
		s.add(backend.FolderUpdated{Folder: f})
		if markAll || !req.Folders[f.Ref].Synced {
			states[f.Ref] = backend.FolderState{Synced: true}
		}
	}
	for _, ref := range slices.Sorted(maps.Keys(req.Folders)) {
		if !valid[ref] {
			s.add(backend.FolderGone{Ref: ref})
		}
	}
	return fs, valid, states, nil
}

func windowQuery(w backend.Window) string {
	if !w.Since.IsZero() && w.Newest <= 0 {
		return "after:" + strconv.FormatInt(w.Since.Unix(), 10)
	}
	return ""
}

func isBinnedFolder(ref backend.FolderRef) bool {
	return ref == "TRASH" || ref == "SPAM"
}

func folderListParams(ref backend.FolderRef, extraQ string) (labelID, q string, includeSpamTrash bool) {
	if ref == AllMailRef {
		q = "-label:DRAFT"
		if extraQ != "" {
			q = extraQ + " " + q
		}
		return "", q, false
	}
	return string(ref), extraQ, isBinnedFolder(ref)
}

func (r *Receiver) listFolderWindow(ctx context.Context, ref backend.FolderRef, w backend.Window) ([]string, error) {
	labelID, q, inc := folderListParams(ref, windowQuery(w))
	return r.c.listMessages(ctx, labelID, q, inc, w.Newest)
}

func (r *Receiver) windowIDs(ctx context.Context, fs []backend.Folder, w backend.Window) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	// Non-AllMail folders first so INBOX/user labels lead in ordering.
	for _, passAll := range []bool{false, true} {
		for _, f := range fs {
			if (f.Ref == AllMailRef) != passAll {
				continue
			}
			ids, err := r.listFolderWindow(ctx, f.Ref, w)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	return out, nil
}

func (r *Receiver) initial(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) error {
	// Capture historyId before listing messages so changes during the list are
	// replayed idempotently on the next incremental round.
	prof, err := r.c.getProfile(ctx)
	if err != nil {
		return err
	}
	s := &round{r: r, req: req, commit: commit}
	fs, valid, states, err := r.syncFolders(ctx, req, s, true)
	if err != nil {
		return err
	}
	// Commit folders first so every MessageAppeared references a known folder.
	if err := s.flush(backend.Page{}); err != nil {
		return err
	}
	ids, err := r.windowIDs(ctx, fs, req.Window)
	if err != nil {
		return err
	}
	for chunk := range slices.Chunk(ids, pageSize) {
		if err := s.appearIDs(ctx, chunk, valid, nil, true, true); err != nil {
			return err
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}
	cur := cursor{HistoryID: prof.HistoryID, Window: &req.Window}
	p := backend.Page{Account: cur.encode(), Folders: states}
	return commit(p)
}

func (r *Receiver) incremental(ctx context.Context, req backend.SyncRequest, cur cursor, commit func(backend.Page) error) error {
	s := &round{r: r, req: req, commit: commit}
	fs, valid, states, err := r.syncFolders(ctx, req, s, false)
	if err != nil {
		return err
	}
	recs, latest, err := r.c.listHistory(ctx, cur.HistoryID)
	if err != nil {
		return err
	}
	if err := s.flush(backend.Page{}); err != nil {
		return err
	}

	added := map[string]bool{}
	changedSet := map[string]bool{}
	for _, rec := range recs {
		for _, it := range rec.MessagesAdded {
			if it.Message.ID != "" {
				added[it.Message.ID] = true
				changedSet[it.Message.ID] = true
			}
		}
		for _, it := range rec.MessagesDeleted {
			if it.Message.ID != "" {
				changedSet[it.Message.ID] = true
			}
		}
		for _, it := range rec.LabelsAdded {
			if it.Message.ID != "" {
				changedSet[it.Message.ID] = true
			}
		}
		for _, it := range rec.LabelsRemoved {
			if it.Message.ID != "" {
				changedSet[it.Message.ID] = true
			}
		}
	}

	refs := make([]backend.FolderRef, len(fs))
	for i, f := range fs {
		refs[i] = f.Ref
	}
	ids := slices.Sorted(maps.Keys(changedSet))
	for chunk := range slices.Chunk(ids, pageSize) {
		held, err := s.heldMap(ctx, refs, chunk)
		if err != nil {
			return err
		}
		known, err := s.knownMap(ctx, chunk)
		if err != nil {
			return err
		}
		now := map[string]map[backend.FolderRef]bool{}
		var live []*apiMessage
		for _, id := range chunk {
			format := "minimal"
			if !known[id] {
				format = "raw"
			}
			m, err := r.c.getMessage(ctx, id, format)
			if errors.Is(err, backend.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			mfs := messageFolders(m.LabelIDs, valid)
			set := map[backend.FolderRef]bool{}
			for _, f := range mfs {
				set[f] = true
			}
			now[id] = set
			live = append(live, m)
		}
		for _, id := range chunk {
			for _, f := range refs {
				if held[f][backend.MessageRef(id)] && !now[id][f] {
					s.add(backend.MessageGone{Location: backend.Location{Folder: f, Ref: backend.MessageRef(id), ServerID: id}})
				}
			}
		}
		for _, m := range live {
			backfill := !added[m.ID]
			if err := s.emitMessage(ctx, m, valid, held, known, backfill, false); err != nil {
				return err
			}
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}

	next := cursor{HistoryID: latest, Window: cur.Window}
	if !cur.covers(req.Window) {
		if err := s.widen(ctx, fs, valid, refs); err != nil {
			return err
		}
		next.Window = &req.Window
	}
	if req.Only == nil {
		if err := s.keepLostInAllMail(ctx, valid); err != nil {
			return err
		}
	}
	return commit(backend.Page{Account: next.encode(), Folders: states})
}

// reconcile runs when startHistoryId expired (HTTP 404): it brings in missing
// window messages and emits a FolderSnapshot for each folder so local storage
// diffs flags and vanished locations without resetting stable IDs.
func (r *Receiver) reconcile(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) error {
	prof, err := r.c.getProfile(ctx)
	if err != nil {
		return err
	}
	s := &round{r: r, req: req, commit: commit}
	fs, valid, states, err := r.syncFolders(ctx, req, s, true)
	if err != nil {
		return err
	}
	if err := s.flush(backend.Page{}); err != nil {
		return err
	}

	refs := make([]backend.FolderRef, len(fs))
	for i, f := range fs {
		refs[i] = f.Ref
	}
	ids, err := r.windowIDs(ctx, fs, req.Window)
	if err != nil {
		return err
	}
	snaps := map[backend.FolderRef]map[backend.MessageRef]backend.Flags{}
	for _, ref := range refs {
		snaps[ref] = map[backend.MessageRef]backend.Flags{}
	}
	seenMsg := map[string]*apiMessage{}

	for chunk := range slices.Chunk(ids, pageSize) {
		held, err := s.heldMap(ctx, refs, chunk)
		if err != nil {
			return err
		}
		known, err := s.knownMap(ctx, chunk)
		if err != nil {
			return err
		}
		for _, id := range chunk {
			format := "minimal"
			if !known[id] {
				format = "raw"
			}
			m, err := r.c.getMessage(ctx, id, format)
			if errors.Is(err, backend.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if !req.Window.Since.IsZero() && req.Window.Newest <= 0 {
				if dt := m.receivedAt(); !dt.IsZero() && dt.Before(req.Window.Since) {
					continue
				}
			}
			seenMsg[id] = m
			fl := flagsOf(m.LabelIDs)
			for _, f := range messageFolders(m.LabelIDs, valid) {
				snaps[f][backend.MessageRef(id)] = fl
			}
			if err := s.emitMessage(ctx, m, valid, held, known, true, false); err != nil {
				return err
			}
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}

	// Reconcile locally held messages omitted by the window (such as an older
	// message moved from A to B outside B's count/date window): emit any newly
	// gained current locations before committing FolderSnapshot.
	if req.Local != nil {
		heldByFolder := map[backend.FolderRef]map[backend.MessageRef]string{}
		var extraIDs []string
		extraSet := map[string]bool{}
		for _, ref := range refs {
			held, err := req.Local.Held(ctx, ref)
			if err != nil {
				return err
			}
			heldByFolder[ref] = held
			for mref := range held {
				id := string(mref)
				if _, seen := seenMsg[id]; !seen && !extraSet[id] {
					extraSet[id] = true
					extraIDs = append(extraIDs, id)
				}
			}
		}
		slices.Sort(extraIDs)
		if len(extraIDs) > 0 {
			known, err := s.knownMap(ctx, extraIDs)
			if err != nil {
				return err
			}
			for _, id := range extraIDs {
				format := "minimal"
				if !known[id] {
					format = "raw"
				}
				m, err := r.c.getMessage(ctx, id, format)
				if errors.Is(err, backend.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				fl := flagsOf(m.LabelIDs)
				var content *backend.Content
				for _, f := range messageFolders(m.LabelIDs, valid) {
					mref := backend.MessageRef(id)
					snaps[f][mref] = fl
					if _, wasHeld := heldByFolder[f][mref]; !wasHeld {
						if content == nil && !known[id] {
							if m.Raw == "" {
								full, err := r.c.getMessage(ctx, id, "raw")
								if errors.Is(err, backend.ErrNotFound) {
									break
								}
								if err != nil {
									return err
								}
								m = full
							}
							if content, err = s.buildContentFrom(m); err != nil {
								return err
							}
						}
						s.add(backend.MessageAppeared{
							Location: backend.Location{Folder: f, Ref: mref, ServerID: id},
							Flags:    fl,
							Backfill: true,
							Content:  content,
						})
						content, known[id] = nil, true
					}
				}
			}
			if err := s.flush(backend.Page{}); err != nil {
				return err
			}
		}
	}

	for _, ref := range refs {
		s.add(backend.FolderSnapshot{Folder: ref, Messages: snaps[ref]})
	}
	if err := s.flush(backend.Page{}); err != nil {
		return err
	}
	if req.Only == nil {
		if err := s.keepLostInAllMail(ctx, valid); err != nil {
			return err
		}
	}
	cur := cursor{HistoryID: prof.HistoryID, Window: &req.Window}
	return commit(backend.Page{Account: cur.encode(), Folders: states})
}

func (s *round) widen(ctx context.Context, fs []backend.Folder, valid map[backend.FolderRef]bool, refs []backend.FolderRef) error {
	ids, err := s.r.windowIDs(ctx, fs, s.req.Window)
	if err != nil {
		return err
	}
	for chunk := range slices.Chunk(ids, pageSize) {
		held, err := s.heldMap(ctx, refs, chunk)
		if err != nil {
			return err
		}
		if err := s.appearIDs(ctx, chunk, valid, held, true, true); err != nil {
			return err
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}
	return nil
}

func (s *round) keepLostInAllMail(ctx context.Context, valid map[backend.FolderRef]bool) error {
	if s.req.Local == nil || !valid[AllMailRef] {
		return nil
	}
	lost, err := s.req.Local.Lost(ctx)
	if err != nil || len(lost) == 0 {
		return err
	}
	for _, id := range lost {
		m, err := s.r.c.getMessage(ctx, id, "minimal")
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if slices.Contains(messageFolders(m.LabelIDs, valid), AllMailRef) {
			s.add(backend.MessageAppeared{
				Location: backend.Location{Folder: AllMailRef, Ref: backend.MessageRef(id), ServerID: id},
				Flags:    flagsOf(m.LabelIDs),
				Backfill: true,
			})
		}
	}
	return s.flush(backend.Page{})
}

func (s *round) heldMap(ctx context.Context, refs []backend.FolderRef, ids []string) (map[backend.FolderRef]map[backend.MessageRef]bool, error) {
	held := map[backend.FolderRef]map[backend.MessageRef]bool{}
	if s.req.Local == nil || len(ids) == 0 {
		return held, nil
	}
	mrefs := make([]backend.MessageRef, len(ids))
	for i, id := range ids {
		mrefs[i] = backend.MessageRef(id)
	}
	for _, f := range refs {
		var err error
		if held[f], err = s.req.Local.HeldRefs(ctx, f, mrefs); err != nil {
			return nil, err
		}
	}
	return held, nil
}

func (s *round) knownMap(ctx context.Context, ids []string) (map[string]bool, error) {
	if s.req.Local == nil || len(ids) == 0 {
		return map[string]bool{}, nil
	}
	return s.req.Local.KnownIDs(ctx, ids)
}

func (s *round) appearIDs(ctx context.Context, ids []string, valid map[backend.FolderRef]bool, held map[backend.FolderRef]map[backend.MessageRef]bool, backfill, checkSince bool) error {
	known, err := s.knownMap(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		format := "minimal"
		if !known[id] {
			format = "raw"
		}
		m, err := s.r.c.getMessage(ctx, id, format)
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := s.emitMessage(ctx, m, valid, held, known, backfill, checkSince); err != nil {
			return err
		}
	}
	return nil
}

func (s *round) emitMessage(ctx context.Context, m *apiMessage, valid map[backend.FolderRef]bool, held map[backend.FolderRef]map[backend.MessageRef]bool, known map[string]bool, backfill, checkSince bool) error {
	if checkSince && !s.req.Window.Since.IsZero() && s.req.Window.Newest <= 0 {
		if dt := m.receivedAt(); !dt.IsZero() && dt.Before(s.req.Window.Since) {
			return nil
		}
	}
	mfs := messageFolders(m.LabelIDs, valid)
	fl := flagsOf(m.LabelIDs)
	var content *backend.Content
	for _, f := range mfs {
		loc := backend.Location{Folder: f, Ref: backend.MessageRef(m.ID), ServerID: m.ID}
		if held != nil && held[f][loc.Ref] {
			s.add(backend.FlagsChanged{Location: loc, Flags: fl})
			continue
		}
		if content == nil && !known[m.ID] {
			if m.Raw == "" {
				full, err := s.r.c.getMessage(ctx, m.ID, "raw")
				if errors.Is(err, backend.ErrNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
				m = full
			}
			var err error
			if content, err = s.buildContentFrom(m); err != nil {
				return err
			}
		}
		s.add(backend.MessageAppeared{
			Location: loc,
			Flags:    fl,
			Backfill: backfill,
			Content:  content,
		})
		content, known[m.ID] = nil, true
	}
	return nil
}

func (s *round) buildContentFrom(m *apiMessage) (*backend.Content, error) {
	raw, err := decodeBase64URL(m.Raw)
	if err != nil {
		return nil, fmt.Errorf("gmailapi: decode message %s: %w", m.ID, err)
	}
	structPart, parts := parseMIME(raw)
	texts := map[backend.PartRef][]byte{}
	if structPart != nil && s.req.TextParts != nil {
		for _, pref := range s.req.TextParts(structPart) {
			b := parts[pref]
			if s.req.TextBytes > 0 && int64(len(b)) > s.req.TextBytes {
				b = b[:s.req.TextBytes]
			}
			texts[pref] = b
		}
	}
	return &backend.Content{
		Size:      int64(len(raw)),
		Date:      m.receivedAt(),
		Header:    backend.HeaderFields(raw, s.req.HeaderFields),
		Structure: structPart,
		Text:      texts,
	}, nil
}

func (r *Receiver) older(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	for _, ref := range req.Only {
		if !req.Folders[ref].Synced {
			return false, backend.ErrNotSynced
		}
	}
	labels, err := r.c.listLabels(ctx)
	if err != nil {
		return false, err
	}
	valid := map[backend.FolderRef]bool{}
	for _, f := range folders(labels) {
		valid[f.Ref] = true
	}
	s := &round{r: r, req: req, commit: commit}
	hasMore := false
	for _, ref := range req.Only {
		held := map[backend.MessageRef]string{}
		if req.Local != nil {
			if held, err = req.Local.Held(ctx, ref); err != nil {
				return false, err
			}
		}
		labelID, q, includeSpamTrash := folderListParams(ref, "")
		emitted := 0
		err = r.c.listMessagesPages(ctx, labelID, q, includeSpamTrash, listPageMax, func(page []string) (bool, error) {
			var unheld []string
			for _, id := range page {
				if _, ok := held[backend.MessageRef(id)]; !ok {
					unheld = append(unheld, id)
				}
			}
			if len(unheld) == 0 {
				return false, nil
			}
			known, err := s.knownMap(ctx, unheld)
			if err != nil {
				return false, err
			}
			for _, id := range unheld {
				format := "minimal"
				if !known[id] && emitted < req.Older {
					format = "raw"
				}
				m, err := r.c.getMessage(ctx, id, format)
				if errors.Is(err, backend.ErrNotFound) {
					continue
				}
				if err != nil {
					return false, err
				}
				if !inFolder(m.LabelIDs, ref) {
					continue
				}
				if emitted == req.Older {
					hasMore = true
					return true, nil
				}
				var content *backend.Content
				if !known[id] {
					if m.Raw == "" {
						if m, err = r.c.getMessage(ctx, id, "raw"); errors.Is(err, backend.ErrNotFound) {
							continue
						} else if err != nil {
							return false, err
						}
					}
					if content, err = s.buildContentFrom(m); err != nil {
						return false, err
					}
					known[id] = true
				}
				s.add(backend.MessageAppeared{
					Location: backend.Location{Folder: ref, Ref: backend.MessageRef(id), ServerID: id},
					Flags:    flagsOf(m.LabelIDs),
					Backfill: true,
					Content:  content,
				})
				emitted++
			}
			return false, nil
		})
		if err != nil {
			return false, err
		}
		if err := s.flush(backend.Page{}); err != nil {
			return false, err
		}
	}
	return hasMore, nil
}

// Fetch implements backend.Receiver.
func (r *Receiver) Fetch(ctx context.Context, req backend.ContentRequest) (io.ReadCloser, error) {
	id := cmp.Or(req.ServerID, string(req.Ref))
	m, err := r.c.getMessage(ctx, id, "raw")
	if err != nil {
		return nil, err
	}
	raw, err := decodeBase64URL(m.Raw)
	if err != nil {
		return nil, fmt.Errorf("gmailapi: decode message %s: %w", id, err)
	}
	switch {
	case req.Part != "":
		_, parts := parseMIME(raw)
		b, ok := parts[req.Part]
		if !ok {
			return nil, fmt.Errorf("gmailapi: part %q: %w", req.Part, backend.ErrNotFound)
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	case req.HeaderFields != nil:
		return io.NopCloser(bytes.NewReader(backend.HeaderFields(raw, req.HeaderFields))), nil
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

// Headers implements backend.Receiver.
func (r *Receiver) Headers(ctx context.Context, folder backend.FolderRef, refs []backend.MessageRef, fields []string) (map[backend.MessageRef][]byte, error) {
	out := map[backend.MessageRef][]byte{}
	for _, ref := range refs {
		rc, err := r.Fetch(ctx, backend.ContentRequest{
			Location:     backend.Location{Folder: folder, Ref: ref},
			HeaderFields: fields,
		})
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		b, rerr := io.ReadAll(rc)
		_ = rc.Close()
		if rerr != nil {
			return nil, rerr
		}
		out[ref] = b
	}
	return out, nil
}

// Find implements backend.Receiver.
func (r *Receiver) Find(ctx context.Context, folder backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	if messageID == "" {
		return nil, nil
	}
	labelID, q, includeSpamTrash := folderListParams(folder, "rfc822msgid:"+messageID)
	ids, err := r.c.listMessages(ctx, labelID, q, includeSpamTrash, 0)
	if err != nil {
		return nil, err
	}
	var out []backend.MessageRef
	for _, id := range ids {
		m, err := r.c.getMessage(ctx, id, "raw")
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !inFolder(m.LabelIDs, folder) {
			continue
		}
		raw, err := decodeBase64URL(m.Raw)
		if err != nil {
			continue
		}
		if size > 0 && int64(len(raw)) != size {
			continue
		}
		msg, err := mail.ReadMessage(bytes.NewReader(raw))
		if err == nil && strings.Trim(strings.TrimSpace(msg.Header.Get("Message-Id")), "<>") == messageID {
			out = append(out, backend.MessageRef(id))
		}
	}
	return out, nil
}

// Apply implements backend.Receiver.
func (r *Receiver) Apply(ctx context.Context, op backend.Op) (backend.Result, error) {
	switch op := op.(type) {
	case backend.Copy:
		return backend.Result{Outcome: backend.Unsupported, Reason: ReasonNoCopy}, nil
	case backend.SetFlags:
		return r.applyFlags(ctx, op)
	case backend.Move:
		if op.To == op.At.Folder {
			return backend.Result{Outcome: backend.Done, Ref: op.At.Ref}, nil
		}
		return r.applyLabels(ctx, op.At, op.To, false)
	case backend.AddToFolder:
		return r.applyLabels(ctx, op.At, op.To, true)
	case backend.RemoveFromFolder:
		return r.applyLabels(ctx, op.At, "", false)
	case backend.Delete:
		id := cmp.Or(op.At.ServerID, string(op.At.Ref))
		if err := r.c.trashMessage(ctx, id); err != nil {
			return mapApplyErr(err)
		}
		return backend.Result{Outcome: backend.Done}, nil
	case backend.Purge:
		if op.Confirm != nil && !op.Confirm() {
			return backend.Result{Outcome: backend.Failed, Reason: backend.Cancelled}, nil
		}
		id := cmp.Or(op.At.ServerID, string(op.At.Ref))
		if err := r.c.deleteMessage(ctx, id); err != nil {
			return mapApplyErr(err)
		}
		return backend.Result{Outcome: backend.Done}, nil
	case backend.Append:
		return r.applyAppend(ctx, op)
	default:
		return backend.Result{}, fmt.Errorf("gmailapi: unknown op %T", op)
	}
}

func (r *Receiver) applyFlags(ctx context.Context, op backend.SetFlags) (backend.Result, error) {
	id := cmp.Or(op.At.ServerID, string(op.At.Ref))
	var add, remove []string
	if op.Add.Seen {
		remove = append(remove, "UNREAD")
	}
	if op.Remove.Seen {
		add = append(add, "UNREAD")
	}
	if op.Add.Flagged {
		add = append(add, "STARRED")
	}
	if op.Remove.Flagged {
		remove = append(remove, "STARRED")
	}
	if len(add) == 0 && len(remove) == 0 {
		if _, err := r.c.getMessage(ctx, id, "minimal"); err != nil {
			return mapApplyErr(err)
		}
		return backend.Result{Outcome: backend.Done}, nil
	}
	if err := r.c.modifyMessage(ctx, id, add, remove); err != nil {
		return mapApplyErr(err)
	}
	return backend.Result{Outcome: backend.Done}, nil
}

func isImmutableSystemLabel(ref backend.FolderRef) bool {
	return ref == "SENT" || ref == "DRAFT"
}

func (r *Receiver) applyLabels(ctx context.Context, at backend.Location, to backend.FolderRef, addOnly bool) (backend.Result, error) {
	if isImmutableSystemLabel(to) || (!addOnly && isImmutableSystemLabel(at.Folder) && to != "TRASH" && to != "SPAM") {
		return backend.Result{Outcome: backend.Unsupported, Reason: ReasonSystemLabelsFixed}, nil
	}
	id := cmp.Or(at.ServerID, string(at.Ref))
	m, err := r.c.getMessage(ctx, id, "minimal")
	if err != nil {
		return mapApplyErr(err)
	}
	effectiveAddOnly := addOnly || at.Folder == "" || at.Folder == AllMailRef
	removeOnly := to == "" || to == AllMailRef
	inSrc := at.Folder == "" || inFolder(m.LabelIDs, at.Folder)
	inDst := to != "" && inFolder(m.LabelIDs, to)
	doneRes := func() backend.Result {
		if to != "" {
			return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(id)}
		}
		return backend.Result{Outcome: backend.Done}
	}
	if (effectiveAddOnly && inDst) || (at.Folder != "" && at.Folder == to) {
		return doneRes(), nil
	}
	if !inSrc {
		if !effectiveAddOnly && (removeOnly || inDst) {
			return doneRes(), nil
		}
		return backend.Result{}, fmt.Errorf("%w: the message left folder %q", backend.ErrNotFound, at.Folder)
	}
	if to == "TRASH" {
		if err := r.c.trashMessage(ctx, id); err != nil {
			return mapApplyErr(err)
		}
		return doneRes(), nil
	}
	if at.Folder == "TRASH" && to == AllMailRef && !effectiveAddOnly {
		if err := r.c.untrashMessage(ctx, id); err != nil {
			return mapApplyErr(err)
		}
		return doneRes(), nil
	}
	var add, remove []string
	if to != "" && to != AllMailRef {
		add = append(add, string(to))
	}
	if !effectiveAddOnly && at.Folder != "" && at.Folder != AllMailRef && !isImmutableSystemLabel(at.Folder) && at.Folder != to {
		remove = append(remove, string(at.Folder))
	}
	if len(add) > 0 || len(remove) > 0 {
		if err := r.c.modifyMessage(ctx, id, add, remove); err != nil {
			return mapApplyErr(err)
		}
	}
	return doneRes(), nil
}

func (r *Receiver) applyAppend(ctx context.Context, op backend.Append) (backend.Result, error) {
	if op.MessageID != "" {
		have, err := r.Find(ctx, op.To, op.MessageID, int64(len(op.Raw)))
		if err != nil {
			return backend.Result{}, err
		}
		if len(have) > 0 {
			return backend.Result{Outcome: backend.Done, Ref: have[0]}, nil
		}
	}
	var labels []string
	if op.To != "" && op.To != AllMailRef {
		labels = append(labels, string(op.To))
	}
	if !op.Flags.Seen {
		labels = append(labels, "UNREAD")
	}
	if op.Flags.Flagged {
		labels = append(labels, "STARRED")
	}
	id, err := r.c.insertMessage(ctx, op.Raw, labels)
	if err != nil {
		return mapApplyErr(err)
	}
	return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(id)}, nil
}

func mapApplyErr(err error) (backend.Result, error) {
	if errors.Is(err, backend.ErrNotFound) || errors.Is(err, backend.ErrAuthFailed) {
		return backend.Result{}, err
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.StatusCode >= http.StatusBadRequest && ae.StatusCode < http.StatusInternalServerError {
		return backend.Result{Outcome: backend.Failed, Reason: cmp.Or(ae.Message, ae.Error())}, nil
	}
	return backend.Result{}, err
}

// Watch implements backend.Receiver: Gmail API push requires Cloud Pub/Sub,
// which a desktop client cannot use, so core polls at PollInterval (1m).
func (r *Receiver) Watch(context.Context, func([]backend.FolderRef)) error {
	return backend.ErrUnsupported
}

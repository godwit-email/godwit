package jmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/godwit-email/godwit/backend"
)

// Receiver is a JMAP account's receiving backend. Its folders are mailboxes
// (FolderRef is the Mailbox id), a message's ServerID and every MessageRef of
// it are its Email id, and its cursor is the account's Mailbox and Email state.
type Receiver struct{ c *client }

var _ backend.Receiver = (*Receiver)(nil)

// New is a Receiver for the account cfg names. It connects on first use.
func New(cfg Config) *Receiver { return &Receiver{newClient(cfg)} }

// Close implements backend.Receiver.
func (r *Receiver) Close() error {
	r.c.http.CloseIdleConnections()
	return nil
}

// ReasonNoSubmission is the add-account check's refusal of a JMAP server (or
// API token) whose session does not advertise urn:ietf:params:jmap:submission.
const ReasonNoSubmission = "jmap.no_submission"

// ErrNoSubmission is Check or Send on a session without JMAP email submission.
// Its message is ReasonNoSubmission. Check returns it with a full CheckResult:
// the account can still receive.
var ErrNoSubmission = errors.New(ReasonNoSubmission)

const (
	reasonNoCopy  = "jmap.no_copy"         // JMAP has no copy within an account, only one more mailbox
	reasonFolders = "jmap.folders_pending" // Mailbox/set is not implemented yet
	reasonAllMail = "jmap.no_all_mail"     // an Email is always in some mailbox; archiving moves it to the archive one
)

var description = func() backend.Description {
	d := backend.Description{Features: map[backend.Feature]backend.Support{}, Identity: backend.IdentityStable, PollInterval: time.Minute}
	for _, f := range backend.AllFeatures {
		d.Features[f] = backend.Support{Level: backend.LevelSupported}
	}
	no := func(reason string, fs ...backend.Feature) {
		for _, f := range fs {
			d.Features[f] = backend.Support{Level: backend.LevelUnsupported, Reason: reason}
		}
	}
	no(reasonNoCopy, backend.FeatureCopy)
	no(reasonFolders, backend.FeatureFolderCreate, backend.FeatureFolderRename, backend.FeatureFolderDelete, backend.FeatureLabelDelete)
	no(reasonAllMail, backend.FeatureAllMail)
	return d
}()

// Describe implements backend.Receiver. Its first call signs in, fetching
// the session, so refused credentials are ErrAuthFailed here.
func (r *Receiver) Describe(ctx context.Context) (backend.Description, error) {
	s, acct, err := r.c.sess(ctx)
	if err != nil {
		return backend.Description{}, err
	}
	d := description
	d.Features = maps.Clone(d.Features)
	if !hasSubmission(s, acct) {
		d.Features[backend.FeatureSavesSent] = backend.Support{Level: backend.LevelUnsupported, Reason: ReasonNoSubmission}
	}
	return d, nil
}

// Check implements backend.Receiver.
func (r *Receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	s, acct, err := r.c.sess(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	boxes, _, err := r.mailboxes(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	r.c.mu.Lock()
	host := r.c.host
	r.c.mu.Unlock()
	d, _ := r.Describe(ctx)
	res := backend.CheckResult{Host: host, Folders: len(boxes), Description: d}
	if !hasSubmission(s, acct) { // receiving works: the result stands, only sending is refused
		return res, ErrNoSubmission
	}
	return res, nil
}

func (r *Receiver) mailboxes(ctx context.Context) ([]*mailbox.Mailbox, string, error) {
	out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
		return []gojmap.Method{&mailbox.Get{Account: acct}}
	})
	if err != nil {
		return nil, "", err
	}
	g := out[0].(*mailbox.GetResponse)
	return g.List, g.State, nil
}

// folders converts mailboxes, naming each by its path of names.
func folders(boxes []*mailbox.Mailbox) []backend.Folder {
	byID := map[gojmap.ID]*mailbox.Mailbox{}
	for _, m := range boxes {
		byID[m.ID] = m
	}
	var out []backend.Folder
	for _, m := range boxes {
		name := m.Name
		for p, n := byID[m.ParentID], 0; p != nil && n < len(boxes); p, n = byID[p.ParentID], n+1 {
			name = p.Name + "/" + name
		}
		out = append(out, backend.Folder{Ref: backend.FolderRef(m.ID), Name: name, Delim: "/",
			Parent: backend.FolderRef(m.ParentID), Role: role(m.Role)})
	}
	return out
}

func role(r mailbox.Role) backend.Role {
	switch r {
	case mailbox.RoleInbox, mailbox.RoleSent, mailbox.RoleDrafts, mailbox.RoleTrash, mailbox.RoleJunk,
		mailbox.RoleArchive, mailbox.RoleAll, mailbox.RoleFlagged:
		return backend.Role(r)
	}
	return ""
}

// cursor is the account's Mailbox and Email state, and the window whose
// mail it holds (nil for a cursor from before it was kept: unknown).
type cursor struct {
	Mailbox string          `json:"m"`
	Email   string          `json:"e"`
	Window  *backend.Window `json:"w,omitempty"`
}

// covers reports that the mail of window w is all in the window c holds.
// A window since a date moves on each round, so c keeps the earliest date.
func (c cursor) covers(w backend.Window) bool {
	h := c.Window
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

func (c cursor) encode() backend.Cursor {
	b, _ := json.Marshal(c)
	return backend.Cursor(b)
}

// emailProps are what a sync reads of an Email; the content comes from its blob.
var emailProps = []string{"id", "blobId", "mailboxIds", "keywords", "size", "receivedAt", "messageId"}

const pageSize = 100

// Sync implements backend.Receiver. It reports changes since the cursor and
// the mail a window wider than the cursor's adds, or
// everything in the window at first and after the server cannot calculate
// changes (then each folder is reset first). It always syncs the whole
// account: Only is not used, nor are Pages and PageSize, so more is false.
// shortcut: no 加载更早 (Older) on JMAP yet, so it is ErrUnsupported; add an
// Email/query past each folder's window when core asks for it.
func (r *Receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	if req.Older > 0 {
		return false, fmt.Errorf("jmap: older mail: %w", backend.ErrUnsupported)
	}
	var cur cursor
	if req.Account != "" {
		if err := json.Unmarshal([]byte(req.Account), &cur); err != nil {
			return false, fmt.Errorf("jmap: cursor %q: %w", req.Account, err)
		}
		err := r.changes(ctx, req, cur, commit)
		if !isMethodError(err, "cannotCalculateChanges") {
			return false, err
		}
	}
	return false, r.full(ctx, req, req.Account != "", commit)
}

// round is a sync round's working state.
type round struct {
	r      *Receiver
	req    backend.SyncRequest
	commit func(backend.Page) error
	page   []backend.Change
}

func (s *round) add(c ...backend.Change) { s.page = append(s.page, c...) }

func (s *round) flush(p backend.Page) error {
	p.Changes, s.page = s.page, nil
	return s.commit(p)
}

// full reports every mailbox and the mail in each one's window.
func (r *Receiver) full(ctx context.Context, req backend.SyncRequest, reset bool, commit func(backend.Page) error) error {
	boxes, mstate, err := r.mailboxes(ctx)
	if err != nil {
		return err
	}
	s := &round{r: r, req: req, commit: commit}
	states := map[backend.FolderRef]backend.FolderState{}
	for _, f := range folders(boxes) {
		s.add(backend.FolderUpdated{Folder: f})
		if reset {
			s.add(backend.FolderReset{Ref: f.Ref})
		}
		states[f.Ref] = backend.FolderState{Synced: true}
	}
	for ref := range req.Folders {
		if _, ok := states[ref]; !ok {
			s.add(backend.FolderGone{Ref: ref})
		}
	}
	// The state comes before the queries: an Email/get after a query in the
	// same request may already count mail the query did not see.
	out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method { return []gojmap.Method{emailState{acct, []gojmap.ID{}}} })
	if err != nil {
		return err
	}
	estate := out[0].(*email.GetResponse).State
	mails, err := r.window(ctx, boxes, req.Window)
	if err != nil {
		return err
	}
	for chunk := range slices.Chunk(mails, pageSize) {
		if err := s.appear(ctx, chunk, nil, true); err != nil {
			return err
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}
	return s.flush(backend.Page{Account: cursor{mstate, estate, &req.Window}.encode(), Folders: states})
}

// emailState is an Email/get of no ids: only the state. (email.Get leaves
// out an empty ids, which asks for every Email.)
type emailState struct {
	Account gojmap.ID   `json:"accountId"`
	IDs     []gojmap.ID `json:"ids"`
}

func (emailState) Name() string           { return "Email/get" }
func (emailState) Requires() []gojmap.URI { return []gojmap.URI{mail.URI} }

// window lists the mail of boxes in w, each Email once.
func (r *Receiver) window(ctx context.Context, boxes []*mailbox.Mailbox, w backend.Window) ([]*email.Email, error) {
	seen := map[gojmap.ID]bool{}
	var mails []*email.Email
	for _, m := range boxes {
		got, err := r.query(ctx, m.ID, w)
		if err != nil {
			return nil, err
		}
		for _, e := range got {
			if !seen[e.ID] {
				seen[e.ID] = true
				mails = append(mails, e)
			}
		}
	}
	return mails, nil
}

// query lists the mail of mailbox m in the window, newest first.
func (r *Receiver) query(ctx context.Context, m gojmap.ID, w backend.Window) ([]*email.Email, error) {
	var all []*email.Email
	for pos := 0; ; pos += pageSize {
		limit := pageSize
		if w.Newest > 0 {
			limit = min(limit, w.Newest-pos)
		}
		if limit <= 0 {
			break
		}
		f := &email.FilterCondition{InMailbox: m}
		if !w.Since.IsZero() {
			f.After = &w.Since
		}
		out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
			return []gojmap.Method{
				&email.Query{Account: acct, Filter: f, Sort: []*email.SortComparator{{Property: "receivedAt"}},
					Position: int64(pos), Limit: uint64(limit)},
				queried(acct),
			}
		})
		if err != nil {
			return nil, err
		}
		q, g := out[0].(*email.QueryResponse), out[1].(*email.GetResponse)
		all = append(all, g.List...)
		if len(q.IDs) < limit {
			break
		}
	}
	return all, nil
}

// queried is an Email/get of the sync properties of the ids that the
// request's first call, an Email/query, found.
func queried(acct gojmap.ID) *email.Get {
	return &email.Get{Account: acct, Properties: emailProps,
		ReferenceIDs: &gojmap.ResultReference{ResultOf: "0", Name: "Email/query", Path: "/ids"}}
}

// emails gets ids' sync properties; ids gone are left out.
func (r *Receiver) emails(ctx context.Context, ids []gojmap.ID) ([]*email.Email, error) {
	var all []*email.Email
	for chunk := range slices.Chunk(ids, pageSize) {
		out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
			return []gojmap.Method{&email.Get{Account: acct, Properties: emailProps, IDs: chunk}}
		})
		if err != nil {
			return nil, err
		}
		all = append(all, out[0].(*email.GetResponse).List...)
	}
	return all, nil
}

// appear reports each location of mails that held does not hold, with
// content for those local storage does not know.
func (s *round) appear(ctx context.Context, mails []*email.Email, held map[backend.FolderRef]map[backend.MessageRef]bool, backfill bool) error {
	known := map[string]bool{}
	if s.req.Local != nil {
		ids := make([]string, len(mails))
		for i, e := range mails {
			ids[i] = string(e.ID)
		}
		var err error
		if known, err = s.req.Local.KnownIDs(ctx, ids); err != nil {
			return err
		}
	}
	for _, e := range mails {
		var content *backend.Content
		for _, m := range slices.Sorted(maps.Keys(e.MailboxIDs)) {
			loc := backend.Location{Folder: backend.FolderRef(m), Ref: backend.MessageRef(e.ID), ServerID: string(e.ID)}
			if held[loc.Folder][loc.Ref] {
				s.add(backend.FlagsChanged{Location: loc, Flags: flags(e.Keywords)})
				continue
			}
			if content == nil && !known[string(e.ID)] {
				var err error
				if content, err = s.r.content(ctx, e, s.req.HeaderFields); err != nil {
					if errors.Is(err, backend.ErrNotFound) {
						break // gone since: the next round reports it
					}
					return err
				}
			}
			s.add(backend.MessageAppeared{Location: loc, Flags: flags(e.Keywords), Backfill: backfill, Content: content})
			content, known[string(e.ID)] = nil, true // its other locations are a stored message's
		}
	}
	return nil
}

// content downloads e's blob. The whole message goes to local parsing: a
// structure would need bodyStructure, which not every server gives.
// shortcut: downloads every new message whole; use bodyStructure and
// header:* properties where the server has them if this proves slow.
func (r *Receiver) content(ctx context.Context, e *email.Email, fields []string) (*backend.Content, error) {
	rc, err := r.c.download(ctx, e.BlobID)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	c := &backend.Content{Size: int64(e.Size), Header: backend.HeaderFields(raw, fields), Raw: raw}
	if e.ReceivedAt != nil {
		c.Date = *e.ReceivedAt
	}
	return c, nil
}

var keywords = map[string]func(*backend.Flags) *bool{
	"$seen":     func(f *backend.Flags) *bool { return &f.Seen },
	"$flagged":  func(f *backend.Flags) *bool { return &f.Flagged },
	"$answered": func(f *backend.Flags) *bool { return &f.Answered },
	"$draft":    func(f *backend.Flags) *bool { return &f.Draft },
}

func flags(kw map[string]bool) backend.Flags {
	var f backend.Flags
	for _, k := range slices.Sorted(maps.Keys(kw)) {
		if !kw[k] {
			continue
		}
		if p, ok := keywords[strings.ToLower(k)]; ok {
			*p(&f) = true
		} else {
			f.Keywords = append(f.Keywords, k)
		}
	}
	return f
}

// patchKeywords sets (on) or clears each keyword of f in p.
func patchKeywords(p gojmap.Patch, f backend.Flags, on bool) {
	var v any
	if on {
		v = true
	}
	for k, field := range keywords {
		if *field(&f) {
			p["keywords/"+k] = v
		}
	}
	for _, k := range f.Keywords {
		p["keywords/"+k] = v
	}
}

// changes reports what changed since cur.
func (r *Receiver) changes(ctx context.Context, req backend.SyncRequest, cur cursor, commit func(backend.Page) error) error {
	s := &round{r: r, req: req, commit: commit}
	changedBoxes := map[gojmap.ID]bool{}
	gone := map[gojmap.ID]bool{}
	mstate := cur.Mailbox
	for more := true; more; {
		out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
			return []gojmap.Method{&mailbox.Changes{Account: acct, SinceState: mstate, MaxChanges: pageSize}}
		})
		if err != nil {
			return err
		}
		c := out[0].(*mailbox.ChangesResponse)
		for _, id := range append(c.Created, c.Updated...) {
			changedBoxes[id] = true
		}
		for _, id := range c.Destroyed {
			gone[id] = true
		}
		mstate, more = c.NewState, c.HasMoreChanges
	}
	boxes, _, err := r.mailboxes(ctx)
	if err != nil {
		return err
	}
	states := map[backend.FolderRef]backend.FolderState{}
	var refs []backend.FolderRef
	for _, f := range folders(boxes) {
		if changedBoxes[gojmap.ID(f.Ref)] {
			s.add(backend.FolderUpdated{Folder: f})
		}
		if _, ok := req.Folders[f.Ref]; !ok {
			states[f.Ref] = backend.FolderState{Synced: true}
		}
		refs = append(refs, f.Ref)
	}
	for id := range gone {
		s.add(backend.FolderGone{Ref: backend.FolderRef(id)})
	}

	kind := map[gojmap.ID]bool{} // true: exists, false: destroyed
	estate := cur.Email
	for more := true; more; {
		out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
			return []gojmap.Method{&email.Changes{Account: acct, SinceState: estate, MaxChanges: pageSize}}
		})
		if err != nil {
			return err
		}
		c := out[0].(*email.ChangesResponse)
		for _, id := range append(c.Created, c.Updated...) {
			kind[id] = true
		}
		for _, id := range c.Destroyed {
			kind[id] = false
		}
		estate, more = c.NewState, c.HasMoreChanges
	}
	ids := slices.Sorted(maps.Keys(kind))
	for chunk := range slices.Chunk(ids, pageSize) {
		var live []gojmap.ID
		for _, id := range chunk {
			if kind[id] {
				live = append(live, id)
			}
		}
		mails, err := r.emails(ctx, live)
		if err != nil {
			return err
		}
		held := map[backend.FolderRef]map[backend.MessageRef]bool{}
		if req.Local != nil {
			mrefs := make([]backend.MessageRef, len(chunk))
			for i, id := range chunk {
				mrefs[i] = backend.MessageRef(id)
			}
			for _, f := range refs {
				if held[f], err = req.Local.HeldRefs(ctx, f, mrefs); err != nil {
					return err
				}
			}
		}
		now := map[gojmap.ID]map[gojmap.ID]bool{} // email -> its mailboxes
		for _, e := range mails {
			now[e.ID] = e.MailboxIDs
		}
		for _, id := range chunk { // locations gone, whole emails gone included
			for _, f := range refs {
				if held[f][backend.MessageRef(id)] && !now[id][gojmap.ID(f)] {
					s.add(backend.MessageGone{Location: backend.Location{Folder: f, Ref: backend.MessageRef(id), ServerID: string(id)}})
				}
			}
		}
		if err := s.appear(ctx, mails, held, false); err != nil {
			return err
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}
	next := cursor{mstate, estate, cur.Window}
	if !cur.covers(req.Window) { // the window grew: bring in what it now covers
		if err := s.widen(ctx, boxes, refs); err != nil {
			return err
		}
		next.Window = &req.Window
	}
	return s.flush(backend.Page{Account: next.encode(), Folders: states})
}

// widen reports the mail in the window that local storage does not hold in
// any of folders, as a backfill.
func (s *round) widen(ctx context.Context, boxes []*mailbox.Mailbox, folders []backend.FolderRef) error {
	mails, err := s.r.window(ctx, boxes, s.req.Window)
	if err != nil {
		return err
	}
	for chunk := range slices.Chunk(mails, pageSize) {
		held := map[backend.FolderRef]map[backend.MessageRef]bool{}
		if s.req.Local != nil {
			mrefs := make([]backend.MessageRef, len(chunk))
			for i, e := range chunk {
				mrefs[i] = backend.MessageRef(e.ID)
			}
			for _, f := range folders {
				var err error
				if held[f], err = s.req.Local.HeldRefs(ctx, f, mrefs); err != nil {
					return err
				}
			}
		}
		var fresh []*email.Email
		for _, e := range chunk {
			for m := range e.MailboxIDs {
				if !held[backend.FolderRef(m)][backend.MessageRef(e.ID)] {
					fresh = append(fresh, e)
					break
				}
			}
		}
		if err := s.appear(ctx, fresh, held, true); err != nil {
			return err
		}
		if err := s.flush(backend.Page{}); err != nil {
			return err
		}
	}
	return nil
}

// blob is the blob id of the Email id; ErrNotFound when it is gone.
func (r *Receiver) blob(ctx context.Context, id backend.MessageRef) (gojmap.ID, error) {
	out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
		return []gojmap.Method{&email.Get{Account: acct, IDs: []gojmap.ID{gojmap.ID(id)}, Properties: []string{"id", "blobId"}}}
	})
	if err != nil {
		return "", err
	}
	g := out[0].(*email.GetResponse)
	if len(g.List) == 0 {
		return "", fmt.Errorf("jmap: email %s: %w", id, backend.ErrNotFound)
	}
	return g.List[0].BlobID, nil
}

// Fetch implements backend.Receiver. Sync gives the whole message, which
// local storage parses, so a part is named by its IMAP part path ("1.2") and
// cut from the whole message, still transfer-encoded as IMAP gives it.
// shortcut: a part downloads the whole message; use the part's own blobId
// from bodyStructure if large mail makes that slow.
func (r *Receiver) Fetch(ctx context.Context, req backend.ContentRequest) (io.ReadCloser, error) {
	b, err := r.blob(ctx, req.Ref)
	if err != nil {
		return nil, err
	}
	rc, err := r.c.download(ctx, b)
	if err != nil || req.HeaderFields == nil && req.Part == "" {
		return rc, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if req.Part == "" {
		return io.NopCloser(bytes.NewReader(backend.HeaderFields(raw, req.HeaderFields))), nil
	}
	var path []int
	for s := range strings.SplitSeq(string(req.Part), ".") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("jmap: part %q: %w", req.Part, backend.ErrNotFound)
		}
		path = append(path, n)
	}
	sec := imapserver.ExtractBodySection(bytes.NewReader(raw), &imap.FetchItemBodySection{Part: path})
	if sec == nil {
		return nil, fmt.Errorf("jmap: part %q: %w", req.Part, backend.ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(sec)), nil
}

// Headers implements backend.Receiver.
// shortcut: one download per message, as Fetch; ask Email/get for the
// header:* properties of all refs at once if refreshes get slow.
func (r *Receiver) Headers(ctx context.Context, folder backend.FolderRef, refs []backend.MessageRef, fields []string) (map[backend.MessageRef][]byte, error) {
	out := map[backend.MessageRef][]byte{}
	for _, ref := range refs {
		rc, err := r.Fetch(ctx, backend.ContentRequest{Location: backend.Location{Folder: folder, Ref: ref}, HeaderFields: fields})
		if errors.Is(err, backend.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out[ref] = b
	}
	return out, nil
}

// Find implements backend.Receiver.
func (r *Receiver) Find(ctx context.Context, folder backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	f := &email.FilterCondition{InMailbox: gojmap.ID(folder), Header: []string{"Message-ID", messageID}}
	if size > 0 {
		f.MinSize, f.MaxSize = uint64(size), uint64(size)+1 // maxSize is exclusive (RFC 8621 4.4.1)
	}
	out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
		return []gojmap.Method{&email.Query{Account: acct, Filter: f}, queried(acct)}
	})
	if err != nil {
		return nil, err
	}
	mails := out[1].(*email.GetResponse).List
	var refs []backend.MessageRef
	// The header filter matches substrings, and not every server has it.
	for _, e := range mails {
		if slices.Contains(e.MessageID, messageID) && (size <= 0 || int64(e.Size) == size) {
			refs = append(refs, backend.MessageRef(e.ID))
		}
	}
	return refs, nil
}

// Apply implements backend.Receiver: each op is one Email/set. The Email id
// stays the same wherever the message goes, so Done carries it.
func (r *Receiver) Apply(ctx context.Context, op backend.Op) (backend.Result, error) {
	var at backend.Location
	p := gojmap.Patch{}
	destroy := false
	switch op := op.(type) {
	case backend.SetFlags:
		at = op.At
		patchKeywords(p, op.Remove, false)
		patchKeywords(p, op.Add, true)
	case backend.Move:
		if at = op.At; op.To == at.Folder {
			return backend.Result{Outcome: backend.Done, Ref: at.Ref}, nil
		}
		p["mailboxIds/"+string(at.Folder)] = nil
		p["mailboxIds/"+string(op.To)] = true
	case backend.AddToFolder:
		at = op.At
		p["mailboxIds/"+string(op.To)] = true
	case backend.RemoveFromFolder:
		at = op.At
		p["mailboxIds/"+string(at.Folder)] = nil
	case backend.Delete:
		at = op.At
		trash, err := r.trash(ctx)
		if err != nil {
			return backend.Result{}, err
		}
		if trash == "" || trash == at.Folder {
			destroy = true
		} else {
			p["mailboxIds"] = map[string]bool{string(trash): true}
		}
	case backend.Purge:
		if op.Confirm != nil && !op.Confirm() {
			return backend.Result{Outcome: backend.Failed, Reason: backend.Cancelled}, nil
		}
		at, destroy = op.At, true
	case backend.Copy:
		return backend.Result{Outcome: backend.Unsupported, Reason: reasonNoCopy}, nil
	case backend.Append:
		return r.append(ctx, op)
	default:
		return backend.Result{}, fmt.Errorf("jmap: unknown op %T", op)
	}
	id := gojmap.ID(at.Ref)
	set := &email.Set{}
	if destroy {
		set.Destroy = []gojmap.ID{id}
	} else {
		set.Update = map[gojmap.ID]gojmap.Patch{id: p}
	}
	out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
		set.Account = acct
		return []gojmap.Method{set}
	})
	if err != nil {
		return backend.Result{}, err
	}
	res := out[0].(*email.SetResponse)
	if e := res.NotUpdated[id]; e != nil {
		return failed(e), nil
	}
	if e := res.NotDestroyed[id]; e != nil {
		return failed(e), nil
	}
	switch op.(type) {
	case backend.Move, backend.AddToFolder:
		return backend.Result{Outcome: backend.Done, Ref: at.Ref}, nil
	}
	return backend.Result{Outcome: backend.Done}, nil
}

// append stores op.Raw in op.To by an upload and Email/import, unless op.To
// holds it already.
func (r *Receiver) append(ctx context.Context, op backend.Append) (backend.Result, error) {
	if op.MessageID != "" {
		have, err := r.Find(ctx, op.To, op.MessageID, int64(len(op.Raw)))
		if err != nil {
			return backend.Result{}, err
		}
		if len(have) > 0 {
			return backend.Result{Outcome: backend.Done, Ref: have[0]}, nil
		}
	}
	blob, err := r.c.upload(ctx, op.Raw)
	if err != nil {
		return backend.Result{}, err
	}
	kw := map[string]bool{}
	for k, field := range keywords {
		if *field(&op.Flags) {
			kw[k] = true
		}
	}
	for _, k := range op.Flags.Keywords {
		kw[k] = true
	}
	imp := &email.EmailImport{BlobID: blob, MailboxIDs: map[gojmap.ID]bool{gojmap.ID(op.To): true}, Keywords: kw}
	if !op.Date.IsZero() {
		imp.ReceivedAt = &op.Date
	}
	out, err := r.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
		return []gojmap.Method{&email.Import{Account: acct, Emails: map[string]*email.EmailImport{"m": imp}}}
	})
	if err != nil {
		return backend.Result{}, err
	}
	res := out[0].(*email.ImportResponse)
	if e := res.NotCreated["m"]; e != nil {
		return failed(e), nil
	}
	if e := res.Created["m"]; e != nil {
		return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(e.ID)}, nil
	}
	return backend.Result{Outcome: backend.Unlocated}, nil
}

func failed(e *gojmap.SetError) backend.Result {
	reason := e.Type
	if e.Description != nil && *e.Description != "" {
		reason += ": " + *e.Description
	}
	if e.Properties != nil {
		reason += " " + strings.Join(*e.Properties, ",")
	}
	return backend.Result{Outcome: backend.Failed, Reason: reason}
}

// trash is the trash mailbox, "" if there is none.
func (r *Receiver) trash(ctx context.Context) (backend.FolderRef, error) {
	boxes, _, err := r.mailboxes(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range boxes {
		if m.Role == mailbox.RoleTrash {
			return backend.FolderRef(m.ID), nil
		}
	}
	return "", nil
}

// Watch implements backend.Receiver over the session's EventSource. It wakes
// for every state the server sends, the one on connect included, which
// covers what changed while it was not connected. When the server closes the
// stream it connects again.
// shortcut: no read deadline, so a connection that dies silently is not
// noticed; ask for pings and time out on their absence if that bites.
func (r *Receiver) Watch(ctx context.Context, wake func(folders []backend.FolderRef)) error {
	for {
		s, _, err := r.c.sess(ctx)
		if err != nil {
			return err
		}
		if s.EventSourceURL == "" {
			return backend.ErrUnsupported
		}
		u := strings.NewReplacer("{types}", "*", "{closeafter}", "no", "{ping}", "0").Replace(s.EventSourceURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := r.c.do(req)
		if err != nil {
			return err
		}
		err = events(resp.Body, func() { wake(nil) })
		resp.Body.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		select { // the server closed the stream: connect again
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// events reads an event stream, calling state for each state event, until
// it ends (nil) or fails.
func events(body io.Reader, state func()) error {
	sc := bufio.NewScanner(body)
	typ, data := "", false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data && (typ == "" || typ == "state") {
				state()
			}
			typ, data = "", false
		case strings.HasPrefix(line, "event:"):
			typ = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = true
		}
	}
	return sc.Err()
}

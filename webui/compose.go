package webui

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sync"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
)

var (
	// ErrNoDraft is returned to a window that has no draft attached.
	ErrNoDraft = errors.New("this window has no draft")
	// ErrNotReady is returned before the data is open.
	ErrNotReady = errors.New("the data is not open yet")
	// ErrNoRevision is returned for a save without the revision it edits;
	// core skips the conflict check for revision 0 (core/compose.go SaveDraft).
	ErrNoRevision = errors.New("a save needs the revision it edits")
	// ErrNoPicker is returned for attaching files when there is no file dialog.
	ErrNoPicker = errors.New("no file dialog")
)

// saveHook, when set, runs in Save while it holds the session (tests).
var saveHook func()

// session is one window's draft and the parts of its body the page does not edit.
type session struct {
	mu               sync.Mutex // serializes the window's calls and its release
	draftID          int64
	quote, signature string // HTML, kept as loaded
	loaded           bool
	closed           bool // released: calls get ErrNoDraft
	gone             bool // sent or discarded: calls get ErrNoDraft, nothing to release
}

// adopt keeps d's quote and signature as the ones the page does not edit.
func (s *session) adopt(d core.Draft) (editable string) {
	editable, s.quote, s.signature = mail.SplitDraft(d.HTML)
	s.loaded = true
	return editable
}

// Compose keeps each compose window's session: its draft and the parts of
// the draft the page does not edit. It is not bound: pages reach it only
// through ComposePage.
type Compose struct {
	mu       sync.Mutex
	svc      *core.Service
	sessions map[int]*session
	closed   func(winID, gen int, saved bool)
	done     func(winID int)
}

// NewCompose is a Compose with no windows; SetService opens it up.
func NewCompose() *Compose { return &Compose{sessions: map[int]*session{}} }

// SetService gives Compose the service, once the data is open.
func (c *Compose) SetService(svc *core.Service) {
	c.mu.Lock()
	c.svc = svc
	c.mu.Unlock()
}

// Attach ties draftID to window winID, as the window opens.
func (c *Compose) Attach(winID int, draftID int64) {
	c.mu.Lock()
	c.sessions[winID] = &session{draftID: draftID}
	c.mu.Unlock()
}

// Detach forgets window winID's draft: new calls from it get ErrNoDraft; one
// already in progress finishes.
func (c *Compose) Detach(winID int) {
	c.mu.Lock()
	delete(c.sessions, winID)
	c.mu.Unlock()
}

// Release lets go of closed window winID: new calls from it get ErrNoDraft,
// the call in progress finishes first, then its draft is deleted if it is
// empty (core DeleteDraftIfEmpty, on the revision checked).
func (c *Compose) Release(winID int) {
	c.mu.Lock()
	s := c.sessions[winID]
	delete(c.sessions, winID)
	svc := c.svc
	c.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if svc == nil || s.gone {
		return
	}
	ctx := context.Background()
	d, err := svc.GetDraft(ctx, s.draftID)
	if err == nil {
		_, err = svc.DeleteDraftIfEmpty(ctx, d.ID, d.Revision)
	}
	if err != nil {
		log.Printf("releasing compose window %d: %v", winID, err)
	}
}

// exists reports whether draft id is still stored.
func (c *Compose) exists(id int64) bool {
	c.mu.Lock()
	svc := c.svc
	c.mu.Unlock()
	if svc == nil {
		return false
	}
	_, err := svc.GetDraft(context.Background(), id)
	return err == nil
}

// OnClosed sets the hook that hears the page's final flush: gen is the
// close attempt it answers (the Closing event's payload), saved whether it
// succeeded.
func (c *Compose) OnClosed(fn func(winID, gen int, saved bool)) {
	c.mu.Lock()
	c.closed = fn
	c.mu.Unlock()
}

// OnDone sets the hook told that window winID's draft was sent or
// discarded: the window can close without asking.
func (c *Compose) OnDone(fn func(winID int)) {
	c.mu.Lock()
	c.done = fn
	c.mu.Unlock()
}

// finish tells OnDone's hook winID is done; no session lock is held.
func (c *Compose) finish(winID int) {
	c.mu.Lock()
	fn := c.done
	c.mu.Unlock()
	if fn != nil {
		fn(winID)
	}
}

func (c *Compose) get(winID int) (*core.Service, *session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.svc == nil {
		return nil, nil, ErrNotReady
	}
	s := c.sessions[winID]
	if s == nil {
		return nil, nil, ErrNoDraft
	}
	return c.svc, s, nil
}

// PageDeps is what ComposePage needs from the platform.
type PageDeps struct {
	// WinOf names the calling window (mygo.CallerWindow(ctx).ID() in production).
	WinOf func(context.Context) (int, bool)
	// PickFiles asks for files to attach, in a dialog on window winID; none
	// picked is no error.
	PickFiles func(ctx context.Context, winID int) ([]string, error)
}

// ComposePage is what compose pages call (mygo.Bind); every method acts on
// the calling window's draft only.
type ComposePage struct {
	c *Compose
	d PageDeps
}

// NewComposePage is the bound facade of c.
func NewComposePage(c *Compose, d PageDeps) *ComposePage {
	return &ComposePage{c: c, d: d}
}

// open is the calling window and its session, locked: the caller unlocks it.
func (p *ComposePage) open(ctx context.Context) (int, *core.Service, *session, error) {
	id, ok := p.d.WinOf(ctx)
	if !ok {
		return 0, nil, nil, ErrNoDraft
	}
	svc, s, err := p.c.get(id)
	if err != nil {
		return 0, nil, nil, err
	}
	s.mu.Lock()
	if s.closed || s.gone {
		s.mu.Unlock()
		return 0, nil, nil, ErrNoDraft
	}
	return id, svc, s, nil
}

// Load is the calling window's draft, with the accounts, the theme and the
// page's copy.
func (p *ComposePage) Load(ctx context.Context) (ComposeState, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return ComposeState{}, err
	}
	defer s.mu.Unlock()
	d, err := svc.GetDraft(ctx, s.draftID)
	if err != nil {
		return ComposeState{}, err
	}
	accs, err := svc.ListAccounts(ctx)
	if err != nil {
		return ComposeState{}, err
	}
	st, err := svc.GetSettings(ctx)
	if err != nil {
		return ComposeState{}, err
	}
	editable := s.adopt(d)
	out := ComposeState{DraftID: d.ID, From: d.AccountID, To: nz(d.To), Cc: nz(d.Cc), Bcc: nz(d.Bcc),
		ReplyTo: d.ReplyTo, Subject: d.Subject, Body: editable, Quote: mail.HTMLToText(s.quote),
		Signature: mail.HTMLToText(s.signature), Attachments: attachments(d), Revision: d.Revision,
		UpdatedAt: d.UpdatedAt.UnixMilli(), Theme: ThemeOf(st), Text: composeText()}
	for _, a := range accs {
		out.Accounts = append(out.Accounts, Account{ID: a.ID, Email: a.Email})
	}
	return out, nil
}

// Latest is the draft as stored now, as Load, for resolving a conflict: its
// quote and signature become the ones saves keep.
func (p *ComposePage) Latest(ctx context.Context) (ComposeState, error) { return p.Load(ctx) }

// Save stores the edit in the calling window's draft, keeping its quote and
// signature. A draft changed elsewhere is a Conflict, not an error.
func (p *ComposePage) Save(ctx context.Context, e ComposeEdit) (SaveResult, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return SaveResult{}, err
	}
	defer s.mu.Unlock()
	if e.Revision == 0 {
		return SaveResult{}, ErrNoRevision
	}
	d, err := svc.GetDraft(ctx, s.draftID) // deleted elsewhere: an error, never a new draft
	if err != nil {
		return SaveResult{}, err
	}
	if !s.loaded { // saved before Load: keep what the draft has
		s.adopt(d)
	}
	d.To, d.Cc, d.Bcc, d.ReplyTo, d.Subject = e.To, e.Cc, e.Bcc, e.ReplyTo, e.Subject
	// A marker in the body would pass for the quote or the signature.
	d.HTML = mail.JoinDraft(mail.StripMarkers(e.Body), s.quote, s.signature)
	d.Revision = e.Revision
	if saveHook != nil {
		saveHook()
	}
	d, err = svc.SaveDraft(ctx, d)
	if errors.Is(err, core.ErrStaleDraft) {
		return SaveResult{Conflict: true}, nil
	}
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Revision: d.Revision}, nil
}

// SetAccount moves the calling window's draft to account id, whose
// signature replaces the old one; revision must be the stored one, else
// Conflict.
func (p *ComposePage) SetAccount(ctx context.Context, id, revision int64) (Change, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return Change{}, err
	}
	defer s.mu.Unlock()
	d, err := svc.SetDraftAccount(ctx, s.draftID, id, revision)
	if errors.Is(err, core.ErrStaleDraft) {
		return conflict, nil
	}
	if err != nil {
		return Change{}, err
	}
	s.adopt(d)
	return changeOf(d), nil
}

// AddAttachments asks for files in a dialog on the calling window and adds
// each to its draft, if the draft is at revision (else Conflict, without the
// dialog). A file over the limit or failing is reported, not an error; a
// write from elsewhere stops the adding with Conflict, the files added so
// far in Attachments.
//
// The dialog runs without the session lock: Release does not wait for it
// (AppKit gives no promise a sheet on a closed window ever ends), and once
// the window is released the files are dropped (ErrNoDraft).
func (p *ComposePage) AddAttachments(ctx context.Context, revision int64) (AttachResult, error) {
	res := AttachResult{TooLarge: []string{}, Failed: []string{}}
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return AttachResult{}, err
	}
	d, err := svc.GetDraft(ctx, s.draftID)
	s.mu.Unlock()
	if err != nil {
		return AttachResult{}, err
	}
	if d.Revision != revision {
		res.Change = conflict
		return res, nil
	}
	if p.d.PickFiles == nil {
		return AttachResult{}, ErrNoPicker
	}
	paths, err := p.d.PickFiles(ctx, win)
	if err != nil {
		return AttachResult{}, err
	}
	_, svc, s, err = p.open(ctx)
	if err != nil {
		return AttachResult{}, err
	}
	defer s.mu.Unlock()
	rev, stale := revision, false
	for _, path := range paths {
		_, now, err := svc.AddAttachmentAt(ctx, s.draftID, rev, path)
		if now != 0 {
			rev = now
		}
		switch {
		case errors.Is(err, core.ErrStaleDraft):
			stale = true
		case errors.Is(err, core.ErrTooLarge):
			res.TooLarge = append(res.TooLarge, filepath.Base(path))
		case err != nil:
			log.Printf("attaching %s to draft %d: %v", path, s.draftID, err)
			res.Failed = append(res.Failed, filepath.Base(path))
		}
		if stale {
			break
		}
	}
	if d, err = svc.GetDraft(ctx, s.draftID); err != nil {
		return AttachResult{}, err
	}
	res.Change = changeOf(d)
	res.Conflict = stale || d.Revision != rev // a write from elsewhere, even with no file picked
	return res, nil
}

// RemoveAttachment removes attachment id from the calling window's draft,
// if the draft is at revision (else Conflict).
func (p *ComposePage) RemoveAttachment(ctx context.Context, id, revision int64) (Change, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return Change{}, err
	}
	defer s.mu.Unlock()
	rev, err := svc.RemoveAttachmentAt(ctx, s.draftID, id, revision)
	if errors.Is(err, core.ErrStaleDraft) {
		return conflict, nil
	}
	if err != nil {
		return Change{}, err
	}
	d, err := svc.GetDraft(ctx, s.draftID)
	if err != nil {
		return Change{}, err
	}
	ch := changeOf(d)
	ch.Conflict = d.Revision != rev // a write from elsewhere since
	return ch, nil
}

// Send sends exactly revision of the calling window's draft, else Conflict.
// digest, from an earlier SendResult's warnings, sends despite them. Once
// sent, the window closes (OnDone).
func (p *ComposePage) Send(ctx context.Context, revision int64, digest string) (SendResult, error) {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return SendResult{}, err
	}
	if digest == "" {
		err = svc.SendRevision(ctx, s.draftID, revision)
	} else {
		err = svc.SendAcknowledgedRevision(ctx, s.draftID, revision, digest)
	}
	res := SendResult{Warnings: []SendWarning{}}
	var we *core.SendWarningsError
	var be *core.SendBlockedError
	switch {
	case err == nil:
		res.Sent, s.gone = true, true
	case errors.Is(err, core.ErrStaleDraft):
		res.Conflict, err = true, nil
	case errors.As(err, &we):
		for _, w := range we.Check.Warnings {
			res.Warnings = append(res.Warnings, SendWarning{Plugin: w.Plugin, Message: w.Message})
		}
		res.Digest, err = we.Check.Digest, nil
	case errors.As(err, &be):
		res.Blocked, err = &SendWarning{Plugin: be.Plugin, Message: be.Message}, nil
	default:
		res = SendResult{}
	}
	s.mu.Unlock()
	if res.Sent {
		p.c.finish(win)
	}
	return res, err
}

// Discard deletes the calling window's draft; the window closes (OnDone).
func (p *ComposePage) Discard(ctx context.Context) error {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return err
	}
	err = svc.DeleteDraft(ctx, s.draftID)
	s.gone = err == nil
	s.mu.Unlock()
	if err != nil {
		return err
	}
	p.c.finish(win)
	return nil
}

// Closed is the page's final flush for the close attempt gen, the payload
// of the Closing event it answers: saved reports whether it succeeded.
func (p *ComposePage) Closed(ctx context.Context, gen int, saved bool) error {
	id, ok := p.d.WinOf(ctx)
	if !ok {
		return ErrNoDraft
	}
	p.c.mu.Lock()
	_, ok = p.c.sessions[id]
	fn := p.c.closed
	p.c.mu.Unlock()
	if !ok {
		return ErrNoDraft
	}
	if fn != nil {
		fn(id, gen, saved)
	}
	return nil
}

// conflict is the Change for a draft not at the revision the page passed.
var conflict = Change{Conflict: true, Attachments: []Attachment{}}

// changeOf is d's state after a change.
func changeOf(d core.Draft) Change {
	_, _, sig := mail.SplitDraft(d.HTML)
	return Change{Revision: d.Revision, Attachments: attachments(d), Signature: mail.HTMLToText(sig)}
}

// attachments is d's attachments for the page, never nil.
func attachments(d core.Draft) []Attachment {
	out := []Attachment{}
	for _, a := range d.Attachments {
		out = append(out, Attachment{ID: a.ID, Filename: a.Filename, Size: a.Size})
	}
	return out
}

// nz is l, or an empty list for nil, so JSON carries [] and not null.
func nz(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

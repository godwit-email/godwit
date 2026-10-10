package webui

import (
	"context"
	"errors"
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
)

// Account is a sender the page can pick.
type Account struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

// ComposeState is what a compose page shows when it opens.
type ComposeState struct {
	DraftID  int64     `json:"draftId"`
	Accounts []Account `json:"accounts"`
	// From is the sending account's ID.
	From    int64    `json:"from"`
	To      []string `json:"to"`
	Cc      []string `json:"cc"`
	Bcc     []string `json:"bcc"`
	ReplyTo string   `json:"replyTo"`
	Subject string   `json:"subject"`
	// Body is the editable part as plain text (plan 1).
	Body string `json:"body"`
	// Quote and Signature are read-only plain text.
	Quote     string `json:"quote"`
	Signature string `json:"signature"`
	Revision  int64  `json:"revision"`
	Theme     Theme  `json:"theme"`
}

// ComposeEdit is what the page saves: the fields it edits, and the revision
// they were made against.
type ComposeEdit struct {
	To       []string `json:"to"`
	Cc       []string `json:"cc"`
	Bcc      []string `json:"bcc"`
	ReplyTo  string   `json:"replyTo"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Revision int64    `json:"revision"`
}

// SaveResult is the outcome of a save.
type SaveResult struct {
	Revision int64 `json:"revision"`
	// Conflict means the draft changed elsewhere: nothing was saved.
	Conflict bool `json:"conflict"`
}

// session is one window's draft and the parts of its body the page does not edit.
type session struct {
	mu               sync.Mutex // serializes the window's Load and Save
	draftID          int64
	quote, signature string // HTML, kept as loaded
	loaded           bool
}

// Compose keeps each compose window's session: its draft and the parts of
// the draft the page does not edit. It is not bound: pages reach it only
// through ComposePage.
type Compose struct {
	mu       sync.Mutex
	svc      *core.Service
	sessions map[int]*session
	closed   func(winID int, saved bool)
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

// Detach forgets window winID's draft.
func (c *Compose) Detach(winID int) {
	c.mu.Lock()
	delete(c.sessions, winID)
	c.mu.Unlock()
}

// OnClosed sets the hook that hears the page's final flush: saved reports
// whether it succeeded.
func (c *Compose) OnClosed(fn func(winID int, saved bool)) {
	c.mu.Lock()
	c.closed = fn
	c.mu.Unlock()
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

// ComposePage is what compose pages call (mygo.Bind); every method acts on
// the calling window's draft only.
type ComposePage struct {
	c     *Compose
	winOf func(context.Context) (int, bool)
}

// NewComposePage is the bound facade of c. winOf names the calling window
// (mygo.CallerWindow(ctx).ID() in production).
func NewComposePage(c *Compose, winOf func(context.Context) (int, bool)) *ComposePage {
	return &ComposePage{c: c, winOf: winOf}
}

func (p *ComposePage) win(ctx context.Context) (int, error) {
	id, ok := p.winOf(ctx)
	if !ok {
		return 0, ErrNoDraft
	}
	return id, nil
}

// Load is the calling window's draft, with the accounts and the theme.
func (p *ComposePage) Load(ctx context.Context) (ComposeState, error) {
	id, err := p.win(ctx)
	if err != nil {
		return ComposeState{}, err
	}
	svc, s, err := p.c.get(id)
	if err != nil {
		return ComposeState{}, err
	}
	s.mu.Lock()
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
	var editable string
	editable, s.quote, s.signature = mail.SplitDraft(d.HTML)
	s.loaded = true
	out := ComposeState{DraftID: d.ID, From: d.AccountID, To: nz(d.To), Cc: nz(d.Cc), Bcc: nz(d.Bcc),
		ReplyTo: d.ReplyTo, Subject: d.Subject, Body: bodyText(editable),
		Quote: mail.HTMLToText(s.quote), Signature: mail.HTMLToText(s.signature), Revision: d.Revision, Theme: ThemeOf(st)}
	for _, a := range accs {
		out.Accounts = append(out.Accounts, Account{ID: a.ID, Email: a.Email})
	}
	return out, nil
}

// Save stores the edit in the calling window's draft, keeping its quote and
// signature. A draft changed elsewhere is a Conflict, not an error.
func (p *ComposePage) Save(ctx context.Context, e ComposeEdit) (SaveResult, error) {
	id, err := p.win(ctx)
	if err != nil {
		return SaveResult{}, err
	}
	svc, s, err := p.c.get(id)
	if err != nil {
		return SaveResult{}, err
	}
	if e.Revision == 0 {
		return SaveResult{}, ErrNoRevision
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := svc.GetDraft(ctx, s.draftID)
	if err != nil {
		return SaveResult{}, err
	}
	if !s.loaded { // saved before Load: keep what the draft has
		_, s.quote, s.signature = mail.SplitDraft(d.HTML)
		s.loaded = true
	}
	d.To, d.Cc, d.Bcc, d.ReplyTo, d.Subject = e.To, e.Cc, e.Bcc, e.ReplyTo, e.Subject
	d.HTML = mail.JoinDraft(bodyHTML(e.Body), s.quote, s.signature)
	d.Revision = e.Revision
	d, err = svc.SaveDraft(ctx, d)
	if errors.Is(err, core.ErrStaleDraft) {
		return SaveResult{Conflict: true}, nil
	}
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Revision: d.Revision}, nil
}

// Closed is the page's final flush: saved reports whether it succeeded.
func (p *ComposePage) Closed(ctx context.Context, saved bool) error {
	id, err := p.win(ctx)
	if err != nil {
		return err
	}
	p.c.mu.Lock()
	_, ok := p.c.sessions[id]
	fn := p.c.closed
	p.c.mu.Unlock()
	if !ok {
		return ErrNoDraft
	}
	if fn != nil {
		fn(id, saved)
	}
	return nil
}

// nz is l, or an empty list for nil, so JSON carries [] and not null.
func nz(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

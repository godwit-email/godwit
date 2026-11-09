package imapbackend

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// PollInterval is how often an account syncs when the server cannot push.
// Tests lower it.
var PollInterval = 5 * time.Minute

const (
	// idleRestart ends and restarts a Watch's IDLE before servers drop it
	// (RFC 2177 asks for at least every 29 minutes).
	idleRestart = 25 * time.Minute
)

// Config is how to reach an account's IMAP server.
type Config struct {
	Host        string
	Port        int
	Security    conn.Security
	Credentials backend.CredentialsFunc
}

// Receiver is an IMAP account's backend.Receiver: one session, over one
// connection it dials on first use, signing in with Config.Credentials, and
// keeps until Close. It is not safe for concurrent use, and Watch holds the
// connection while it runs.
type Receiver struct {
	cfg   Config
	c     *imapclient.Client
	abort func() // cuts the connection without waiting for a literal
	caps  imap.CapSet
	// updates is Watch's while it runs: the server reported a change.
	updates atomic.Pointer[chan struct{}]
}

var _ backend.Receiver = (*Receiver)(nil)

// New is a Receiver on the account cfg reaches.
func New(cfg Config) *Receiver { return &Receiver{cfg: cfg} }

func (r *Receiver) client(ctx context.Context) (*imapclient.Client, error) {
	if r.c != nil {
		return r.c, nil
	}
	cr, err := r.cfg.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	c, abort, err := conn.DialIMAPAbortable(ctx, conn.Endpoint{Host: r.cfg.Host, Port: r.cfg.Port, Security: r.cfg.Security,
		Username: cr.Username, Password: cr.Password, AccessToken: cr.Token}, &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { r.changed() },
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil { // FLAGS and PERMANENTFLAGS change no message
				r.changed()
			}
		},
		Fetch: func(*imapclient.FetchMessageData) { r.changed() },
	})
	if err != nil {
		return nil, err
	}
	r.c, r.abort, r.caps = c, abort, c.Caps()
	return c, nil
}

// changed signals a running Watch.
func (r *Receiver) changed() {
	if u := r.updates.Load(); u != nil {
		select {
		case *u <- struct{}{}:
		default:
		}
	}
}

// trace logs the end of the session's traffic, masked, when *err is a server
// response go-imap could not parse (conn.LogTrace): the error report then
// shows what the server sent. Each verb defers it.
func (r *Receiver) trace(err *error) { conn.LogTrace(r.c, *err) }

// Close closes the connection; a second Close is harmless.
func (r *Receiver) Close() error {
	if r.c == nil {
		return nil
	}
	return r.c.Close()
}

// Fetch streams the content req names. Closing the stream before its end cuts
// the connection, since IMAP has no way to stop a literal; the session is then
// done.
func (r *Receiver) Fetch(ctx context.Context, req backend.ContentRequest) (_ io.ReadCloser, err error) {
	defer r.trace(&err)
	c, err := r.client(ctx)
	if err != nil {
		return nil, err
	}
	validity, uid := Decode(string(req.Ref))
	sel, err := c.Select(string(req.Folder), &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, err
	}
	if sel.UIDValidity != validity { // recreated since the sync
		return nil, backend.ErrFolderReset
	}
	sec := &imap.FetchItemBodySection{Peek: true}
	switch {
	case req.HeaderFields != nil:
		sec.Specifier, sec.HeaderFields = imap.PartSpecifierHeader, req.HeaderFields
	case req.Part != "":
		sec.Part = partPath(req.Part)
	}
	cmd := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
	for m := cmd.Next(); m != nil; m = cmd.Next() {
		for item := m.Next(); item != nil; item = m.Next() {
			if bs, ok := item.(imapclient.FetchItemDataBodySection); ok {
				if bs.Literal == nil { // IMAP allows NIL
					cmd.Close()
					return nil, errors.New("imap: the part is not available on the server")
				}
				return &literal{Reader: bs.Literal, cmd: cmd, abort: r.abort}, nil
			}
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, err
	}
	return nil, backend.ErrNotFound
}

// Headers reads the fields of refs in folder with one UID FETCH; refs of
// another UIDVALIDITY than the folder's are gone. A SELECT the server refuses
// is a backend.FolderError.
func (r *Receiver) Headers(ctx context.Context, folder backend.FolderRef, refs []backend.MessageRef, fields []string) (_ map[backend.MessageRef][]byte, err error) {
	defer r.trace(&err)
	c, err := r.client(ctx)
	if err != nil {
		return nil, err
	}
	sel, err := c.Select(string(folder), &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		var refused *imap.Error
		if errors.As(err, &refused) {
			return nil, &backend.FolderError{Folder: folder, Err: err}
		}
		return nil, err
	}
	var uids imap.UIDSet
	for _, ref := range refs {
		if v, uid := Decode(string(ref)); v == sel.UIDValidity && uid != 0 {
			uids.AddNum(imap.UID(uid))
		}
	}
	out := map[backend.MessageRef][]byte{}
	if len(uids) == 0 {
		return out, nil
	}
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: fields, Peek: true}
	msgs, err := c.Fetch(uids, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		out[backend.MessageRef(Encode(sel.UIDValidity, uint32(m.UID)))] = m.FindBodySection(sec)
	}
	return out, nil
}

// literal is a FETCH's body section being read.
type literal struct {
	io.Reader
	cmd   *imapclient.FetchCommand
	abort func()
	eof   bool
	done  bool
}

func (l *literal) Read(p []byte) (int, error) {
	n, err := l.Reader.Read(p)
	l.eof = l.eof || err == io.EOF
	return n, err
}

// Close ends the FETCH; a second Close does nothing.
func (l *literal) Close() error {
	if l.done {
		return nil
	}
	l.done = true
	if !l.eof {
		l.abort()     // the command would otherwise wait for the rest of the literal
		l.cmd.Close() // the dead connection ends it
		return nil
	}
	return l.cmd.Close()
}

// Watch selects INBOX and IDLEs on the session's connection, waking with
// INBOX at each change the server reports. It wakes once, with nil, as soon
// as INBOX is selected: a sync after that wake misses nothing. When ctx ends
// it returns with IDLE still running; Close ends it.
func (r *Receiver) Watch(ctx context.Context, wake func(folders []backend.FolderRef)) (err error) {
	defer r.trace(&err)
	c, err := r.client(ctx)
	if err != nil {
		return err
	}
	if !r.caps.Has(imap.CapIdle) {
		return backend.ErrUnsupported
	}
	updates := make(chan struct{}, 1)
	r.updates.Store(&updates)
	defer r.updates.Store(nil)
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return err
	}
	wake(nil)
	inbox := []backend.FolderRef{"INBOX"}
	for {
		// A NOOP first collects what the server queued meanwhile.
		if err := c.Noop().Wait(); err != nil {
			return err
		}
		cmd, err := c.Idle()
		if err != nil {
			return err
		}
		t := time.NewTimer(idleRestart)
		select {
		case <-updates:
			wake(inbox)
		case <-t.C:
		case <-c.Closed():
			return errors.New("imap: connection lost during IDLE")
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
		t.Stop()
		if err := cmd.Close(); err != nil {
			return err
		}
		if err := cmd.Wait(); err != nil {
			return err
		}
	}
}

// Check is the add-account check: a sign-in and the folders a sync would
// mirror. A failing LIST does not fail it; it reports no folders.
func (r *Receiver) Check(ctx context.Context) (_ backend.CheckResult, err error) {
	defer r.trace(&err)
	c, err := r.client(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	stop := context.AfterFunc(ctx, r.abort) // a stalled LIST ends with ctx
	defer stop()
	res := backend.CheckResult{Host: r.cfg.Host, Description: describe(r.caps)}
	list, _ := c.List("", "*", nil).Collect()
	for _, d := range list {
		if Selectable(d) {
			res.Folders++
		}
	}
	return res, nil
}

// Describe declares what the server's capabilities allow, as they were at
// sign-in.
func (r *Receiver) Describe(ctx context.Context) (backend.Description, error) {
	if _, err := r.client(ctx); err != nil {
		return backend.Description{}, err
	}
	return describe(r.caps), nil
}

func describe(caps imap.CapSet) backend.Description {
	yes := backend.Support{Level: backend.LevelSupported}
	no := func(reason string) backend.Support {
		return backend.Support{Level: backend.LevelUnsupported, Reason: reason}
	}
	either := func(ok bool, reason string) backend.Support {
		if ok {
			return yes
		}
		return no(reason)
	}
	gmail := caps.Has(GmailFeature)
	d := backend.Description{Identity: backend.IdentityLocation, PollInterval: PollInterval, Features: map[backend.Feature]backend.Support{
		// Without MOVE a move is COPY, \Deleted and UID EXPUNGE, which needs
		// UIDPLUS; on Gmail it is label writes.
		backend.FeatureMove:             either(caps.Has(imap.CapMove) || caps.Has(imap.CapUIDPlus) || gmail, "imap.no_move"),
		backend.FeatureCopy:             either(!gmail, "imap.gmail_copy_is_label"),
		backend.FeatureAddToFolder:      either(gmail, "imap.no_labels"),
		backend.FeatureRemoveFromFolder: either(gmail, "imap.no_labels"),
		backend.FeaturePurge:            either(caps.Has(imap.CapUIDPlus), "imap.no_uidplus"),
		backend.FeatureAppend:           yes,
		backend.FeatureSeen:             yes,
		backend.FeatureFlagged:          yes,
		backend.FeatureAnswered:         yes,
		backend.FeatureDraft:            yes,
		backend.FeatureKeywords:         yes,
		backend.FeatureFolderCreate:     yes,
		backend.FeatureFolderRename:     yes,
		backend.FeatureFolderDelete:     either(!gmail, "imap.gmail_folder_is_label"),
		backend.FeatureLabelDelete:      either(gmail, "imap.no_labels"),
		backend.FeatureSavesSent:        no("imap.client_saves_sent"),
		backend.FeaturePush:             either(caps.Has(imap.CapIdle), "imap.no_idle"),
		backend.FeatureAllMail:          either(gmail, "imap.no_labels"),
	}}
	if gmail {
		d.Identity = backend.IdentityStable
	}
	return d
}

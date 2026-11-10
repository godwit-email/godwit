// Package pop3 is the POP3 receiving backend (ADR 0037, spec #627). A POP3
// server has one inbox and no flags: a message's server location is its UIDL,
// flags and moves stay local, and only Purge writes back (DELE).
package pop3

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"slices"
	"time"

	gopop3 "github.com/knadh/go-pop3"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// Config is where a POP3 account's server is and how to sign in to it.
type Config struct {
	Host        string
	Port        int
	Security    conn.Security
	Credentials backend.CredentialsFunc
	// PageBytes bounds the raw mail one sync page holds before it is
	// committed, as whole messages can be large; 0 for 32 MiB.
	PageBytes int64
}

// Inbox is the one folder's ref.
const Inbox backend.FolderRef = "INBOX"

// Reason keys, translated by the UI and Wit.
const (
	ReasonLocalMove  = "pop3.local_move"  // moves happen on this computer only
	ReasonLocalFlags = "pop3.local_flags" // flags are kept on this computer only
	ReasonInboxOnly  = "pop3.inbox_only"  // a POP3 server has only an inbox
	ReasonNone       = "pop3.not_provided"
	ReasonNoUIDL     = "pop3.no_uidl"
)

// ErrNoUIDL means the server lacks UIDL, so messages cannot be told apart
// across sessions. Its message is ReasonNoUIDL.
var ErrNoUIDL = errors.New(ReasonNoUIDL)

var description = func() backend.Description {
	d := backend.Description{Features: map[backend.Feature]backend.Support{}, Identity: backend.IdentityInboxOnly, PollInterval: 5 * time.Minute}
	set := func(l backend.Level, reason string, fs ...backend.Feature) {
		for _, f := range fs {
			d.Features[f] = backend.Support{Level: l, Reason: reason}
		}
	}
	set(backend.LevelNoted, ReasonLocalMove, backend.FeatureMove)
	set(backend.LevelNoted, ReasonLocalFlags, backend.FeatureSeen, backend.FeatureFlagged, backend.FeatureAnswered, backend.FeatureDraft, backend.FeatureKeywords)
	set(backend.LevelSupported, "", backend.FeaturePurge)
	set(backend.LevelUnsupported, ReasonInboxOnly, backend.FeatureCopy, backend.FeatureAddToFolder, backend.FeatureRemoveFromFolder, backend.FeatureAppend,
		backend.FeatureFolderCreate, backend.FeatureFolderRename, backend.FeatureFolderDelete, backend.FeatureLabelDelete)
	set(backend.LevelUnsupported, ReasonNone, backend.FeatureSavesSent, backend.FeaturePush, backend.FeatureAllMail)
	return d
}()

// Receiver is a POP3 account's receiving backend. Message numbers hold only
// within a session, so each call signs in, works and QUITs.
type Receiver struct{ cfg Config }

var _ backend.Receiver = (*Receiver)(nil)

// New is a Receiver on the account cfg reaches.
func New(cfg Config) *Receiver { return &Receiver{cfg} }

// Close implements backend.Receiver: no connection outlives a call.
func (r *Receiver) Close() error { return nil }

// entry is one message of a session's maildrop listing.
type entry struct {
	n    int // the message number in this session
	uid  string
	size int64
}

// session is one signed-in POP3 session and its listing, oldest first.
type session struct {
	c    *gopop3.Conn
	list []entry
}

func (s *session) find(ref backend.MessageRef) (entry, bool) {
	i := slices.IndexFunc(s.list, func(e entry) bool { return e.uid == string(ref) })
	if i < 0 {
		return entry{}, false
	}
	return s.list[i], true
}

// do signs in, lists the maildrop and runs f; it QUITs after, which is when
// the server carries out a DELE. ctx ending cuts the connection.
func (r *Receiver) do(ctx context.Context, f func(*session) error) error {
	cr, err := r.cfg.Credentials(ctx)
	if err != nil {
		return err
	}
	c, cut, err := conn.DialPOP3(ctx, conn.Endpoint{Host: r.cfg.Host, Port: r.cfg.Port, Security: r.cfg.Security, Username: cr.Username, Password: cr.Password})
	if err != nil {
		return err
	}
	defer context.AfterFunc(ctx, cut)()
	defer cut()
	s := &session{c: c}
	if err = s.load(); err == nil {
		err = f(s)
	}
	if err == nil {
		err = c.Quit()
	}
	return wrap(ctx, err)
}

func (s *session) load() error {
	uids, err := s.c.Uidl(0)
	if err != nil {
		if serverErr(err) {
			return ErrNoUIDL
		}
		return err
	}
	sizes, err := s.c.List(0)
	if err != nil {
		return err
	}
	size := map[int]int64{}
	for _, m := range sizes {
		size[m.ID] = int64(m.Size)
	}
	for _, m := range uids {
		s.list = append(s.list, entry{m.ID, m.UID, size[m.ID]})
	}
	return nil
}

// serverErr reports an -ERR reply: go-pop3 gives it as a plain error, while
// a connection failure is a net.Error or EOF.
func serverErr(err error) bool {
	var ne net.Error
	return err != nil && !errors.As(err, &ne) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, net.ErrClosed)
}

// wrap prefers ctx's error over the closed connection it caused.
func wrap(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Describe implements backend.Receiver.
func (r *Receiver) Describe(context.Context) (backend.Description, error) {
	d := description
	d.Features = maps.Clone(d.Features)
	return d, nil
}

// Check implements backend.Receiver: signing in and UIDL must work.
func (r *Receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	if err := r.do(ctx, func(*session) error { return nil }); err != nil {
		return backend.CheckResult{}, err
	}
	d, _ := r.Describe(ctx)
	return backend.CheckResult{Host: r.cfg.Host, Folders: 1, Description: d}, nil
}

// Watch implements backend.Receiver: POP3 cannot push.
func (r *Receiver) Watch(context.Context, func([]backend.FolderRef)) error {
	return backend.ErrUnsupported
}

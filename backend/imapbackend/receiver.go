package imapbackend

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/backend"
)

// PollInterval is how often an account syncs when the server cannot push.
const PollInterval = 5 * time.Minute

// Dial connects and signs in to the account's server.
type Dial func(ctx context.Context) (*imapclient.Client, error)

// Receiver is an IMAP account's backend.Receiver. It dials on first use and
// keeps the connection until Close. It is not safe for concurrent use.
//
// shortcut: core dials and signs in for it (Dial) and keeps reading and
// writing over the same connection itself; #603 moves the connection and its
// credentials here, #602 the write-back.
type Receiver struct {
	dial Dial
	c    *imapclient.Client
}

var _ backend.Receiver = (*Receiver)(nil)

// New is a Receiver that connects with dial.
func New(dial Dial) *Receiver { return &Receiver{dial: dial} }

func (r *Receiver) client(ctx context.Context) (*imapclient.Client, error) {
	if r.c == nil {
		c, err := r.dial(ctx)
		if err != nil {
			return nil, err
		}
		r.c = c
	}
	return r.c, nil
}

// Close closes the connection it dialed.
func (r *Receiver) Close() error {
	if r.c == nil {
		return nil
	}
	return r.c.Close()
}

// errLater answers the verbs other tickets move here (#602, #603).
var errLater = errors.New("imapbackend: not implemented yet")

func (r *Receiver) Fetch(context.Context, backend.ContentRequest) (io.ReadCloser, error) {
	return nil, errLater
}

func (r *Receiver) Find(context.Context, backend.FolderRef, string, int64) ([]backend.MessageRef, error) {
	return nil, errLater
}

func (r *Receiver) Apply(context.Context, backend.Op) (backend.Result, error) {
	return backend.Result{}, errLater
}

func (r *Receiver) Watch(context.Context, func([]backend.FolderRef)) error { return errLater }

func (r *Receiver) Check(context.Context) (backend.CheckResult, error) {
	return backend.CheckResult{}, errLater
}

// Describe declares what the server's capabilities allow.
func (r *Receiver) Describe(ctx context.Context) (backend.Description, error) {
	c, err := r.client(ctx)
	if err != nil {
		return backend.Description{}, err
	}
	return describe(c.Caps()), nil
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
		// Without MOVE a move is COPY, \Deleted and UID EXPUNGE, which needs UIDPLUS.
		backend.FeatureMove:             either(caps.Has(imap.CapMove) || caps.Has(imap.CapUIDPlus), "imap.no_move"),
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
	}}
	if gmail {
		d.Identity = backend.IdentityStable
	}
	return d
}

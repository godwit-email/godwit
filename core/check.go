package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/godwit-email/godwit/backend/jmap"
	"github.com/godwit-email/godwit/backend/pop3"
	"github.com/godwit-email/godwit/core/auth"
)

// KindInvalid marks a failed settings step: the input itself is not valid.
const KindInvalid conn.FailureKind = "invalid"

const checkTimeout = 20 * time.Second

// CheckStep is one item of a connection check. Name is settings, imap (jmap
// for a JMAP account) or smtp.
// Detail comes from conn's already redacted errors: no password, token or server text.
type CheckStep struct {
	Name    string
	OK      bool
	Skipped bool
	Kind    conn.FailureKind
	Detail  string
}

// Folders is how many selectable folders the imap step listed (0 if LIST
// failed). Host is the host the receiving step signed in to, as host:port:
// for JMAP the session's, which may be another than the one typed in.
type CheckReport struct {
	Steps   []CheckStep
	Folders int
	Host    string
	desc    backend.Description // what the signed-in receiving backend declared; kept off the public report
}

// OK reports whether every step passed.
func (r CheckReport) OK() bool {
	for _, s := range r.Steps {
		if !s.OK {
			return false
		}
	}
	return len(r.Steps) > 0
}

// OnlySMTPFailed reports whether settings and imap passed and smtp failed:
// what AccountInput.ReceiveOnly lets through.
func (r CheckReport) OnlySMTPFailed() bool {
	return len(r.Steps) == 3 && r.Steps[0].OK && r.Steps[1].OK && !r.Steps[2].OK && !r.Steps[2].Skipped
}

// CheckError is returned by TestAccount and AddAccount when a check fails.
type CheckError struct {
	Report CheckReport
	err    error // the first failing step's error
}

func (e *CheckError) Error() string {
	for _, s := range e.Report.Steps {
		if !s.OK && !s.Skipped {
			return s.Name + ": " + s.Detail
		}
	}
	return "check failed"
}

func (e *CheckError) Unwrap() error { return e.err }

// CheckAccount runs the settings, imap and smtp checks in order; after a failure
// the remaining steps are Skipped.
func (s *Service) CheckAccount(ctx context.Context, in AccountInput) (CheckReport, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return CheckReport{}, err
	}
	defer end()
	if err := s.allowPOP3(ctx, &in); err != nil {
		return CheckReport{}, err
	}
	r, _ := s.check(ctx, in, "")
	return r, ctx.Err()
}

// check returns the report and the first failing step's underlying error.
// token is a stored OAuth account's access token, used instead of in.Password.
func (s *Service) check(ctx context.Context, in AccountInput, token string) (CheckReport, error) {
	r := CheckReport{Steps: []CheckStep{{Name: "settings"}, {Name: in.receiving()}, {Name: "smtp"}}}
	var first error
	fail := func(i int, kind conn.FailureKind, err error) {
		r.Steps[i].Kind, r.Steps[i].Detail, first = kind, capDetail(err.Error()), err
	}
	user, graph := in.username(), in.graph
	steps := []func() (conn.FailureKind, error){
		func() (conn.FailureKind, error) {
			if err := in.validate(); err != nil {
				return KindInvalid, err
			}
			if in.OAuthFlowID == "" {
				if in.Password == "" && token == "" {
					return KindInvalid, errors.New("password is required")
				}
				if in.Receiving.Auth == AuthAPIToken && in.SendPassword == "" {
					return KindInvalid, errors.New("the sending password is required: an API token does not sign in to SMTP")
				}
				return KindInvalid, nil
			}
			g, err := s.flowGrant(in) // peeked: AddAccount still needs it
			user, token = g.Email, g.AccessToken
			graph = func(ctx context.Context) error {
				cfg, err := oauthConfig(g.provider)
				if err != nil {
					return err
				}
				if g.client.ID != "" {
					cfg.ClientID, cfg.ClientSecret = g.client.ID, g.client.Secret
				}
				_, err = graphGrant(ctx, cfg, g.RefreshToken)
				return err
			}
			return KindInvalid, err
		},
		func() (conn.FailureKind, error) {
			cr := backend.Credentials{Username: user, Password: in.Password, Token: token}
			if in.Receiving.Auth == AuthAPIToken {
				cr = backend.Credentials{Username: user, Token: in.Password}
			}
			recv := newReceiver(in.Receiving, func(context.Context) (backend.Credentials, error) { return cr, nil })
			defer recv.Close()
			ctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			c, err := recv.Check(ctx)
			r.Folders, r.Host, r.desc = c.Folders, c.Host, c.Description
			return conn.Classify(err), err
		},
		func() (conn.FailureKind, error) {
			c, err := dialStep(ctx, func(ctx context.Context) (interface{ Close() error }, error) {
				return conn.DialSMTP(ctx, conn.Endpoint{Host: in.Sending.Host, Port: in.Sending.Port, Security: in.Sending.Security, Username: user,
					Password: cmp.Or(in.SendPassword, in.Password), AccessToken: token})
			})
			if err == nil {
				c.Close()
			}
			if errors.Is(err, conn.ErrSMTPAuthDisabled) && graph != nil {
				// The mailbox may still send through Graph: a token for it is the check.
				ctx, cancel := context.WithTimeout(ctx, checkTimeout)
				defer cancel()
				if err = graph(ctx); errors.Is(err, errNoGraph) {
					err = fmt.Errorf("smtp: %w", conn.ErrSMTPAuthDisabled)
				}
			}
			return conn.Classify(err), err
		},
	}
	for i, run := range steps {
		if first != nil {
			r.Steps[i].Skipped = true
			continue
		}
		if kind, err := run(); err != nil {
			fail(i, kind, err)
		} else {
			r.Steps[i].OK = true
		}
	}
	return r, first
}

// newReceiver is the receiving backend of srv, signing in with creds.
func newReceiver(srv Server, creds backend.CredentialsFunc) backend.Receiver {
	switch srv.Protocol {
	case auth.JMAP:
		return jmap.New(jmap.Config{SessionURL: sessionURL(srv), Credentials: creds})
	case auth.POP3:
		return pop3.New(pop3.Config{Host: srv.Host, Port: srv.Port, Security: srv.Security, Credentials: creds})
	}
	return imapbackend.New(imapbackend.Config{Host: srv.Host, Port: srv.Port, Security: srv.Security, Credentials: creds})
}

// sessionURL is a JMAP slot's session resource: Path on the host, or
// /.well-known/jmap to discover it.
func sessionURL(srv Server) string {
	u := url.URL{Scheme: "https", Host: net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)), Path: srv.Path}
	if srv.Security == SecurityNone {
		u.Scheme = "http"
	}
	if u.Path == "" {
		u.Path = "/.well-known/jmap"
	}
	return u.String()
}

func dialStep(ctx context.Context, dial func(context.Context) (interface{ Close() error }, error)) (interface{ Close() error }, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return dial(ctx)
}

const maxDetail = 200

// capDetail cuts s to maxDetail bytes on a UTF-8 boundary.
func capDetail(s string) string {
	if len(s) <= maxDetail {
		return s
	}
	s = s[:maxDetail]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

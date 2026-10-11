package core

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/mailsync"
)

// KindInvalid marks a failed settings step: the input itself is not valid.
const KindInvalid conn.FailureKind = "invalid"

const checkTimeout = 20 * time.Second

// CheckStep is one item of a connection check. Name is settings, imap or smtp.
// Detail comes from conn's already redacted errors: no password, token or server text.
type CheckStep struct {
	Name    string
	OK      bool
	Skipped bool
	Kind    conn.FailureKind
	Detail  string
}

// Folders is how many selectable folders the imap step listed (0 if LIST failed).
type CheckReport struct {
	Steps   []CheckStep
	Folders int
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
	r, _ := s.check(ctx, in, "")
	return r, ctx.Err()
}

// check returns the report and the first failing step's underlying error.
// token is a stored OAuth account's access token, used instead of in.Password.
func (s *Service) check(ctx context.Context, in AccountInput, token string) (CheckReport, error) {
	r := CheckReport{Steps: []CheckStep{{Name: "settings"}, {Name: "imap"}, {Name: "smtp"}}}
	var first error
	fail := func(i int, kind conn.FailureKind, err error) {
		r.Steps[i].Kind, r.Steps[i].Detail, first = kind, capDetail(err.Error()), err
	}
	user := in.username()
	steps := []func() (conn.FailureKind, error){
		func() (conn.FailureKind, error) {
			if err := in.validate(); err != nil {
				return KindInvalid, err
			}
			if in.OAuthFlowID == "" {
				if in.Password == "" && token == "" {
					return KindInvalid, errors.New("password is required")
				}
				return KindInvalid, nil
			}
			g, err := s.flowGrant(in) // peeked: AddAccount still needs it
			user, token = g.Email, g.AccessToken
			return KindInvalid, err
		},
		func() (conn.FailureKind, error) {
			c, err := dialStep(ctx, func(ctx context.Context) (interface{ Close() error }, error) {
				c, abort, err := conn.DialIMAPAbortable(ctx, conn.Endpoint{Host: in.IMAPHost, Port: in.IMAPPort, Security: in.IMAPSecurity, Username: user, Password: in.Password, AccessToken: token}, nil)
				if err == nil {
					stop := context.AfterFunc(ctx, abort)   // a stalled LIST ends with the step's timeout
					r.Folders, _ = mailsync.CountFolders(c) // a failing LIST does not fail the sign-in
					stop()
				}
				return c, err
			})
			if err == nil {
				c.Close()
			}
			return conn.Classify(err), err
		},
		func() (conn.FailureKind, error) {
			c, err := dialStep(ctx, func(ctx context.Context) (interface{ Close() error }, error) {
				return conn.DialSMTP(ctx, conn.Endpoint{Host: in.SMTPHost, Port: in.SMTPPort, Security: in.SMTPSecurity, Username: user, Password: in.Password, AccessToken: token})
			})
			if err == nil {
				c.Close()
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

// Package smtpbackend is the SMTP sending backend (ADR 0037).
package smtpbackend

import (
	"context"
	"errors"
	"fmt"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// Config is how to reach an account's SMTP server.
type Config struct {
	Host        string
	Port        int
	Security    conn.Security
	Credentials backend.CredentialsFunc
}

// Sender submits over SMTP, one connection per message. It does not save a
// sent copy: the outbox has the Receiver append one.
type Sender struct{ cfg Config }

var _ backend.Sender = (*Sender)(nil)

// New is a Sender on the server cfg reaches.
func New(cfg Config) *Sender { return &Sender{cfg} }

func (*Sender) SavesSent() bool { return false }

// rejected is err as a *backend.RejectedError at stage if it is the server's
// reply, otherwise err.
func rejected(err error, stage, rcpt string) error {
	var se *smtp.SMTPError
	if !errors.As(err, &se) {
		return err
	}
	re := &backend.RejectedError{Stage: stage, Recipient: rcpt, Code: se.Code, Message: se.Message}
	if c := se.EnhancedCode; c[0] > 0 {
		re.Enhanced = fmt.Sprintf("%d.%d.%d", c[0], c[1], c[2])
	}
	return re
}

// Send submits raw to rcpts, stopping at the first refused recipient. A
// mailbox whose SMTP AUTH is off is conn.ErrSMTPAuthDisabled (an
// ErrAuthFailed). Cancelling ctx closes the connection.
func (s *Sender) Send(ctx context.Context, from string, rcpts []string, raw []byte) error {
	cr, err := s.cfg.Credentials(ctx)
	if err != nil {
		return err
	}
	c, err := conn.DialSMTP(ctx, conn.Endpoint{Host: s.cfg.Host, Port: s.cfg.Port, Security: s.cfg.Security,
		Username: cr.Username, Password: cr.Password, AccessToken: cr.Token})
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err := c.Mail(from, nil); err != nil {
		return rejected(err, "MAIL", "")
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r, nil); err != nil {
			return rejected(err, "RCPT", r)
		}
	}
	w, err := c.Data()
	if err != nil {
		return rejected(err, "DATA", "")
	}
	if _, err := w.Write(raw); err != nil {
		return err // the terminating dot was not sent
	}
	if err := w.Close(); err != nil {
		if err = rejected(err, "DATA", ""); errors.As(err, new(*backend.RejectedError)) {
			return err // the server answered
		}
		return fmt.Errorf("%w: %v", backend.ErrUncertain, err)
	}
	c.Quit()
	return nil
}

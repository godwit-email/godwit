// Package send submits messages over SMTP.
package send

import (
	"context"
	"errors"
	"fmt"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/core/conn"
)

// ErrUncertain means the whole message reached the server but its verdict did
// not come back: it may have been accepted. Retrying could deliver it twice.
var ErrUncertain = errors.New("send: the server may have accepted the message")

// Send submits raw to rcpts. An error that is not ErrUncertain happened before
// the server could accept the message, so retrying is safe. Cancelling ctx closes
// the connection.
func Send(ctx context.Context, ep conn.Endpoint, from string, rcpts []string, raw []byte) error {
	c, err := conn.DialSMTP(ctx, ep)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r, nil); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err // the terminating dot was not sent
	}
	if err := w.Close(); err != nil {
		var se *smtp.SMTPError
		if errors.As(err, &se) {
			return err // the server answered: rejected
		}
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	c.Quit()
	return nil
}

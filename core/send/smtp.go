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

// RejectedError is the server's refusal of the message at MAIL FROM, at a
// RCPT TO or at the end of DATA. The server has not accepted the message.
type RejectedError struct {
	// Stage is the refused command: "MAIL", "RCPT" or "DATA".
	Stage string
	// Recipient is the refused address at RCPT, "" at the other stages.
	Recipient string
	// Code is the reply code, such as 550: 4xx refuses for now, 5xx for good.
	Code int
	// Enhanced is the enhanced status code, such as "5.1.1"; "" if the server sent none.
	Enhanced string
	// Message is the server's reply text as it sent it.
	Message string
}

func (e *RejectedError) Error() string {
	at := e.Stage
	if e.Recipient != "" {
		at += " " + e.Recipient
	}
	return fmt.Sprintf("send: the server refused %s: %d %s %s", at, e.Code, e.Enhanced, e.Message)
}

// rejected is err as a *RejectedError at stage if it is the server's reply,
// otherwise err.
func rejected(err error, stage, rcpt string) error {
	var se *smtp.SMTPError
	if !errors.As(err, &se) {
		return err
	}
	re := &RejectedError{Stage: stage, Recipient: rcpt, Code: se.Code, Message: se.Message}
	if c := se.EnhancedCode; c[0] > 0 {
		re.Enhanced = fmt.Sprintf("%d.%d.%d", c[0], c[1], c[2])
	}
	return re
}

// Send submits raw to rcpts, stopping at the first refused recipient. An error
// that is not ErrUncertain happened before the server could accept the message,
// so retrying is safe; a *RejectedError is the server's refusal. Cancelling ctx
// closes the connection.
func Send(ctx context.Context, ep conn.Endpoint, from string, rcpts []string, raw []byte) error {
	c, err := conn.DialSMTP(ctx, ep)
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
		if err = rejected(err, "DATA", ""); errors.As(err, new(*RejectedError)) {
			return err // the server answered
		}
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	c.Quit()
	return nil
}

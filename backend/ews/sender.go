package ews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptrace"
	"net/mail"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// sendTimeout bounds one send: a server that stops answering must not hold
// the outbox (and the sync after it) forever. Running out once the request is
// written is ErrUncertain, like any lost answer.
const sendTimeout = 5 * time.Minute

// Sender is an EWS account's sending backend: CreateItem with the MIME
// message and SendAndSaveCopy, so Exchange files the sent copy in Sent Items.
type Sender struct{ c *client }

var _ backend.Sender = (*Sender)(nil)

// NewSender is a Sender for the account cfg names.
func NewSender(cfg Config) *Sender { return &Sender{newClient(cfg)} }

// SavesSent implements backend.Sender: Exchange files what it sends in Sent Items.
func (*Sender) SavesSent() bool { return true }

// Send implements backend.Sender. Exchange takes the recipients from the
// MIME's To and Cc; rcpts they do not name go as BccRecipients, so they are
// never written into the message. A request written whole without an answer
// is ErrUncertain; the server refusing the message is a *RejectedError;
// ErrorServerBusy, a SOAP fault or an HTTP error is a plain error, tried again.
func (s *Sender) Send(ctx context.Context, _ string, rcpts []string, raw []byte) error {
	defer s.c.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	var wrote atomic.Bool // the last try's request went out whole; set on the transport's goroutine
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { wrote.Store(false) },
		WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) }})
	err := s.c.sendMIME(ctx, raw, bccOf(raw, rcpts))
	var re *ResponseError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &re) && re.Code != "ErrorServerBusy":
		return &backend.RejectedError{Stage: "CreateItem", Code: 554, Message: re.Code + ": " + re.Text}
	case wrote.Load() && lostAnswer(err):
		return fmt.Errorf("%w: %v", backend.ErrUncertain, err)
	}
	return err
}

// lostAnswer reports that err is the connection failing or timing out
// rather than the server answering.
func lostAnswer(err error) bool {
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errNoVerdict)
}

// bccOf is the rcpts raw's To and Cc do not name.
func bccOf(raw []byte, rcpts []string) []string {
	seen := map[string]bool{}
	if m, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil { // Build's output parses
		for _, h := range []string{"To", "Cc"} {
			list, _ := m.Header.AddressList(h)
			for _, a := range list {
				seen[strings.ToLower(a.Address)] = true
			}
		}
	}
	var bcc []string
	for _, r := range rcpts {
		if !seen[strings.ToLower(r)] {
			bcc = append(bcc, r)
		}
	}
	return bcc
}

// Package graph is the Microsoft Graph receiving and sending backend (ADR 0037).
package graph

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/mail"
	"strings"
	"sync/atomic"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// SendURL is Graph's sendMail.
const SendURL = "https://graph.microsoft.com/v1.0/me/sendMail"

// graphMaxBody is Graph's 4 MB request limit, which the base64 body must fit.
const graphMaxBody = 4 << 20

// graphClient is the receiving backend's kind of client. A Graph or proxy
// that stops answering must not hold the outbox (and the sync after it)
// forever: a timeout once the request is written is ErrUncertain, like any
// lost answer.
var graphClient = conn.NewHTTPClient(conn.HTTPTransport(), nil)

// Sender submits through Graph's sendMail with the Token its credentials
// give. Graph files the sent copy itself (saveToSentItems).
type Sender struct {
	url   string
	creds backend.CredentialsFunc
}

var _ backend.Sender = (*Sender)(nil)

// New is a Sender posting to url, normally SendURL.
func New(url string, creds backend.CredentialsFunc) *Sender { return &Sender{url, creds} }

func (*Sender) SavesSent() bool { return true }

// Send submits raw to rcpts: ErrUncertain once the whole request went out
// without an answer, backend.ErrAuthFailed for a refused token, a
// *backend.RejectedError (5xx) for a refused message. Graph takes recipients
// from the headers, so rcpts not in To or Cc are added as Bcc to the copy
// Graph gets (Exchange strips it); from is the mailbox's own.
func (s *Sender) Send(ctx context.Context, _ string, rcpts []string, raw []byte) error {
	body := base64.StdEncoding.EncodeToString(withBcc(raw, rcpts))
	if len(body) > graphMaxBody {
		return &backend.RejectedError{Stage: "DATA", Code: 552, Enhanced: "5.3.4",
			Message: "the message is over the 4 MB Microsoft Graph sends"}
	}
	cr, err := s.creds(ctx)
	if err != nil {
		return err
	}
	var wrote atomic.Bool // set on the transport's goroutine
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cr.Token)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Prefer", `IdType="ImmutableId"`)
	resp, err := graphClient.Do(req)
	if err != nil {
		if wrote.Load() {
			return fmt.Errorf("%w: %v", backend.ErrUncertain, err)
		}
		return fmt.Errorf("graph send: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	switch c := resp.StatusCode; {
	case c == http.StatusAccepted:
		return nil
	case c == http.StatusUnauthorized, c == http.StatusForbidden: // no Mail.Send consent, or a stale token
		return fmt.Errorf("graph: %w", backend.ErrAuthFailed)
	case c == http.StatusTooManyRequests, c == http.StatusServiceUnavailable: // throttled before it was taken; Graph says retry
		return fmt.Errorf("graph send: the server answered %d", c)
	case c >= 500: // it reached Graph, which may have sent it (ADR 0006)
		return fmt.Errorf("%w: graph answered %d", backend.ErrUncertain, c)
	default:
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		json.Unmarshal(b, &e)
		return &backend.RejectedError{Stage: "DATA", Code: 554, Message: fmt.Sprintf("%d %s: %s", c, e.Error.Code, e.Error.Message)}
	}
}

// withBcc is raw with a Bcc header naming the rcpts its To and Cc do not.
func withBcc(raw []byte, rcpts []string) []byte {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return raw // Build's output parses; Graph refuses what does not
	}
	seen := map[string]bool{}
	for _, h := range []string{"To", "Cc"} {
		list, _ := m.Header.AddressList(h)
		for _, a := range list {
			seen[strings.ToLower(a.Address)] = true
		}
	}
	var bcc []string
	for _, r := range rcpts {
		if !seen[strings.ToLower(r)] {
			bcc = append(bcc, (&mail.Address{Address: r}).String()) // quotes a local part that needs it
		}
	}
	if len(bcc) == 0 {
		return raw
	}
	// One address per folded line: no line nears RFC 5322's 998 characters.
	return append([]byte("Bcc: "+strings.Join(bcc, ",\r\n ")+"\r\n"), raw...)
}

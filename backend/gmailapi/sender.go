package gmailapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// Sender submits mail through Gmail REST API users.messages.send. Gmail
// automatically stores the sent copy with the SENT label (SavesSent == true).
type Sender struct{ c *client }

var _ backend.Sender = (*Sender)(nil)

// NewSender returns a Sender for the Gmail account cfg reaches.
func NewSender(cfg Config) *Sender { return &Sender{c: newClient(cfg)} }

// SavesSent implements backend.Sender.
func (*Sender) SavesSent() bool { return true }

// Send implements backend.Sender. Gmail extracts recipients from the MIME
// headers, so rcpts absent from To and Cc are prepended as Bcc (Gmail strips
// Bcc before delivering to other recipients).
func (s *Sender) Send(ctx context.Context, _ string, rcpts []string, raw []byte) error {
	defer s.c.http.CloseIdleConnections()
	if s.c.cfg.Credentials == nil {
		return fmt.Errorf("gmailapi: %w", backend.ErrAuthFailed)
	}
	cr, err := s.c.cfg.Credentials(ctx)
	if err != nil {
		return err
	}
	if cr.Token == "" {
		return fmt.Errorf("gmailapi: %w", backend.ErrAuthFailed)
	}
	payload, err := json.Marshal(struct {
		Raw string `json:"raw"`
	}{Raw: base64.RawURLEncoding.EncodeToString(withBcc(raw, rcpts))})
	if err != nil {
		return err
	}
	u := s.c.baseURL + "/gmail/v1/users/me/messages/send"

	return conn.Serial(ctx, s.c.accountKey(cr), s.c.cfg.Wait, func() error {
		var wrote atomic.Bool
		reqCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GetConn:      func(string) { wrote.Store(false) },
			WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) },
		})
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, u, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+cr.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.c.http.Do(req)
		if err != nil {
			if wrote.Load() {
				return fmt.Errorf("%w: %v", backend.ErrUncertain, err)
			}
			return fmt.Errorf("gmailapi send: %w", err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil // accepted, whether or not the rest of the answer arrived
		}
		// Google answers 5xx after the whole request reached it, and nothing
		// says the message was not sent: resending could deliver it twice (ADR 0006).
		if rerr != nil || resp.StatusCode >= 500 {
			return fmt.Errorf("%w: HTTP %s", backend.ErrUncertain, resp.Status)
		}
		perr := parseError(resp, body)
		var busy *conn.BusyError
		if errors.Is(perr, backend.ErrAuthFailed) || errors.As(perr, &busy) {
			return perr // conn.Serial waits a busy answer out
		}
		var ae *apiError
		code := 554
		msg := perr.Error()
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			code = 552
		}
		if errors.As(perr, &ae) && ae.Message != "" {
			msg = fmt.Sprintf("%d %s: %s", ae.StatusCode, cmp.Or(ae.Reason, ae.Status), ae.Message)
		}
		return &backend.RejectedError{Stage: "messages.send", Code: code, Message: msg}
	})
}

func withBcc(raw []byte, rcpts []string) []byte {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	seen := map[string]bool{}
	for _, h := range []string{"To", "Cc", "Bcc"} {
		list, _ := m.Header.AddressList(h)
		for _, a := range list {
			seen[strings.ToLower(a.Address)] = true
		}
	}
	var bcc []string
	for _, r := range rcpts {
		if !seen[strings.ToLower(r)] {
			bcc = append(bcc, (&mail.Address{Address: r}).String())
		}
	}
	if len(bcc) == 0 {
		return raw
	}
	return append([]byte("Bcc: "+strings.Join(bcc, ",\r\n ")+"\r\n"), raw...)
}

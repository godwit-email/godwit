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
	"time"

	"github.com/godwit-email/godwit/backend"
)

const sendTimeout = 5 * time.Minute

// Sender submits mail through Gmail REST API users.messages.send. Gmail
// automatically stores the sent copy with the SENT label (SavesSent == true).
type Sender struct{ c *client }

var _ backend.Sender = (*Sender)(nil)

// NewSender returns a Sender for the Gmail account cfg reaches.
func NewSender(cfg Config) *Sender { return &Sender{c: newClient(cfg, sendTimeout)} }

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
	release, err := acquire(ctx, s.c.accountKey(cr))
	if err != nil {
		return err
	}
	defer release()

	payload, err := json.Marshal(struct {
		Raw string `json:"raw"`
	}{Raw: base64.RawURLEncoding.EncodeToString(withBcc(raw, rcpts))})
	if err != nil {
		return err
	}
	u := s.c.baseURL + "/gmail/v1/users/me/messages/send"

	for try := 0; ; try++ {
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
		if rerr != nil {
			return fmt.Errorf("%w: %v", backend.ErrUncertain, rerr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout {
			return fmt.Errorf("%w: HTTP %s", backend.ErrUncertain, resp.Status)
		}
		perr := parseError(resp, body)
		if errors.Is(perr, backend.ErrAuthFailed) {
			return perr
		}
		var rl *rateLimitError
		if errors.As(perr, &rl) {
			if try == maxRetries {
				return perr
			}
			d := rl.backOff
			if d <= 0 {
				d = min(defaultBackOff<<try, maxBackOff)
			}
			if err := s.c.wait(ctx, d); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 500 {
			return perr
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
	}
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

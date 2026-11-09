package send

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
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/core/conn"
)

// GraphHost marks an Endpoint that sends through Microsoft Graph with its
// AccessToken, for a mailbox whose SMTP AUTH is disabled.
const GraphHost = "graph.microsoft.com"

// GraphURL is Graph's sendMail; tests replace it.
var GraphURL = "https://" + GraphHost + "/v1.0/me/sendMail"

// graphMaxBody is Graph's 4 MB request limit, which the base64 body must fit.
const graphMaxBody = 4 << 20

// graphClient goes through the proxy mail goes through. A Graph or proxy that
// stops answering must not hold the outbox (and the sync after it) forever: a
// timeout once the request is written is ErrUncertain, like any lost answer.
var graphClient = &http.Client{Timeout: 5 * time.Minute, Transport: func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) { return conn.SystemProxy(r.URL), nil }
	t.ResponseHeaderTimeout = time.Minute
	return t
}()}

// SendGraph submits raw to rcpts through Graph's sendMail, with the same
// errors as Send: ErrUncertain once the whole request went out without an
// answer, conn.ErrAuthFailed for a refused token, a *RejectedError (5xx) for a
// refused message. Graph takes recipients from the headers, so rcpts not in
// To or Cc are added as Bcc to the copy Graph gets (Exchange strips it).
func SendGraph(ctx context.Context, token string, rcpts []string, raw []byte) error {
	body := base64.StdEncoding.EncodeToString(withBcc(raw, rcpts))
	if len(body) > graphMaxBody {
		return &RejectedError{Stage: "DATA", Code: 552, Enhanced: "5.3.4",
			Message: "the message is over the 4 MB Microsoft Graph sends"}
	}
	var wrote atomic.Bool // set on the transport's goroutine
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GraphURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := graphClient.Do(req)
	if err != nil {
		if wrote.Load() {
			return fmt.Errorf("%w: %v", ErrUncertain, err)
		}
		return fmt.Errorf("graph send: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	switch c := resp.StatusCode; {
	case c == http.StatusAccepted:
		return nil
	case c == http.StatusUnauthorized, c == http.StatusForbidden: // no Mail.Send consent, or a stale token
		return fmt.Errorf("graph: %w", conn.ErrAuthFailed)
	case c == http.StatusGatewayTimeout: // the backend may have taken it
		return fmt.Errorf("%w: graph answered %d", ErrUncertain, c)
	case c == http.StatusTooManyRequests, c >= 500:
		return fmt.Errorf("graph send: the server answered %d", c)
	default:
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		json.Unmarshal(b, &e)
		return &RejectedError{Stage: "DATA", Code: 554, Message: fmt.Sprintf("%d %s: %s", c, e.Error.Code, e.Error.Message)}
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
			bcc = append(bcc, r)
		}
	}
	if len(bcc) == 0 {
		return raw
	}
	return append([]byte("Bcc: "+strings.Join(bcc, ", ")+"\r\n"), raw...)
}

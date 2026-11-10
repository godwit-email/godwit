// Package ews is the EWS protocol backend (Exchange Web Services, on-premises
// Exchange 2016 and later; ADR 0037): a backend.Receiver over an account's
// EWS URL, through the thin SOAP layer in this package.
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// Config is how to reach an account.
type Config struct {
	// URL is the EWS endpoint, such as https://mail.corp.example/EWS/Exchange.asmx.
	URL string
	// Address is the account's email address, the mailbox a subscription
	// anchors on (X-AnchorMailbox).
	Address     string
	Credentials backend.CredentialsFunc
	// Wait waits d out, or until ctx is done, when the server asks to back
	// off. Nil waits in real time; tests pass a fake.
	Wait func(ctx context.Context, d time.Duration) error
}

// client speaks SOAP to the account's EWS URL. It follows a redirect only
// to the same host, so the credentials stay there.
type client struct {
	cfg   Config
	http  *http.Client
	plain *http.Transport // under http's, signing in no further

	mu      sync.Mutex
	version *serverVersion // from the last response that had one
}

var errOtherHost = errors.New("ews: refused a redirect to another host")

func newClient(cfg Config) *client {
	t := transport()
	return &client{cfg: cfg, plain: t, http: &http.Client{Transport: signer{t}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		switch {
		case len(via) >= 10:
			return errors.New("ews: too many redirects")
		case req.URL.Host != via[0].URL.Host:
			return errOtherHost
		}
		return nil
	}}}
}

// host is where the client talks to, as host:port when the URL has a port.
func (c *client) host() string {
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

// Throttling (spec 限流): an ErrorServerBusy answer is waited out and the
// request sent again, at most maxRetries times.
const (
	maxRetries     = 3
	maxBackOff     = time.Minute
	defaultBackOff = 5 * time.Second // when the server names no BackOffMilliseconds
)

// call sends op, a request type of this package, and returns the body's
// response element read as R. 401 is ErrAuthFailed; a SOAP fault is a *fault; ErrorServerBusy, once the retries are used up, is
// a *busyError.
// Calls for one account go one at a time, waits included, so they stay
// under the server's concurrency limit and stop while it is busy.
func call[R any](ctx context.Context, c *client, op any) (R, error) {
	var zero R
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return zero, err
	}
	release, err := acquire(ctx, c.account(cr))
	if err != nil {
		return zero, err
	}
	defer release()
	for try := 0; ; try++ {
		r, err := send[R](ctx, c, op, cr)
		var busy *busyError
		if !errors.As(err, &busy) || try == maxRetries {
			return r, err
		}
		if err := c.wait(ctx, busy.backOff); err != nil {
			return r, err
		}
	}
}

func (c *client) wait(ctx context.Context, d time.Duration) error {
	if c.cfg.Wait != nil {
		return c.cfg.Wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// accounts holds a one-slot channel per account (EWS URL and user name),
// taken for the length of a call, and how many calls hold it or wait for it:
// an account's entry goes once none does. The push connection does not take it.
var accounts = struct {
	sync.Mutex
	m map[string]*slot
}{m: map[string]*slot{}}

type slot struct {
	ch    chan struct{}
	users int
}

func acquire(ctx context.Context, account string) (release func(), err error) {
	accounts.Lock()
	s := accounts.m[account]
	if s == nil {
		s = &slot{ch: make(chan struct{}, 1)}
		accounts.m[account] = s
	}
	s.users++
	accounts.Unlock()
	done := func() {
		accounts.Lock()
		if s.users--; s.users == 0 {
			delete(accounts.m, account)
		}
		accounts.Unlock()
	}
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch; done() }, nil
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
}

// account is the key of the account c signs in to as cr: its EWS URL and user name.
func (c *client) account(cr backend.Credentials) string { return c.cfg.URL + "\x00" + cr.Username }

// post sends op signed in as cr, with the account's affinity, and returns
// the response, its body unread: 200, or 500 or 503, which carry a fault or
// throttling. 401 is ErrAuthFailed, any other status an error.
func (c *client) post(ctx context.Context, op any, cr backend.Credentials) (*http.Response, error) {
	body, err := xml.Marshal(newRequest(op))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(append([]byte(xml.Header), body...)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.SetBasicAuth(cr.Username, cr.Password)
	a := c.affinity(c.account(cr))
	a.route(req, op, c.cfg.Address)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusInternalServerError, http.StatusServiceUnavailable:
		a.keep(resp)
		return resp, nil
	case http.StatusUnauthorized:
		err = errAuthFailed
	default:
		err = fmt.Errorf("ews: HTTP %s", resp.Status)
	}
	drain(resp)
	return nil, err
}

// send is one try of call.
func send[R any](ctx context.Context, c *client, op any, cr backend.Credentials) (R, error) {
	var zero R
	resp, err := c.post(ctx, op, cr)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}
	if resp.StatusCode != http.StatusOK {
		if err := serverBusy(raw); err != nil {
			return zero, err
		}
	}
	var env responseEnvelope[R]
	if err := xml.Unmarshal(raw, &env); err != nil {
		return zero, fmt.Errorf("ews: response (HTTP %s): %w", resp.Status, err)
	}
	if v := env.Header.Version; v != nil {
		c.mu.Lock()
		c.version = v
		c.mu.Unlock()
	}
	if env.Body.Fault != nil {
		return zero, env.Body.Fault
	}
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("ews: HTTP %s", resp.Status)
	}
	return env.Body.Response, nil
}

// busyError is the server's ErrorServerBusy, asking to wait backOff.
type busyError struct{ backOff time.Duration }

func (e *busyError) Error() string {
	return "ews: the server is busy (ErrorServerBusy), try again later"
}

// serverBusy is a *busyError when raw, a response body, is ErrorServerBusy:
// a SOAP fault or a response message, its BackOffMilliseconds in a Value
// (capped at maxBackOff). Otherwise nil.
func serverBusy(raw []byte) error {
	d := xml.NewDecoder(bytes.NewReader(raw))
	busy, backOff := false, defaultBackOff
	var cur string
	var isBackOff bool
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch el := tok.(type) {
		case xml.StartElement:
			cur, isBackOff = el.Name.Local, false
			for _, a := range el.Attr {
				isBackOff = isBackOff || cur == "Value" && a.Name.Local == "Name" && a.Value == "BackOffMilliseconds"
			}
		case xml.CharData:
			t := strings.TrimSpace(string(el))
			switch {
			case cur == "ResponseCode" && t == "ErrorServerBusy", cur == "faultcode" && strings.HasSuffix(t, "ErrorServerBusy"):
				busy = true
			case isBackOff:
				if ms, err := strconv.Atoi(t); err == nil {
					backOff = time.Duration(ms) * time.Millisecond
				}
			}
		case xml.EndElement:
			cur, isBackOff = "", false
		}
	}
	if !busy {
		return nil
	}
	return &busyError{min(backOff, maxBackOff)}
}

// serverVersion is the version the server last said it is; nil before a response.
func (c *client) serverVersion() *serverVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

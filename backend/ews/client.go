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
	"github.com/godwit-email/godwit/backend/conn"
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
// as conn.SameHost allows, so the credentials stay there.
type client struct {
	cfg  Config
	http *http.Client
	// streaming is http without the overall timeout, for
	// GetStreamingEvents, which its own idle timer bounds instead.
	streaming *http.Client
	plain     *http.Transport // under http's, signing in no further

	mu      sync.Mutex
	version *serverVersion // from the last response that had one
}

func newClient(cfg Config) *client {
	t := transport()
	hc := conn.NewHTTPClient(t, signer{t})
	return &client{cfg: cfg, plain: t, http: hc, streaming: conn.Streaming(hc)}
}

// host is where the client talks to, as host:port when the URL has a port.
func (c *client) host() string {
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

// call sends op, a request type of this package, and returns the body's
// response element read as R. 401 is ErrAuthFailed; a SOAP fault is a *fault; ErrorServerBusy, once the retries are used up, is
// a *conn.BusyError.
// Calls for one account go one at a time (conn.Serial), the push connection
// aside.
func call[R any](ctx context.Context, c *client, op any) (R, error) {
	var r R
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return r, err
	}
	err = conn.Serial(ctx, c.account(cr), c.cfg.Wait, func() (err error) {
		r, err = send[R](ctx, c, op, cr)
		return err
	})
	return r, err
}

// account is the key of the account c signs in to as cr: its EWS URL and user name.
func (c *client) account(cr backend.Credentials) string { return c.cfg.URL + "\x00" + cr.Username }

// post sends op on hc signed in as cr, with the account's affinity, and
// returns the response, its body unread: 200, or 500 or 503, which carry a
// fault or throttling. 502 and 504, a proxy that gave up waiting, are
// errUnreadable (no verdict); any other status conn.HTTPStatusErr's error
// or a plain one.
func (c *client) post(ctx context.Context, hc *http.Client, op any, cr backend.Credentials) (*http.Response, error) {
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
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusInternalServerError, http.StatusServiceUnavailable:
		a.keep(resp)
		return resp, nil
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		// a proxy gave up on Exchange, which may still have run op
		err = fmt.Errorf("%w (HTTP %s)", errUnreadable, resp.Status)
	default:
		if err = conn.HTTPStatusErr(resp); err != nil {
			err = fmt.Errorf("ews: %w", err)
		} else {
			err = fmt.Errorf("ews: HTTP %s", resp.Status)
		}
	}
	drain(resp)
	return nil, err
}

// send is one try of call.
func send[R any](ctx context.Context, c *client, op any, cr backend.Credentials) (R, error) {
	var zero R
	resp, err := c.post(ctx, c.http, op, cr)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, fmt.Errorf("%w: %w", errUnreadable, err)
	}
	if resp.StatusCode != http.StatusOK {
		if err := serverBusy(raw); err != nil {
			return zero, err
		}
	}
	var env responseEnvelope[R]
	if err := xml.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode == http.StatusServiceUnavailable { // the server did not take the request
			return zero, fmt.Errorf("ews: response (HTTP %s): %w", resp.Status, err)
		}
		return zero, fmt.Errorf("%w (HTTP %s): %w", errUnreadable, resp.Status, err)
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

// errUnreadable is a response whose body could not be read or decoded: no
// verdict, so a request that may have taken effect did so unseen.
var errUnreadable = errors.New("ews: unreadable response")

var errServerBusy = errors.New("ews: the server is busy (ErrorServerBusy), try again later")

// serverBusy is a *conn.BusyError when raw, a response body, is
// ErrorServerBusy: a SOAP fault or a response message, its
// BackOffMilliseconds in a Value. Otherwise nil.
func serverBusy(raw []byte) error {
	d := xml.NewDecoder(bytes.NewReader(raw))
	busy, backOff := false, time.Duration(0)
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
	return &conn.BusyError{BackOff: backOff, Err: errServerBusy}
}

// serverVersion is the version the server last said it is; nil before a response.
func (c *client) serverVersion() *serverVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

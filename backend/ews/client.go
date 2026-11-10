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
	"sync"

	"github.com/Azure/go-ntlmssp"

	"github.com/godwit-email/godwit/backend"
)

// Config is how to reach an account.
type Config struct {
	// URL is the EWS endpoint, such as https://mail.corp.example/EWS/Exchange.asmx.
	URL         string
	Credentials backend.CredentialsFunc
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
	return &client{cfg: cfg, plain: t, http: &http.Client{Transport: negotiator{ntlmssp.Negotiator{RoundTripper: t, AllowBasicAuth: true}}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
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

// call sends op, a request type of this package, and returns the body's
// response element read as R. 401 is ErrAuthFailed (or ErrExtendedProtection);
// a SOAP fault is a *fault.
func call[R any](ctx context.Context, c *client, op any) (R, error) {
	var zero R
	body, err := xml.Marshal(newRequest(op))
	if err != nil {
		return zero, err
	}
	body = append([]byte(xml.Header), body...)
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.SetBasicAuth(cr.Username, cr.Password)
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusInternalServerError: // a fault comes with 500
	case http.StatusUnauthorized:
		return zero, c.refused(ctx, resp, body, cr)
	default:
		return zero, fmt.Errorf("ews: HTTP %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
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

// serverVersion is the version the server last said it is; nil before a response.
func (c *client) serverVersion() *serverVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

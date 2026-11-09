// Package jmap is the JMAP protocol backend (RFC 8620, 8621; ADR 0037): a
// backend.Receiver over an account's JMAP session.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
	_ "git.sr.ht/~rockorager/go-jmap/mail/email"   // registers the Email methods
	_ "git.sr.ht/~rockorager/go-jmap/mail/mailbox" // registers the Mailbox methods

	"github.com/godwit-email/godwit/backend"
)

// Config is how to reach an account.
type Config struct {
	// SessionURL is the session resource, or /.well-known/jmap to discover it.
	SessionURL  string
	Credentials backend.CredentialsFunc
}

// client speaks JMAP over HTTP. It signs every request with the account's
// credentials and trusts the hosts the session names; it follows a redirect
// only to the same host, except from /.well-known/jmap.
type client struct {
	cfg  Config
	http *http.Client

	mu      sync.Mutex
	session *gojmap.Session
	host    string // where the session came from
}

var errOtherHost = errors.New("jmap: refused a redirect to another host")

func newClient(cfg Config) *client {
	c := &client{cfg: cfg}
	c.http = &http.Client{Transport: authTransport{cfg.Credentials}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		switch {
		case len(via) >= 10:
			return errors.New("jmap: too many redirects")
		case strings.HasSuffix(via[0].URL.Path, "/.well-known/jmap"):
			return nil
		case req.URL.Host != via[0].URL.Host:
			return errOtherHost
		}
		return nil
	}}
	return c
}

type authTransport struct{ creds backend.CredentialsFunc }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cr, err := a.creds(r.Context())
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	if cr.Token != "" {
		r.Header.Set("Authorization", "Bearer "+cr.Token)
	} else {
		r.SetBasicAuth(cr.Username, cr.Password)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// do does an HTTP request and checks its status: 401 is ErrAuthFailed, 404
// ErrNotFound.
func (c *client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("jmap: %s: %w", req.URL.Path, backend.ErrAuthFailed)
	case http.StatusNotFound:
		return nil, fmt.Errorf("jmap: %s: %w", req.URL.Path, backend.ErrNotFound)
	}
	return nil, fmt.Errorf("jmap: %s: HTTP %s", req.URL.Path, resp.Status)
}

// sess is the session, fetched when there is none.
func (c *client) sess(ctx context.Context) (*gojmap.Session, gojmap.ID, error) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	if s == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.SessionURL, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := c.do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		s = new(gojmap.Session)
		if err := json.NewDecoder(resp.Body).Decode(s); err != nil {
			return nil, "", fmt.Errorf("jmap: session: %w", err)
		}
		c.mu.Lock()
		c.session, c.host = s, resp.Request.URL.Host
		c.mu.Unlock()
	}
	acct := s.PrimaryAccounts[mail.URI]
	if acct == "" {
		return nil, "", errors.New("jmap: the session has no mail account")
	}
	return s, acct, nil
}

// methodError is a JMAP method-level error, such as cannotCalculateChanges.
type methodError struct{ *gojmap.MethodError }

func (e methodError) Error() string { return "jmap: " + e.Type }

func isMethodError(err error, typ string) bool {
	var me methodError
	return errors.As(err, &me) && me.Type == typ
}

// call sends one request of the methods build gives for the account and
// returns each method's response arguments, such as *email.GetResponse. A
// method error is returned as methodError.
func (c *client) call(ctx context.Context, build func(acct gojmap.ID) []gojmap.Method) ([]any, error) {
	s, acct, err := c.sess(ctx)
	if err != nil {
		return nil, err
	}
	methods := build(acct)
	req := &gojmap.Request{Using: []gojmap.URI{gojmap.CoreURI, mail.URI}}
	for _, m := range methods {
		req.Invoke(m)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.APIURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hresp, err := c.do(hreq)
	if err != nil {
		return nil, err
	}
	defer hresp.Body.Close()
	var resp gojmap.Response
	if err := json.NewDecoder(hresp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("jmap: response: %w", err)
	}
	if resp.SessionState != s.State {
		c.mu.Lock()
		c.session = nil // fetched again next time
		c.mu.Unlock()
	}
	if len(resp.Responses) != len(methods) {
		return nil, fmt.Errorf("jmap: %d responses to %d calls", len(resp.Responses), len(methods))
	}
	out := make([]any, len(methods))
	for i, inv := range resp.Responses {
		if me, ok := inv.Args.(*gojmap.MethodError); ok {
			return nil, methodError{me}
		}
		out[i] = inv.Args
	}
	return out, nil
}

// download reads the blob blobID.
func (c *client) download(ctx context.Context, blobID gojmap.ID) (io.ReadCloser, error) {
	s, acct, err := c.sess(ctx)
	if err != nil {
		return nil, err
	}
	u := strings.NewReplacer(
		"{accountId}", url.PathEscape(string(acct)),
		"{blobId}", url.PathEscape(string(blobID)),
		"{name}", "message.eml",
		"{type}", url.QueryEscape("message/rfc822"),
	).Replace(s.DownloadURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

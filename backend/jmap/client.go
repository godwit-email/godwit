// Package jmap is the JMAP protocol backend (RFC 8620, 8621; ADR 0037): a
// backend.Receiver and backend.Sender over an account's JMAP session.
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
	"time"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
	_ "git.sr.ht/~rockorager/go-jmap/mail/email"         // registers the Email methods
	"git.sr.ht/~rockorager/go-jmap/mail/emailsubmission" // registers the EmailSubmission methods
	_ "git.sr.ht/~rockorager/go-jmap/mail/identity"      // registers the Identity methods
	_ "git.sr.ht/~rockorager/go-jmap/mail/mailbox"       // registers the Mailbox methods

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// Config is how to reach an account.
type Config struct {
	// SessionURL is the session resource, or /.well-known/jmap to discover it.
	SessionURL  string
	Credentials backend.CredentialsFunc
	// SentFolder, when non-nil, overrides which mailbox Sender files the sent
	// copy into: *SentFolder is the target Mailbox id, or "" when the account
	// has no Sent folder (the staged copy is destroyed once submitted). When
	// nil, Sender uses the server's RoleSent mailbox.
	SentFolder *backend.FolderRef
}

// client speaks JMAP over HTTP. It signs every request with the account's
// credentials and trusts the hosts the session names; it follows a redirect
// only to the same host, except from /.well-known/jmap.
type client struct {
	cfg  Config
	http *http.Client
	// stream is http without the overall timeout, for a download or the
	// event stream; their bodies are bounded by conn.StallBody instead.
	stream *http.Client

	mu      sync.Mutex
	session *gojmap.Session
	host    string // where the session came from
}

var errOtherHost = errors.New("jmap: refused a redirect to another host")

// Timeouts of one HTTP request, so a server that stops answering cannot hold
// the account's sync forever; vars so tests can shorten them.
var (
	headerTimeout  = time.Minute     // until the response headers arrive
	requestTimeout = 5 * time.Minute // a whole request, except a download or the event stream
	stallTimeout   = time.Minute     // a download with no byte arriving
)

func newClient(cfg Config) *client {
	c := &client{cfg: cfg}
	t := conn.HTTPTransport()
	t.ResponseHeaderTimeout = headerTimeout
	c.http = &http.Client{Timeout: requestTimeout, Transport: authTransport{cfg.Credentials, t}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
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
	stream := *c.http
	stream.Timeout = 0
	c.stream = &stream
	return c
}

type authTransport struct {
	creds backend.CredentialsFunc
	base  http.RoundTripper
}

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
	return a.base.RoundTrip(r)
}

// do does an HTTP request on hc and checks its status: 401 is ErrAuthFailed,
// 404 ErrNotFound.
func (c *client) do(hc *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated { // an upload's is 201
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
		resp, err := c.do(c.http, req)
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

// hasSubmission reports whether both the JMAP session and its primary mail
// account advertise urn:ietf:params:jmap:submission.
func hasSubmission(s *gojmap.Session, acct gojmap.ID) bool {
	if _, ok := s.RawCapabilities[emailsubmission.URI]; !ok {
		return false
	}
	a, ok := s.Accounts[acct]
	if !ok {
		return false
	}
	_, ok = a.RawCapabilities[emailsubmission.URI]
	return ok
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
	hresp, err := c.do(c.http, hreq)
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
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := c.do(c.stream, req)
	if err != nil {
		cancel()
		return nil, err
	}
	return conn.StallBody(resp.Body, stallTimeout, cancel), nil
}

// upload stores raw as a blob and returns its id (RFC 8620 6.1).
func (c *client) upload(ctx context.Context, raw []byte) (gojmap.ID, error) {
	s, acct, err := c.sess(ctx)
	if err != nil {
		return "", err
	}
	u := strings.ReplaceAll(s.UploadURL, "{accountId}", url.PathEscape(string(acct)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "message/rfc822")
	resp, err := c.do(c.http, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var up gojmap.UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
		return "", fmt.Errorf("jmap: upload: %w", err)
	}
	return up.ID, nil
}

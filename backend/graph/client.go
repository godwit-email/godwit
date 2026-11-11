package graph

import (
	"bytes"
	"context"
	"encoding/json"
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

// BaseURL is Graph's v1.0 root.
const BaseURL = "https://graph.microsoft.com/v1.0"

// Config is how to reach a Microsoft Graph account.
type Config struct {
	// URL is the Graph API root, normally BaseURL.
	URL         string
	Credentials backend.CredentialsFunc
	// Wait waits d out, or until ctx is done, when the server asks to back
	// off. Nil waits in real time; tests pass a fake.
	Wait func(ctx context.Context, d time.Duration) error
}

// ResponseError is Graph's refusal of a request (4xx other than 401/403/429).
type ResponseError struct {
	Status  int
	Code    string
	Message string
}

func (e *ResponseError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("graph: %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("graph: HTTP %d", e.Status)
}

func isStatus(err error, status int) bool {
	var re *ResponseError
	return errors.As(err, &re) && re.Status == status
}

func isDeltaReset(err error) bool {
	var re *ResponseError
	if !errors.As(err, &re) {
		return false
	}
	if re.Status == http.StatusGone {
		return true
	}
	return strings.EqualFold(re.Code, "syncStateNotFound") || strings.EqualFold(re.Code, "resyncRequired") ||
		strings.Contains(strings.ToLower(re.Code), "syncstatenotfound")
}

// Throttling: 429 and 503 answers are waited out (Retry-After) and retried,
// at most maxRetries times, one call per account at a time.
const (
	maxRetries     = 3
	maxBackOff     = time.Minute
	defaultBackOff = 5 * time.Second
)

// deltaPageSize is the odata.maxpagesize a delta request asks for.
const deltaPageSize = 200

type busyError struct{ backOff time.Duration }

func (e *busyError) Error() string {
	return "graph: the server is busy, try again later"
}

type client struct {
	cfg  Config
	base string
	http *http.Client

	mu       sync.Mutex
	signedIn bool
}

var errOtherHost = errors.New("graph: refused a redirect to another host")

func newClient(cfg Config) *client {
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		base = BaseURL
	}
	t := conn.HTTPTransport()
	t.ResponseHeaderTimeout = time.Minute
	return &client{
		cfg:  cfg,
		base: base,
		http: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: t,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				switch {
				case len(via) >= 10:
					return errors.New("graph: too many redirects")
				case req.URL.Host != via[0].URL.Host:
					return errOtherHost
				case via[0].URL.Scheme == "https" && req.URL.Scheme != "https":
					return errors.New("graph: refused a redirect from HTTPS to HTTP")
				}
				return nil
			},
		},
	}
}

func (c *client) host() string {
	u, err := url.Parse(c.base)
	if err != nil {
		return ""
	}
	return u.Host
}

func (c *client) account(cr backend.Credentials) string {
	return c.base + "\x00" + cr.Username
}

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

func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return defaultBackOff
	}
	if sec, err := strconv.Atoi(v); err == nil {
		if sec <= 0 {
			return 0
		}
		return min(time.Duration(sec)*time.Second, maxBackOff)
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, maxBackOff)
		}
		return 0
	}
	return defaultBackOff
}

// do runs one HTTP request against pathOrURL (relative to base unless it has a
// scheme), serialized per account, with Prefer: IdType="ImmutableId" on every
// request and odata.maxpagesize on delta requests.
func (c *client) do(ctx context.Context, method, pathOrURL, contentType string, body []byte) ([]byte, error) {
	return c.doRetrying(ctx, method, pathOrURL, contentType, body, maxRetries)
}

// doRetrying is do with at most retries retries after a back-off; past them
// a throttled request returns a *busyError.
func (c *client) doRetrying(ctx context.Context, method, pathOrURL, contentType string, body []byte, retries int) ([]byte, error) {
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	target := pathOrURL
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		u, err := url.Parse(pathOrURL)
		if err != nil {
			return nil, err
		}
		if u.Host != c.host() {
			return nil, errOtherHost
		}
		if strings.HasPrefix(c.base, "https://") && u.Scheme != "https" {
			return nil, errors.New("graph: refused a link from HTTPS to HTTP")
		}
	} else {
		target = c.base + pathOrURL
	}
	release, err := acquire(ctx, c.account(cr))
	if err != nil {
		return nil, err
	}
	defer release()
	for try := 0; ; try++ {
		raw, err := c.sendOnce(ctx, method, target, contentType, body, cr)
		var busy *busyError
		if !errors.As(err, &busy) || try == retries {
			return raw, err
		}
		if err := c.wait(ctx, busy.backOff); err != nil {
			return nil, err
		}
	}
}

func (c *client) sendOnce(ctx context.Context, method, target, contentType string, body []byte, cr backend.Credentials) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cr.Token)
	prefer := `IdType="ImmutableId"`
	if strings.HasSuffix(req.URL.Path, "/delta") { // Graph's own page is 10 messages
		prefer += ", odata.maxpagesize=" + strconv.Itoa(deltaPageSize)
	}
	req.Header.Set("Prefer", prefer)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch sc := resp.StatusCode; {
	case sc >= 200 && sc < 300:
		c.mu.Lock()
		c.signedIn = true
		c.mu.Unlock()
		return raw, nil
	case sc == http.StatusUnauthorized || sc == http.StatusForbidden:
		return nil, fmt.Errorf("graph: %w", backend.ErrAuthFailed)
	case sc == http.StatusTooManyRequests || sc == http.StatusServiceUnavailable:
		return nil, &busyError{backOff: retryAfter(resp.Header)}
	case sc >= 400 && sc < 500:
		var ge struct {
			Error struct {
				Code       string `json:"code"`
				Message    string `json:"message"`
				InnerError struct {
					Code string `json:"code"`
				} `json:"innerError"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &ge)
		code := ge.Error.Code
		if strings.EqualFold(ge.Error.InnerError.Code, "syncStateNotFound") {
			code = ge.Error.InnerError.Code
		}
		return nil, &ResponseError{Status: sc, Code: code, Message: ge.Error.Message}
	default:
		return nil, fmt.Errorf("graph: HTTP %s", resp.Status)
	}
}

func callJSON[R any](ctx context.Context, c *client, method, pathOrURL string, reqBody any) (R, error) {
	var zero R
	var body []byte
	var ct string
	if reqBody != nil {
		var err error
		body, err = json.Marshal(reqBody)
		if err != nil {
			return zero, err
		}
		ct = "application/json"
	}
	raw, err := c.do(ctx, method, pathOrURL, ct, body)
	if err != nil || len(raw) == 0 {
		return zero, err
	}
	var out R
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("graph: decode response: %w", err)
	}
	return out, nil
}

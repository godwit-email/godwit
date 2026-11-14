package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/internal/sysproxy"
)

// HTTPTransport is the HTTP backends' transport: it connects as
// IMAP and SMTP do, through the user's proxy, but to a server on this
// computer directly, and trusts TLSRoots.
func HTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{RootCAs: TLSRoots}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); !sysproxy.OnThisComputer(host) {
			if p := SystemProxy(&url.URL{Scheme: "https", Host: addr}); p != nil {
				return sysproxy.Dial(ctx, p, addr)
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return t
}

// ErrStalled is a response body that went too long without a byte arriving.
var ErrStalled = errors.New("the server stopped sending")

// StallBody is body, failing with ErrStalled once d passes with no byte
// arriving; cancel, which should cancel the request, is called then and on
// Close. It bounds a download or event stream that an overall request
// timeout would cut off.
func StallBody(body io.ReadCloser, d time.Duration, cancel context.CancelFunc) io.ReadCloser {
	b := &stallBody{ReadCloser: body, d: d, cancel: cancel}
	b.t = time.AfterFunc(d, func() { b.stalled.Store(true); cancel() })
	return b
}

type stallBody struct {
	io.ReadCloser
	d       time.Duration
	cancel  context.CancelFunc
	t       *time.Timer
	stalled atomic.Bool
}

func (b *stallBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.stalled.Load() {
		return n, ErrStalled
	}
	if n > 0 {
		b.t.Reset(b.d)
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.t.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}

// Timeouts of one HTTP backend request, so a server that stops answering
// cannot hold an account's sync forever; vars so tests can shorten them.
var (
	HeaderTimeout  = time.Minute     // until the response headers arrive
	RequestTimeout = 5 * time.Minute // a whole request, except on Streaming
	StallTimeout   = time.Minute     // a StallBody with no byte arriving
)

// NewHTTPClient is an HTTP backend's client for one account: requests go
// through rt, nil for t itself, which sends them over t. It waits
// HeaderTimeout for an answer and RequestTimeout for a whole request, and
// follows a redirect only as SameHost allows.
func NewHTTPClient(t *http.Transport, rt http.RoundTripper) *http.Client {
	t.ResponseHeaderTimeout = HeaderTimeout
	if rt == nil {
		rt = t
	}
	return &http.Client{Transport: rt, Timeout: RequestTimeout, CheckRedirect: SameHost}
}

// Streaming is c without the overall timeout, for a download or an event
// stream that may outlast it: its body is bounded by StallBody, or by the
// stream's own idle timer, instead.
func Streaming(c *http.Client) *http.Client {
	s := *c
	s.Timeout = 0
	return &s
}

// ErrOtherHost is a redirect, or a link, away from the account's host.
var ErrOtherHost = errors.New("refused a redirect to another host")

// SameHost is the HTTP backends' redirect policy: never from HTTPS to plain
// HTTP, and only to the host the request was for, so the credentials stay
// there (ErrOtherHost).
func SameHost(req *http.Request, via []*http.Request) error {
	switch {
	case len(via) >= 10:
		return errors.New("too many redirects")
	case via[0].URL.Scheme == "https" && req.URL.Scheme != "https":
		return errors.New("refused a redirect from HTTPS to HTTP")
	case req.URL.Host != via[0].URL.Host:
		return ErrOtherHost
	}
	return nil
}

// HTTPStatusErr is the error for the statuses every HTTP backend reads
// alike: 401 is backend.ErrAuthFailed, 429 a *BusyError. Nil for any other,
// which the backend reads by its protocol.
func HTTPStatusErr(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return backend.ErrAuthFailed
	case http.StatusTooManyRequests:
		return &BusyError{BackOff: RetryAfter(resp.Header)}
	}
	return nil
}

// Throttling: a busy answer is waited out and the request sent again, at
// most MaxRetries times, each wait at most MaxBackOff.
const (
	MaxRetries = 3
	MaxBackOff = time.Minute
)

// BusyError is a server's answer to slow down and try again after BackOff,
// 0 when it named no time. Err, when set, is the answer as the backend read it.
type BusyError struct {
	BackOff time.Duration
	Err     error
}

func (e *BusyError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "the server is busy, try again later"
}

func (e *BusyError) Unwrap() error { return e.Err }

// RetryAfter is h's Retry-After, in seconds or as a date; 0 when it names
// none, or no time in the future.
func RetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if sec, err := strconv.Atoi(v); err == nil {
		return max(time.Duration(sec)*time.Second, 0)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// BackOff is how long to wait before sending again after try (0 for the
// first) failed with err: a *BusyError's BackOff, or 1s doubling with each
// try when it named none, at most MaxBackOff. False when err is not a
// *BusyError, or the retries are spent.
func BackOff(err error, try int) (time.Duration, bool) {
	var busy *BusyError
	if !errors.As(err, &busy) || try >= MaxRetries {
		return 0, false
	}
	if busy.BackOff > 0 {
		return min(busy.BackOff, MaxBackOff), true
	}
	return min(time.Second<<try, MaxBackOff), true
}

// Serial calls try for account, one call per account at a time, back-off
// waits included, so an account stays under the server's concurrency limit
// and stops while it is busy. It sends again after a *BusyError, waiting it
// out with wait (nil waits in real time), as BackOff says.
func Serial(ctx context.Context, account string, wait func(context.Context, time.Duration) error, try func() error) error {
	release, err := Acquire(ctx, account)
	if err != nil {
		return err
	}
	defer release()
	if wait == nil {
		wait = Sleep
	}
	for n := 0; ; n++ {
		err := try()
		d, ok := BackOff(err, n)
		if !ok {
			return err
		}
		if err := wait(ctx, d); err != nil {
			return fmt.Errorf("%s: waiting out the server's back-off: %w", serverOf(account), err)
		}
	}
}

// serverOf is the host of account's server, for an error to name: the key
// leads with the server's URL.
func serverOf(account string) string {
	base, _, _ := strings.Cut(account, "\x00")
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// Sleep waits d out, or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// accounts holds a one-slot channel per account, taken for the length of a
// call, and how many calls hold it or wait for it: an account's entry goes
// once none does.
var accounts = struct {
	sync.Mutex
	m map[string]*slot
}{m: map[string]*slot{}}

type slot struct {
	ch    chan struct{}
	users int
}

// Acquire takes account's slot, waiting while another call holds it.
// account names the server and the user: the API's URL, a NUL, the user
// name. A wait that ctx ends says so and names the URL's host, so a request's
// deadline is never the bare context error (#755).
func Acquire(ctx context.Context, account string) (release func(), err error) {
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
		return nil, fmt.Errorf("%s: waiting for the account's other requests: %w", serverOf(account), ctx.Err())
	}
}

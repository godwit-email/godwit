package plugins

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Network limits of ctx.net.fetch (spec 5.5).
const (
	MaxFetchResponse = 5 << 20
	FetchTimeout     = 30 * time.Second
	maxRedirects     = 5
)

// NetOptions configures a Client. Zero values are the real network and the
// spec's limits; AllowHTTP and AllowLoopback exist for tests only.
type NetOptions struct {
	Resolver interface {
		LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	}
	Dial                     func(ctx context.Context, network, addr string) (net.Conn, error)
	AllowHTTP, AllowLoopback bool
	MaxBody                  int64         // response body; 0 = MaxFetchResponse
	Timeout                  time.Duration // whole call, redirects and body included; 0 = FetchTimeout
	UserAgent                string        // "Godwit-Plugin/<version>"
	rootCAs                  *x509.CertPool
}

// NetError is a fetch the policy refused. Reason is a stable code:
// host_not_allowed, scheme, ip_literal, userinfo, private_address,
// too_many_redirects, too_large or timeout.
type NetError struct{ Reason, Host string }

func (e *NetError) Error() string {
	if e.Host != "" {
		return "fetch denied (" + e.Reason + "): " + e.Host
	}
	return "fetch denied (" + e.Reason + ")"
}

func deny(reason, host string) error { return &NetError{reason, host} }

// Client is ctx.net.fetch's HTTP client. It resolves names itself and dials
// only addresses it has checked (no DNS rebinding between check and connect),
// never uses a proxy, and re-checks every redirect.
type Client struct {
	opts NetOptions
	tr   *http.Transport
}

func NewClient(opts NetOptions) *Client {
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Dial == nil {
		opts.Dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	opts.MaxBody = cmp.Or(opts.MaxBody, MaxFetchResponse)
	opts.Timeout = cmp.Or(opts.Timeout, FetchTimeout)
	c := &Client{opts: opts}
	c.tr = &http.Transport{
		Proxy:                  nil, // never the environment's proxy
		DialContext:            c.dial,
		TLSClientConfig:        &tls.Config{RootCAs: opts.rootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 64 << 10,
		IdleConnTimeout:        90 * time.Second,
	}
	return c
}

// blocked are the ranges a plugin may never reach, besides what netip
// classifies as loopback, private, link-local, multicast or unspecified.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),  // reserved and broadcast
	netip.MustParsePrefix("::/96"),        // IPv4-compatible
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), // 6to4 embeds an IPv4 address
}

func (c *Client) blocked(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap() // ::ffff:a.b.c.d is a.b.c.d
	if c.opts.AllowLoopback && a.IsLoopback() {
		return false
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	return slices.ContainsFunc(blocked, func(p netip.Prefix) bool { return p.Contains(a) })
}

// dial resolves addr's host and connects to a checked address. Any blocked
// address in the answer refuses the whole name.
func (c *Client) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) != nil {
		return nil, deny("ip_literal", "") // check already refuses these; never dial one
	}
	ips, err := c.opts.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "no address", Name: host, IsNotFound: true}
	}
	for _, ip := range ips {
		if c.blocked(ip.IP) {
			return nil, deny("private_address", net.JoinHostPort(host, port))
		}
	}
	var errs []error
	for _, ip := range ips {
		conn, err := c.opts.Dial(ctx, "tcp", net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// check validates one hop's URL against the policy and returns its host:port.
func (c *Client) check(u *url.URL, allowed []string) (string, error) {
	if u.Scheme != "https" && (u.Scheme != "http" || !c.opts.AllowHTTP) {
		return "", deny("scheme", "")
	}
	if u.User != nil {
		return "", deny("userinfo", "")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || u.Opaque != "" {
		return "", deny("host_not_allowed", "")
	}
	if strings.Contains(host, ":") || net.ParseIP(host) != nil {
		return "", deny("ip_literal", "")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	hp := net.JoinHostPort(host, port)
	for _, a := range allowed {
		a = strings.ToLower(a)
		if !strings.Contains(a, ":") {
			a += ":443" // a declared host without a port is port 443
		}
		if a == hp {
			return hp, nil
		}
	}
	return hp, deny("host_not_allowed", hp)
}

// dropped are headers a plugin may not send, by canonical name or prefix.
func dropped(k string) bool {
	k = http.CanonicalHeaderKey(k)
	return k == "Cookie" || k == "Host" || k == "User-Agent" || strings.HasPrefix(k, "Proxy-")
}

// Do fetches req if its URL is allowed (host[:port], 443 by default) and
// returns the response, the request's host:port for the audit, and an error;
// a policy refusal is a *NetError. No partial body is ever returned.
func (c *Client) Do(ctx context.Context, allowed []string, req FetchRequest) (FetchResponse, string, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return FetchResponse{}, "", errors.New("invalid URL")
	}
	hp, err := c.check(u, allowed)
	if err != nil {
		return FetchResponse{}, hp, err
	}
	if len(req.Body) > MaxFetchBody {
		return FetchResponse{}, hp, deny("too_large", "")
	}
	tctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	method := cmp.Or(strings.ToUpper(req.Method), http.MethodGet)
	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(tctx, method, u.String(), body)
	if err != nil {
		return FetchResponse{}, hp, errors.New("invalid request")
	}
	for k, v := range req.Headers {
		if !dropped(k) {
			hr.Header.Set(k, v)
		}
	}
	hr.Header.Set("User-Agent", c.opts.UserAgent)

	hc := &http.Client{Transport: c.tr, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return deny("too_many_redirects", "")
		}
		if _, err := c.check(next.URL, allowed); err != nil {
			return err
		}
		// Authorization is for the first host only: once the chain has left
		// it, it is never sent again.
		left := !strings.EqualFold(next.URL.Host, via[0].URL.Host)
		for _, r := range via[1:] {
			left = left || !strings.EqualFold(r.URL.Host, via[0].URL.Host)
		}
		if left {
			next.Header.Del("Authorization")
		}
		return nil
	}}
	resp, err := hc.Do(hr)
	if err != nil {
		return FetchResponse{}, hp, c.fail(ctx, tctx, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxBody+1))
	if err != nil {
		return FetchResponse{}, hp, c.fail(ctx, tctx, err)
	}
	if int64(len(b)) > c.opts.MaxBody {
		return FetchResponse{}, hp, deny("too_large", "")
	}
	out := FetchResponse{Status: resp.StatusCode, Headers: map[string]string{}, Body: string(b)}
	for k, v := range resp.Header {
		out.Headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return out, hp, nil
}

// fail maps a transport error: a policy refusal as is, our own deadline as
// "timeout", the caller's cancellation as its error. The URL (path, query)
// is left out of the message.
func (c *Client) fail(ctx, tctx context.Context, err error) error {
	var ne *NetError
	switch {
	case errors.As(err, &ne):
		return ne
	case ctx.Err() != nil:
		return ctx.Err()
	case tctx.Err() != nil:
		return deny("timeout", "")
	}
	if ue := (*url.Error)(nil); errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("fetch failed: %w", err)
}

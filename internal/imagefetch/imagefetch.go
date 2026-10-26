// Package imagefetch fetches and checks images: the reader's image proxy
// and prefetch download remote ones through a client of its own (public
// hosts only, no cookies, no Referer, a generic User-Agent), and a message's
// inline ones count against one Budget. Every image must be a PNG, JPEG,
// GIF or WebP within the per-image limits.
package imagefetch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // Config
	_ "image/jpeg" // Config
	_ "image/png"  // Config
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	_ "golang.org/x/image/webp" // Config
	xproxy "golang.org/x/net/proxy"
)

// The budget of one message's images (global constraints).
const (
	MaxImages      = 50       // images, remote and inline
	MaxImageBytes  = 5 << 20  // one image
	MaxTotalBytes  = 20 << 20 // all images together
	MaxPixels      = 40_000_000
	MaxTotalPixels = 100_000_000 // all images together, decoded
	Timeout        = 15 * time.Second
	MaxRedirects   = 5
)

// The reasons an image is refused.
var (
	ErrTooMany          = errors.New("too many images")
	ErrTooLarge         = errors.New("image too large")
	ErrTotalTooLarge    = errors.New("images too large together")
	ErrPixels           = errors.New("image has too many pixels")
	ErrTotalPixels      = errors.New("images have too many pixels together")
	ErrFormat           = errors.New("not a PNG, JPEG, GIF or WebP image")
	ErrScheme           = errors.New("image address is not http or https")
	ErrHost             = errors.New("image host is not public")
	errTooManyRedirects = fmt.Errorf("more than %d redirects", MaxRedirects)
)

// Formats are the formats shown, as image.DecodeConfig names them.
var Formats = map[string]bool{"png": true, "jpeg": true, "gif": true, "webp": true}

// NewClient is a client of its own: NewTransport's, http(s) redirects only
// and at most MaxRedirects of them, Referer dropped on each.
func NewClient(allowLoopback bool) *http.Client {
	return &http.Client{Transport: NewTransport(allowLoopback), Timeout: Timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > MaxRedirects {
			return errTooManyRedirects
		}
		if !IsHTTP(req.URL) {
			return ErrScheme
		}
		req.Header.Del("Referer")
		return nil
	}}
}

// SystemProxy is the proxy for a request to u, nil for none (only http://
// and socks5:// ones are used); the app sets it to the user's (sysproxy).
var SystemProxy func(u *url.URL) *url.URL

// fakeIP is the range proxies in TUN mode answer DNS with (fake-ip): such an
// address stands for the name looked up, which the proxy resolves itself.
var fakeIP = netip.MustParsePrefix("198.18.0.0/15")

// lookup resolves a name; tests stand in for DNS.
var lookup = net.DefaultResolver.LookupNetIP

// NewTransport reaches public hosts only (ErrHost otherwise). It resolves a
// host itself and dials the addresses it checked (pinned), so a name that
// resolves differently on the second ask (DNS rebinding) changes nothing:
// every answer must be public, or every one a fake-ip one (a TUN proxy's).
// Through the system proxy (SystemProxy) it tunnels to the pinned address,
// never the name, so the proxy cannot resolve it elsewhere; for a fake-ip
// name it gives the name to a proxy on this computer, which knows what the
// fake address stands for, and refuses a proxy elsewhere. allowLoopback lets
// tests reach httptest servers, which listen on loopback.
func NewTransport(allowLoopback bool) *Transport {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if allowLoopback {
			return (&net.Dialer{Timeout: Timeout}).DialContext(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrHost
		}
		host = strings.ToLower(host)
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return nil, ErrHost
		}
		addrs, fake, err := resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		var proxy *url.URL
		if SystemProxy != nil {
			scheme, _ := ctx.Value(schemeKey{}).(string) // RoundTrip's; the dial's context keeps the request's values
			if scheme == "" {
				scheme = map[bool]string{true: "https", false: "http"}[port == "443"]
			}
			proxy = SystemProxy(&url.URL{Scheme: scheme, Host: address})
		}
		switch {
		case proxy == nil:
			return dialPinned(ctx, network, addrs, port, func(ip netip.Addr) bool { return PublicIP(ip) || fake && fakeIP.Contains(ip) })
		case !fake:
			return dialProxy(ctx, proxy, net.JoinHostPort(addrs[0].String(), port))
		case onThisComputer(proxy):
			return dialProxy(ctx, proxy, address) // the fake address means nothing to the proxy; the name does
		}
		return nil, ErrHost // a proxy elsewhere has no idea what our fake-ip resolver meant
	}
	return &Transport{&http.Transport{DialContext: dial, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4,
		TLSHandshakeTimeout: Timeout, ResponseHeaderTimeout: Timeout, MaxResponseHeaderBytes: 64 << 10}}
}

// Transport is NewTransport's: an http.Transport whose dials know the
// scheme of the request they are for, which picks the proxy.
type Transport struct{ *http.Transport }

type schemeKey struct{}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.Transport.RoundTrip(req.WithContext(context.WithValue(req.Context(), schemeKey{}, req.URL.Scheme)))
}

// resolve is host's addresses to dial, IPv4 first: a literal one as is,
// when public; a name's answers when every one is public, or every one a
// fake-ip one (fake).
func resolve(ctx context.Context, host string) (addrs []netip.Addr, fake bool, err error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip = ip.Unmap(); !PublicIP(ip) {
			return nil, false, ErrHost
		}
		return []netip.Addr{ip}, false, nil
	}
	answers, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, false, err
	}
	fake = len(answers) > 0
	for _, a := range answers {
		a = a.Unmap()
		fake = fake && fakeIP.Contains(a)
		if a.Is4() {
			addrs = append([]netip.Addr{a}, addrs...)
		} else {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return nil, false, ErrHost
	}
	if !fake {
		for _, a := range addrs {
			if !PublicIP(a) {
				return nil, false, ErrHost
			}
		}
	}
	return addrs, fake, nil
}

// fallbackDelay is how long each pinned address gets alone before the next
// is dialed too, as net.Dialer's Happy Eyeballs does.
const fallbackDelay = 300 * time.Millisecond

// dialPinned races addrs, each started fallbackDelay after the one before,
// so one that drops connections silently cannot hold up the rest; the
// first to connect wins. Each is checked again as it connects (allowed).
func dialPinned(ctx context.Context, network string, addrs []netip.Addr, port string, allowed func(netip.Addr) bool) (net.Conn, error) {
	d := &net.Dialer{Timeout: Timeout, Control: func(_, address string, _ syscall.RawConn) error {
		if ap, err := netip.ParseAddrPort(address); err != nil || !allowed(ap.Addr().Unmap()) {
			return ErrHost
		}
		return nil
	}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		c   net.Conn
		err error
	}
	results := make(chan result, len(addrs))
	for i, a := range addrs {
		go func() {
			select {
			case <-time.After(time.Duration(i) * fallbackDelay):
			case <-ctx.Done():
				results <- result{err: ctx.Err()}
				return
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			results <- result{c, err}
		}()
	}
	var err error
	for n := range len(addrs) {
		r := <-results
		if r.err == nil {
			go func() { // the losers: close any that connected too
				for range len(addrs) - n - 1 {
					if r := <-results; r.c != nil {
						r.c.Close()
					}
				}
			}()
			return r.c, nil
		}
		if err == nil || !errors.Is(r.err, context.Canceled) {
			err = r.err
		}
	}
	return nil, err
}

// onThisComputer is whether proxy listens on this computer.
func onThisComputer(proxy *url.URL) bool {
	h := strings.ToLower(proxy.Hostname())
	ip, err := netip.ParseAddr(h)
	return h == "localhost" || err == nil && ip.Unmap().IsLoopback()
}

// dialProxy opens a tunnel to target (host:port) through proxy: an http://
// one by CONNECT, a socks5:// one by SOCKS5.
// shortcut: plain http:// images go through CONNECT too; send them as absolute-URI requests if a proxy refuses CONNECT to port 80.
func dialProxy(ctx context.Context, proxy *url.URL, target string) (net.Conn, error) {
	d := &net.Dialer{Timeout: Timeout}
	switch proxy.Scheme {
	case "socks5":
		s, err := xproxy.SOCKS5("tcp", proxy.Host, nil, d)
		if err != nil {
			return nil, err
		}
		return s.(xproxy.ContextDialer).DialContext(ctx, "tcp", target)
	case "http":
		c, err := d.DialContext(ctx, "tcp", proxy.Host)
		if err != nil {
			return nil, err
		}
		deadline := time.Now().Add(Timeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		c.SetDeadline(deadline)
		br := bufio.NewReader(c)
		var resp *http.Response
		if _, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err == nil {
			resp, err = http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		}
		switch {
		case err != nil:
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("proxy refused the tunnel: %s", resp.Status)
		case br.Buffered() > 0:
			err = errors.New("proxy sent data before the tunnel opened")
		}
		if err != nil {
			c.Close()
			return nil, err
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
	return nil, fmt.Errorf("unsupported proxy %q", proxy.Scheme)
}

// notGlobal are ranges that are not public hosts although IsPrivate and the
// like do not name them: this network, shared (CGNAT / overlay) space,
// IETF protocol, benchmarking and documentation ranges, reserved space,
// deprecated IPv6 site-local, local-use NAT64, 6to4.
var notGlobal = func() (out []netip.Prefix) {
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fec0::/10", "64:ff9b:1::/48", "2001:2::/48", "2002::/16", "2001::/23", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// globalUnicast6 is the one IPv6 block assigned for global use; everything
// outside it (SRv6 SIDs 5f00::/16, unassigned space, ...) is refused.
var globalUnicast6 = netip.MustParsePrefix("2000::/3")

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// PublicIP is false for loopback, private, link-local, unspecified, multicast
// and other non-global addresses (shared 100.64.0.0/10 included).
func PublicIP(ip netip.Addr) bool {
	if nat64.Contains(ip) { // the translated IPv4 address decides
		b := ip.As16()
		return PublicIP(netip.AddrFrom4([4]byte(b[12:])))
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.Is6() && !globalUnicast6.Contains(ip) { // IPv6 is allowed only inside 2000::/3
		return false
	}
	for _, p := range notGlobal {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// IsHTTP reports an http or https URL.
func IsHTTP(u *url.URL) bool { return u.Scheme == "http" || u.Scheme == "https" }

// Budget is one message's images' budget: their count, bytes and decoded
// pixels, inline and remote together. It is safe
// for concurrent use.
type Budget struct {
	count  atomic.Int64 // images taken
	bytes  atomic.Int64 // bytes left
	pixels atomic.Int64 // decoded pixels left
}

// NewBudget is a whole budget.
func NewBudget() *Budget {
	b := &Budget{}
	b.bytes.Store(MaxTotalBytes)
	b.pixels.Store(MaxTotalPixels)
	return b
}

// Take counts one more image; ErrTooMany past MaxImages.
func (b *Budget) Take() error {
	if b.count.Add(1) > MaxImages {
		return ErrTooMany
	}
	return nil
}

// TakePixels takes px decoded pixels; ErrTotalPixels when fewer are left.
func (b *Budget) TakePixels(px int64) error {
	for {
		left := b.pixels.Load()
		if px > left {
			return ErrTotalPixels
		}
		if b.pixels.CompareAndSwap(left, left-px) {
			return nil
		}
	}
}

// TakeBytes takes n bytes; ErrTotalTooLarge when fewer are left.
func (b *Budget) TakeBytes(n int64) error {
	for {
		left := b.bytes.Load()
		if n > left {
			return ErrTotalTooLarge
		}
		if b.bytes.CompareAndSwap(left, left-n) {
			return nil
		}
	}
}

// TakeInline takes an inline image of size bytes and px pixels: its count,
// bytes and pixels, all or none of them but the count (as a remote image's,
// which is counted once asked for).
func (b *Budget) TakeInline(size, px int64) error {
	if err := b.Take(); err != nil {
		return err
	}
	if err := b.TakeBytes(size); err != nil {
		return err
	}
	if err := b.TakePixels(px); err != nil {
		b.bytes.Add(size)
		return err
	}
	return nil
}

// Dims is data's format and size, read from its header: a PNG, JPEG, GIF
// or WebP image (by content) of at most MaxPixels.
func Dims(data []byte) (format string, width, height int, err error) {
	// ICO registers a global decoder; keep it outside the ordinary image boundary.
	if bytes.HasPrefix(data, []byte{0, 0, 1, 0}) {
		return "", 0, 0, ErrFormat
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !Formats[format] {
		return "", 0, 0, ErrFormat
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return "", 0, 0, ErrPixels
	}
	return format, cfg.Width, cfg.Height, nil
}

// Config is Dims, with the pixels.
func Config(data []byte) (format string, px int64, err error) {
	format, w, h, err := Dims(data)
	return format, int64(w) * int64(h), err
}

// Download gets src (http or https) with c: at most MaxImageBytes of it.
func Download(ctx context.Context, c *http.Client, src string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	if !IsHTTP(req.URL) {
		return nil, ErrScheme
	}
	req.Header.Set("User-Agent", UserAgent)
	// Only the formats Dims reads: a CDN must not pick AVIF for us.
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/gif;q=0.9,*/*;q=0.5")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	// Cancel before Close: closing a body not read to the end would read
	// up to 256 KiB more of it to reuse the connection.
	defer func() { cancel(); resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	switch {
	case err != nil:
		return nil, err
	case len(data) > MaxImageBytes:
		return nil, ErrTooLarge
	}
	return data, nil
}

// Fetch downloads src and checks it: its bytes, format and size in pixels.
func Fetch(ctx context.Context, c *http.Client, src string) (data []byte, format string, width, height int, err error) {
	if data, err = Download(ctx, c, src); err != nil {
		return nil, "", 0, 0, err
	}
	format, width, height, err = Dims(data)
	if err != nil {
		return nil, "", 0, 0, err
	}
	return data, format, width, height, nil
}

// UserAgent is what the proxy sends: no browser, no app version.
const UserAgent = "Mozilla/5.0 (compatible; ImageProxy)"

// DataURL decodes a data: URL's bytes, at most MaxImageBytes of them.
func DataURL(src string) ([]byte, error) {
	if len(src) < len("data:") || !strings.EqualFold(src[:len("data:")], "data:") {
		return nil, ErrScheme
	}
	meta, payload, ok := strings.Cut(src[len("data:"):], ",")
	if !ok {
		return nil, ErrFormat
	}
	if len(payload) > MaxImageBytes/3*4+4 {
		return nil, ErrTooLarge
	}
	var data []byte
	var err error
	if strings.HasSuffix(strings.ToLower(meta), ";base64") {
		data, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), ""))
	} else {
		var s string
		s, err = url.PathUnescape(payload)
		data = []byte(s)
	}
	if err != nil {
		return nil, ErrFormat
	}
	if len(data) > MaxImageBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// Encode is data, an image of format (as Config names it), as a data: URL.
func Encode(format string, data []byte) string {
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data)
}

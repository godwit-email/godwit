// Package imagefetch fetches and checks images: the reader's image proxy
// and prefetch download remote ones through a client of its own (public
// hosts only, no cookies, no Referer, a generic User-Agent), and a message's
// inline ones count against one Budget. Every image must be a PNG, JPEG,
// GIF or WebP within the per-image limits.
package imagefetch

import (
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

// NewClient is a client of its own: no proxy, no cookie jar, no Referer,
// public hosts only (checked on the address dialed, after DNS), http(s)
// redirects only and at most MaxRedirects of them. allowLoopback lets
// tests reach httptest servers, which listen on loopback.
func NewClient(allowLoopback bool) *http.Client {
	d := &net.Dialer{Timeout: Timeout, Control: func(_, address string, _ syscall.RawConn) error {
		if allowLoopback {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrHost
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !PublicIP(ip.Unmap()) {
			return ErrHost
		}
		return nil
	}}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4,
		TLSHandshakeTimeout: Timeout, ResponseHeaderTimeout: Timeout, MaxResponseHeaderBytes: 64 << 10}
	return &http.Client{Transport: tr, Timeout: Timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
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

// notGlobal are ranges that are not public hosts although IsPrivate and the
// like do not name them: this network, shared (CGNAT / overlay) space,
// IETF protocol, benchmarking and documentation ranges, reserved space,
// deprecated IPv6 site-local, local-use NAT64.
var notGlobal = func() (out []netip.Prefix) {
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fec0::/10", "64:ff9b:1::/48", "2001:2::/48"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

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
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif,*/*;q=0.5")
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

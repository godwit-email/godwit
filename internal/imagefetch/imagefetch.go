// Package imagefetch fetches and checks a message's images within one
// budget: the native reader's loader and the reader page's Image share it.
// Remote images come through a client of its own (public hosts only, no
// cookies, no Referer); every image, remote or inline, must be a PNG, JPEG,
// GIF or WebP within the per-image and per-message limits.
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
	Workers        = 4        // downloads and decodes at once
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
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, DisableKeepAlives: true,
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

// PublicIP is false for loopback, private, link-local, unspecified and
// multicast addresses.
func PublicIP(ip netip.Addr) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified())
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

// BytesLeft is what the bytes' budget has left.
func (b *Budget) BytesLeft() int64 { return b.bytes.Load() }

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

// GivePixels gives back pixels taken for an image not shown after all.
func (b *Budget) GivePixels(px int64) { b.pixels.Add(px) }

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

// Config is data's format and pixels, read from its header: a PNG, JPEG,
// GIF or WebP image (by content) of at most MaxPixels.
func Config(data []byte) (format string, px int64, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !Formats[format] {
		return "", 0, ErrFormat
	}
	px = int64(cfg.Width) * int64(cfg.Height)
	if px > MaxPixels {
		return "", 0, ErrPixels
	}
	return format, px, nil
}

// Check is Config, with the pixels taken from the budget.
func (b *Budget) Check(data []byte) (format string, px int64, err error) {
	format, px, err = Config(data)
	if err == nil {
		err = b.TakePixels(px)
	}
	if err != nil {
		return "", 0, err
	}
	return format, px, nil
}

// Download gets src (http or https) with c, at most MaxImageBytes of it,
// reserving the bytes from the budget as they are read: once it is gone
// no request goes out and reads in flight stop.
func (b *Budget) Download(ctx context.Context, c *http.Client, src string) ([]byte, error) {
	held := reserve(&b.bytes, readChunk) // the first read's share: none left, no request
	if held == 0 {
		return nil, ErrTotalTooLarge
	}
	body := &budgetReader{left: &b.bytes, held: held}
	defer body.release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	if !IsHTTP(req.URL) {
		return nil, ErrScheme
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	// Cancel before Close: closing a body not read to the end would read
	// up to 256 KiB more of it, past the budget, to reuse the connection.
	defer func() { cancel(); resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image: %s", resp.Status)
	}
	body.r = resp.Body
	data, err := io.ReadAll(io.LimitReader(body, MaxImageBytes+1))
	switch {
	case err != nil:
		return nil, err
	case len(data) > MaxImageBytes:
		return nil, ErrTooLarge
	}
	return data, nil
}

// readChunk is the most a read reserves of the budget at once.
const readChunk = 32 << 10

// reserve takes up to want bytes of left in one step; 0 when none is left.
func reserve(left *atomic.Int64, want int64) int64 {
	for {
		l := left.Load()
		if l <= 0 {
			return 0
		}
		n := min(want, l)
		if left.CompareAndSwap(l, l-n) {
			return n
		}
	}
}

// budgetReader reads only bytes reserved from left beforehand, so readers
// at the same time never overrun it together; release gives back what
// was reserved and not read.
type budgetReader struct {
	r    io.Reader
	left *atomic.Int64
	held int64 // reserved, not read yet
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.held == 0 {
		if b.held = reserve(b.left, min(int64(len(p)), readChunk)); b.held == 0 {
			return 0, ErrTotalTooLarge
		}
	}
	n, err := b.r.Read(p[:min(int64(len(p)), b.held)])
	b.held -= int64(n)
	return n, err
}

func (b *budgetReader) release() {
	b.left.Add(b.held)
	b.held = 0
}

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
